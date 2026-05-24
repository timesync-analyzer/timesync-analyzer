package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/report"
	"timesync-analyzer/src/internal/reportd"
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

	rc := cfg.Reportd
	defaultGroups, err := reportd.ParseRenderGroupList(rc.Defaults.Groups)
	if err != nil {
		logger.Fatal("invalid default groups", zap.Error(err))
	}
	alertGroups, err := reportd.ParseRenderGroupList(rc.Alerts.Groups)
	if err != nil {
		logger.Fatal("invalid alert groups", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.NewPostgresStorage(ctx, cfg.DB, logger)
	if err != nil {
		logger.Fatal("can't create storage", zap.Error(err))
	}
	defer store.Close()

	opts := reportd.Options{
		OutDir:               rc.OutDir,
		DashboardPath:        rc.Dashboards.Sync,
		NetworkDashboardPath: rc.Dashboards.Network,
		SystemDashboardPath:  rc.Dashboards.System,
		Grafana: report.GrafanaConfig{
			URL:           rc.Grafana.URL,
			User:          firstNonEmpty(rc.Grafana.User, os.Getenv("GF_SECURITY_ADMIN_USER")),
			Password:      firstNonEmpty(rc.Grafana.Password, os.Getenv("GF_SECURITY_ADMIN_PASSWORD")),
			Token:         rc.Grafana.Token,
			Width:         rc.Grafana.Width,
			Height:        rc.Grafana.Height,
			Node:          rc.Grafana.Node,
			RenderWorkers: rc.Grafana.RenderWorkers,
			RenderTimeout: rc.Grafana.RenderTimeout,
		},
		DefaultGroups:      defaultGroups,
		AlertGroups:        alertGroups,
		DefaultPeriod:      rc.Defaults.Period,
		AlertPeriod:        rc.Alerts.Period,
		AlertDelay:         rc.Alerts.Delay,
		AlertCooldown:      rc.Alerts.Cooldown,
		JobTimeout:         rc.JobTimeout,
		ChartsPerPage:      rc.Defaults.ChartsPerPage,
		DefaultSkipCharts:  rc.Defaults.SkipCharts,
		QueueSize:          rc.QueueSize,
		WorkerCount:        rc.WorkerCount,
		Token:              rc.Token,
		ReportTTL:          rc.Cleanup.ReportTTL,
		CleanupInterval:    rc.Cleanup.Interval,
		AutoReportInterval: rc.Auto.Interval,
		AutoReportWindow:   rc.Auto.Window,
	}

	svc := reportd.New(store, opts, logger)
	svc.Start(ctx)

	server := &http.Server{
		Addr:              rc.Listen,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("report service started", zap.String("listen", rc.Listen), zap.String("out_dir", rc.OutDir))
		if opts.Token == "" {
			logger.Warn("REPORT_TOKEN is empty; report API is unauthenticated")
		}
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("HTTP server failed", zap.Error(err))
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("shutting down report service")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP shutdown failed", zap.Error(err))
	}
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
