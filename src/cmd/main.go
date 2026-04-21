package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"timesync-analyzer/src/internal/app"
	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/storage"

	"github.com/joho/godotenv"
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
	envPath := flag.String("env", "", "path to .env file (defaults to <config-dir>/.env)")
	flag.Parse()

	resolvedEnv := *envPath
	if resolvedEnv == "" {
		resolvedEnv = filepath.Join(filepath.Dir(*pathToConfig), ".env")
	}
	if err := godotenv.Load(resolvedEnv); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load env file %q: %v\n", resolvedEnv, err)
	}

	cfg := config.MustLoad(*pathToConfig)
	logger := buildLogger(cfg.Env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.NewBatchPostgresStorage(ctx, cfg.DB, logger)
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

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("Batcher inserter started")
		if err := store.Run(ctx); err != nil {
			logger.Error("Batch inserter error", zap.Error(err))
		}
	}()

	go application.RunWatchdog(ctx)

	<-sigChan
	logger.Info("Shutting down...")
	cancel()
	wg.Wait()

	logger.Info("Stopped")
}

func buildLogger(env string) *zap.Logger {
	switch env {
	case envLocal:
		cfg := zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		return zap.Must(cfg.Build())
	case envDev:
		cfg := zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
		return zap.Must(cfg.Build())
	default:
		return zap.Must(zap.NewProduction())
	}
}
