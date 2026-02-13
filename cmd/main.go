package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"timesync-analyzer/internal/app"
	"timesync-analyzer/internal/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	envLocal = "local"
	envDev   = "dev"
	envProd  = "prod"
)

func main() {
	pathToConfig := flag.String("config", "config/config.yaml", "path to config")
	flag.Parse()

	cfg := config.MustLoad(*pathToConfig)
	logger := buildLogger(cfg.Env)

	app, err := app.NewApp(cfg, logger)

	if err != nil {
		logger.Fatal("can't create app", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

	sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("App started")
        if err := app.Run(ctx); err != nil {
            logger.Error("Application error", zap.Error(err))
        }
    }()

	<-sigChan
    logger.Info("Shutting down...")
    cancel()

	logger.Info("Stopped")
}

func buildLogger(env string) *zap.Logger {
	var logger *zap.Logger

	switch env {
	case envLocal:
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		logger, _ = config.Build()
	case envDev:
		config := zap.NewProductionConfig()
		config.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
		logger, _ = config.Build()
	case envProd:
		logger, _ = zap.NewProduction()
	}

	return logger
}