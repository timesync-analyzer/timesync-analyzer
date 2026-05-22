package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"timesync-analyzer/src/internal/adapter"
	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/storage"

	pb "timesync-analyzer/protocol/generated"

	"github.com/pebbe/zmq4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type App struct {
	adapter             adapter.Adapter
	storage             storage.Storage
	windowMetricsSlider MetricsWindowSlider
	logger              *zap.Logger
	cfg                 config.Config
	msgCh               chan *pb.MetricsWrapper
	wg                  sync.WaitGroup
}

func NewApp(cfg config.Config, store storage.Storage, logger *zap.Logger) (*App, error) {
	server, err := adapter.NewServer(cfg.Zmq, logger)
	if err != nil {
		return nil, fmt.Errorf("can't create zmq server: %w", err)
	}

	slider := NewMetricsWindowSlider(store, logger, cfg.Slider.CalculateInterval, cfg.Slider.ObservationInterval)

	return &App{
		adapter:             server,
		windowMetricsSlider: *slider,
		storage:             store,
		logger:              logger,
		cfg:                 cfg,
		msgCh:               make(chan *pb.MetricsWrapper, cfg.Worker.QueueSize),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer a.adapter.Close()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.windowMetricsSlider.Run(ctx); err != nil {
			a.logger.Error("window metrics slider error", zap.Error(err))
		}
	}()
	a.startWorkers(ctx)

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("Shutting down gracefully")
			close(a.msgCh)
			a.wg.Wait()
			return ctx.Err()
		default:
		}

		data, err := a.adapter.Read()
		if err != nil {
			if zmq4.AsErrno(err) == zmq4.Errno(zmq4.ETERM) {
				close(a.msgCh)
				a.wg.Wait()
				return nil
			}
			continue
		}

		var wrapper pb.MetricsWrapper
		if err := proto.Unmarshal(data, &wrapper); err != nil {
			a.logger.Warn("Failed to unmarshal", zap.Error(err))
			continue
		}

		select {
		case a.msgCh <- &wrapper:
		default:
			a.logger.Warn("Message queue full, dropping message",
				zap.Int32("type", int32(wrapper.Type)),
				zap.String("node", wrapper.GetNodeName()))
		}
	}
}

func (a *App) startWorkers(ctx context.Context) {
	for range a.cfg.Worker.NumWorkers {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for wrapper := range a.msgCh {
				a.handleMsg(ctx, wrapper)
			}
		}()
	}
}

func (a *App) handleMsg(ctx context.Context, wrapper *pb.MetricsWrapper) {
	switch wrapper.Type {
	case pb.MessageType_MESSAGE_TYPE_PHC2SYS:
		a.handlePhc2sys(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetPhc2Sys())
	case pb.MessageType_MESSAGE_TYPE_PTP4L:
		a.handlePtp4l(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetPtp4L())
	case pb.MessageType_MESSAGE_TYPE_SYSTEM:
		a.handleSystem(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetSystem())
	case pb.MessageType_MESSAGE_TYPE_PPS:
		a.handlePps(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetPps())
	case pb.MessageType_MESSAGE_TYPE_PTP4L_PORT_EVENT:
		a.handlePtp4lPortEvent(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetPtp4LPortEvent())
	case pb.MessageType_MESSAGE_TYPE_PTP_TOPOLOGY:
		a.handlePtpTopology(ctx, wrapper.GetNodeName(), wrapper.GetTimestamp().AsTime(), wrapper.GetPtpTopology())
	default:
		a.logger.Warn("Unknown message type", zap.Int32("type", int32(wrapper.Type)))
	}
}

func (a *App) resolveOrInsertNode(ctx context.Context, hostname string) (int32, error) {
	nodeID, ok := a.storage.ResolveNodeID(hostname)
	if ok {
		return nodeID, nil
	}

	if err := a.storage.InsertNode(ctx, hostname); err != nil {
		return 0, fmt.Errorf("insert node %q: %w", hostname, err)
	}

	nodeID, ok = a.storage.ResolveNodeID(hostname)
	if !ok {
		return 0, fmt.Errorf("node %q not found after insert", hostname)
	}
	return nodeID, nil
}

func (a *App) handlePtp4lPortEvent(ctx context.Context, node string, timestamp time.Time, m *pb.Ptp4LPortEvent) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	if !strings.Contains(m.Interface, "/") {
		if err := a.storage.UpdateNodeInfo(ctx, node, strings.ToLower(m.ToState), m.Interface, m.AdapterName); err != nil {
			a.logger.Error("Failed to update node interface", zap.Error(err), zap.String("node", node))
		}
	}

	if err := a.storage.InsertPtp4lPortEvent(ctx, timestamp, nodeID, m.Port, m.Interface, m.FromState, m.ToState, m.EventTrigger); err != nil {
		a.logger.Error("Failed to insert ptp4l port event", zap.Error(err), zap.String("node", node))
	}
}

