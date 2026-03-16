package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"timesync-analyzer/src/internal/app"
	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/storage"

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.NewPostgresStorage(ctx, cfg.DB, logger)
	if err != nil {
		logger.Fatal("can't create storage", zap.Error(err))
	}
	defer store.Close()

	application, err := app.NewApp(cfg, store, logger)
	if err != nil {
		logger.Fatal("can't create app", zap.Error(err))
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logger.Debug(cfg.Zmq.Address)

	go func() {
		logger.Info("App started")
		if err := application.Run(ctx); err != nil {
			logger.Error("Application error", zap.Error(err))
		}
	}()

	go application.RunWatchdog(ctx)

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
