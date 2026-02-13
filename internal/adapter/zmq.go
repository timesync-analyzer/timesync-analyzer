package adapter

import (
	"fmt"
	"timesync-analyzer/internal/config"

	"github.com/pebbe/zmq4"
	"go.uber.org/zap"
)

type ZMQServer struct {
	ctx      *zmq4.Context
	receiver *zmq4.Socket
	logger   *zap.Logger
}

func NewServer(config config.ZMQConfig, logger *zap.Logger) (*ZMQServer, error) {
	ctx, err := zmq4.NewContext()
    if err != nil {
        return nil, fmt.Errorf("can't create context: %w", err)
    }

	socket, err := zmq4.NewSocket(zmq4.PULL)
    if err != nil {
		ctx.Term()
        return nil, fmt.Errorf("error while creating socket: %w", err)
    }

	socket.SetRcvtimeo(config.Timeout)

    if err := socket.Bind(config.Address); err != nil {
		ctx.Term()
        return nil, fmt.Errorf("error while binding to %s: %w", config.Address, err)
    }

	return &ZMQServer{
		ctx: ctx,
		receiver: socket,
		logger: logger,
	}, nil
}

func (s *ZMQServer) Close(){
	if s.receiver != nil {
		s.receiver.Close()
	}
	if s.ctx != nil {
		s.ctx.Term()
	}
}

func (s *ZMQServer) Read() ([]byte, error) {
	msg, err := s.receiver.RecvBytes(0)
	if err != nil {
		return nil, fmt.Errorf("can't read data from socket: %w", err)
	}
	return msg, nil
}