func (a *App) handlePtpTopology(ctx context.Context, node string, timestamp time.Time, m *pb.PtpTopologySnapshot) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	snapshot := storage.PtpTopologySnapshot{
		LocalClockIdentity:  m.GetLocalClockIdentity(),
		ParentClockIdentity: m.GetParentClockIdentity(),
		ParentPort:          m.GetParentPort(),
		GrandmasterIdentity: m.GetGrandmasterIdentity(),
		StepsRemoved:        m.GetStepsRemoved(),
		PathDelayNs:         m.GetMeanPathDelayNs(),
		ChildPort:           m.GetChildPort(),
	}
	if err := a.storage.InsertPtpTopologySnapshot(ctx, timestamp, nodeID, snapshot); err != nil {
		a.logger.Error("Failed to insert ptp topology snapshot", zap.Error(err), zap.String("node", node))
	}
	a.logger.Info("Handle ptp shanpshot",  zap.String("node", node))
}

func (a *App) handlePtp4l(ctx context.Context, node string, timestamp time.Time, m *pb.Ptp4LMetrics) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	if err := a.storage.InsertPtp4l(ctx, timestamp, nodeID, m.OffsetNs, m.Frequency, m.PathDelay); err != nil {
		a.logger.Error("Failed to insert ptp4l metrics", zap.Error(err), zap.String("node", node))
	}
}

func (a *App) handlePps(ctx context.Context, node string, timestamp time.Time, m *pb.PPSMetrics) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	if err := a.storage.InsertPps(ctx, timestamp, nodeID, m.OffsetNs); err != nil {
		a.logger.Error("Failed to insert ptp4l metrics", zap.Error(err), zap.String("node", node))
	}
}

func (a *App) handlePhc2sys(ctx context.Context, node string, timestamp time.Time, m *pb.Phc2SysMetrics) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	if err := a.storage.InsertPhc2sys(ctx, timestamp, nodeID, m.OffsetNs, m.Frequency, m.PathDelay); err != nil {
		a.logger.Error("Failed to insert phc2sys metrics", zap.Error(err), zap.String("node", node))
	}
}

func (a *App) handleSystem(ctx context.Context, node string, timestamp time.Time, m *pb.SystemMetrics) {
	if m == nil {
		return
	}

	nodeID, err := a.resolveOrInsertNode(ctx, node)
	if err != nil {
		a.logger.Error("Failed to resolve node", zap.Error(err), zap.String("node", node))
		return
	}

	a.storage.TouchNode(nodeID)

	if net := m.NetworkStats; net != nil {
		if err := a.storage.InsertNetwork(ctx, timestamp, nodeID,
			int64(net.RxPackets), int64(net.TxPackets),
			int64(net.RxDropped), int64(net.TxDropped),
			int64(net.RxErrors), int64(net.TxErrors),
			int64(net.Collisions),
		); err != nil {
			a.logger.Error("Failed to insert network metrics", zap.Error(err), zap.String("node", node))
		}
	}

	if cpu := m.CpuStats; cpu != nil {
		if err := a.storage.InsertCpu(ctx, timestamp, nodeID,
			cpu.UsagePercent,
			int64(cpu.ContextSwitches), int64(cpu.Interrupts), int64(cpu.Softirqs),
		); err != nil {
			a.logger.Error("Failed to insert cpu metrics", zap.Error(err), zap.String("node", node))
		}
	}

	if mem := m.MemoryStats; mem != nil {
		if err := a.storage.InsertMemory(ctx, timestamp, nodeID,
			float64(mem.MemAvailableKb),
			int64(mem.MemFreeKb), int64(mem.SwapTotalKb), int64(mem.SwapFreeKb), int64(mem.BuffersKb),
		); err != nil {
			a.logger.Error("Failed to insert memory metrics", zap.Error(err), zap.String("node", node))
		}
	}

	if temp := m.TemperatureStats; temp != nil {
		for _, reading := range temp.ZonesReadings {
			sensorID, err := a.storage.ResolveSensorID(ctx, nodeID, reading.Sensor, reading.Label)
			if err != nil {
				a.logger.Error("Failed to resolve sensor", zap.Error(err),
					zap.String("node", node),
					zap.String("sensor", reading.Sensor),
					zap.String("label", reading.Label))
				continue
			}
			if err := a.storage.InsertTemperature(ctx, timestamp, sensorID, reading.Temperature); err != nil {
				a.logger.Error("Failed to insert temperature metrics", zap.Error(err),
					zap.String("node", node),
					zap.Int32("sensor_id", sensorID))
			}
		}
	}
}

func (a *App) RunWatchdog(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.NodeTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.storage.DeactivateStaleNodes(ctx, a.cfg.NodeTimeout); err != nil {
				a.logger.Error("Failed to deactivate stale nodes", zap.Error(err))
			}
		}
	}
}
