package app

import (
	"context"
	"fmt"
	"timesync-analyzer/internal/adapter"
	"timesync-analyzer/internal/config"

	pb "timesync-analyzer/protocol/generated"

	"github.com/pebbe/zmq4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type App struct {
	adapter adapter.Adapter
	logger *zap.Logger
}

func NewApp(config config.Config, logger *zap.Logger) (*App, error) {
	server, err := adapter.NewServer(config.Zmq, logger)
	if err != nil {
		return nil, fmt.Errorf("can't create zmq server: %w", err)
	}

	return &App{
		adapter: server,
		logger: logger,
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

		a.handleMsg(&wrapper)
	}
}

func (a *App) handleMsg(metrics_wrapper *pb.MetricsWrapper) error {
	switch metrics_wrapper.Type {
	case pb.MessageType_MESSAGE_TYPE_PHC2SYS:
		a.handlePhc2sys(metrics_wrapper.GetPhc2Sys())
	case pb.MessageType_MESSAGE_TYPE_PTP4L:
		a.handlePtp4l(metrics_wrapper.GetPtp4L())
	case pb.MessageType_MESSAGE_TYPE_SYSTEM:
		a.handleSystem(metrics_wrapper.GetSystem())
	default:
		fmt.Println(metrics_wrapper.Type)
		a.logger.Warn("Unknown message type get")
	}
	return nil
}

func (a *App) handlePtp4l(m *pb.Ptp4LMetrics) {
    if m == nil {
        return
    }

    fmt.Printf("📊 PTP4L: Node=%s, Offset=%d ns\n",
        m.Node, m.OffsetNs)
}

func (a *App) handlePhc2sys(m *pb.Phc2SysMetrics) {
    if m == nil {
        return
    }

    fmt.Printf("📊 PHC2SYS: Node=%s, Offset=%d ns\n",
        m.Node, m.OffsetNs)
}

func (a *App) handleSystem(m *pb.SystemMetrics) {
    if m == nil {
        return
    }

    fmt.Printf("📊 SYSTEM: Node=%s, CPU=%.1f%%\n",
        m.Node, m.CpuStats.UsagePercent)
}