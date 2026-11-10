package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

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
	configPath := flag.String("config", "config/config.yaml", "path to analyzer config")
	envPath := flag.String("env", "", "path to .env file (defaults to <config-dir>/.env)")
	flag.Parse()

	resolvedEnv := *envPath
	if resolvedEnv == "" {
		resolvedEnv = filepath.Join(filepath.Dir(*configPath), ".env")
	}
	if err := godotenv.Load(resolvedEnv); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load env file %q: %v\n", resolvedEnv, err)
	}

	cfg := config.MustLoad(*configPath)
	logger := buildLogger(cfg.Env)
	defer logger.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil {
		logger.Fatal("analyzer failed", zap.Error(err))
	}
}

func run(ctx context.Context, cfg config.Config, logger *zap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	store, err := storage.NewBatchPostgresStorage(ctx, cfg.DB, logger)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	defer store.Close()

	application, err := app.NewApp(cfg, store, logger)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	var wg sync.WaitGroup
	var batchErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		batchErr = store.Run(ctx)
		cancel()
	}()
	go func() {
		defer wg.Done()
		application.RunWatchdog(ctx)
	}()

	logger.Info("analyzer started", zap.String("address", cfg.Zmq.Address))
	appErr := application.Run(ctx)
	cancel()
	logger.Info("shutting down analyzer")
	wg.Wait()

	// Workers may enqueue rows after the batcher's cancellation flush.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	flushErr := store.FlushAll(shutdownCtx)
	if errors.Is(appErr, context.Canceled) {
		appErr = nil
	}
	return errors.Join(appErr, batchErr, flushErr)
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
