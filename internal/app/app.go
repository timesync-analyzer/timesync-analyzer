package app

import (
	"context"
	"fmt"
	"timesync-analyzer/internal/adapter"
	"timesync-analyzer/internal/config"
	"timesync-analyzer/internal/storage"

	pb "timesync-analyzer/protocol/generated"

	"github.com/pebbe/zmq4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type App struct {
	adapter adapter.Adapter
	storage storage.Storage
	logger  *zap.Logger
}

func NewApp(config config.Config, store storage.Storage, logger *zap.Logger) (*App, error) {
	server, err := adapter.NewServer(config.Zmq, logger)
	if err != nil {
		return nil, fmt.Errorf("can't create zmq server: %w", err)
	}

	return &App{
		adapter: server,
		storage: store,
		logger:  logger,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer a.adapter.Close()
	for {
		select {
		case <-ctx.Done():
			a.logger.Info("Shutting down gracefully")
			return ctx.Err()
		default:
		}

		data, err := a.adapter.Read()
		if err != nil {
			if zmq4.AsErrno(err) == zmq4.Errno(zmq4.ETERM) {
				return nil
			}
			continue
		}
		var wrapper pb.MetricsWrapper
		if err := proto.Unmarshal(data, &wrapper); err != nil {
			a.logger.Warn("Failed to unmarshal", zap.Error(err))
			continue
		}

		a.handleMsg(ctx, &wrapper)
	}
}

func (a *App) handleMsg(ctx context.Context, wrapper *pb.MetricsWrapper) {
	switch wrapper.Type {
	case pb.MessageType_MESSAGE_TYPE_PHC2SYS:
		a.handlePhc2sys(ctx, wrapper.GetPhc2Sys())
	case pb.MessageType_MESSAGE_TYPE_PTP4L:
		a.handlePtp4l(ctx, wrapper.GetPtp4L())
	case pb.MessageType_MESSAGE_TYPE_SYSTEM:
		a.handleSystem(ctx, wrapper.GetSystem())
	default:
		a.logger.Warn("Unknown message type", zap.Int32("type", int32(wrapper.Type)))
	}
}

func (a *App) handlePtp4l(ctx context.Context, m *pb.Ptp4LMetrics) {
	if m == nil {
		return
	}

	nodeID, ok := a.storage.ResolveNodeID(m.Node)
	if !ok {
		a.logger.Warn("Unknown node", zap.String("hostname", m.Node))
		return
	}

	ts := m.Timestamp.AsTime()

	if err := a.storage.InsertPtp4l(ctx, ts, nodeID, m.OffsetNs, m.Frequency, m.PathDelay); err != nil {
		a.logger.Error("Failed to insert ptp4l metrics", zap.Error(err), zap.String("node", m.Node))
	}
}

func (a *App) handlePhc2sys(ctx context.Context, m *pb.Phc2SysMetrics) {
	if m == nil {
		return
	}

	nodeID, ok := a.storage.ResolveNodeID(m.Node)
	if !ok {
		a.logger.Warn("Unknown node", zap.String("hostname", m.Node))
		return
	}

	ts := m.Timestamp.AsTime()

	if err := a.storage.InsertPhc2sys(ctx, ts, nodeID, m.OffsetNs, m.Frequency, m.PathDelay); err != nil {
		a.logger.Error("Failed to insert phc2sys metrics", zap.Error(err), zap.String("node", m.Node))
	}
}

func (a *App) handleSystem(ctx context.Context, m *pb.SystemMetrics) {
	if m == nil {
		return
	}

	nodeID, ok := a.storage.ResolveNodeID(m.Node)
	if !ok {
		a.logger.Warn("Unknown node", zap.String("hostname", m.Node))
		return
	}

	ts := m.Timestamp.AsTime()

	if net := m.NetworkStats; net != nil {
		if err := a.storage.InsertNetwork(ctx, ts, nodeID,
			int64(net.RxPackets), int64(net.TxPackets),
			int64(net.RxDropped), int64(net.TxDropped),
			int64(net.RxErrors), int64(net.TxErrors),
			int64(net.Collisions),
		); err != nil {
			a.logger.Error("Failed to insert network metrics", zap.Error(err), zap.String("node", m.Node))
		}
	}

	if cpu := m.CpuStats; cpu != nil {
		if err := a.storage.InsertCpu(ctx, ts, nodeID,
			cpu.UsagePercent,
			int64(cpu.ContextSwitches), int64(cpu.Interrupts), int64(cpu.Softirqs),
		); err != nil {
			a.logger.Error("Failed to insert cpu metrics", zap.Error(err), zap.String("node", m.Node))
		}
	}

	if mem := m.MemoryStats; mem != nil {
		if err := a.storage.InsertMemory(ctx, ts, nodeID,
			float64(mem.MemAvailableKb),
			int64(mem.MemFreeKb), int64(mem.SwapTotalKb), int64(mem.SwapFreeKb), int64(mem.BuffersKb),
		); err != nil {
			a.logger.Error("Failed to insert memory metrics", zap.Error(err), zap.String("node", m.Node))
		}
	}

	if temp := m.TemperatureStats; temp != nil {
		for _, reading := range temp.ZonesReadings {
			sensorID, err := a.storage.ResolveSensorID(ctx, nodeID, reading.Sensor, reading.Label)
			if err != nil {
				a.logger.Error("Failed to resolve sensor", zap.Error(err),
					zap.String("node", m.Node),
					zap.String("sensor", reading.Sensor),
					zap.String("label", reading.Label))
				continue
			}
			if err := a.storage.InsertTemperature(ctx, ts, sensorID, reading.Temperature); err != nil {
				a.logger.Error("Failed to insert temperature metrics", zap.Error(err),
					zap.String("node", m.Node),
					zap.Int32("sensor_id", sensorID))
			}
		}
	}
}
