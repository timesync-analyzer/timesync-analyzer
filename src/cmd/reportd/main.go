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
	"strings"
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
	listenAddr := flag.String("listen", ":8080", "HTTP listen address")
	outDir := flag.String("out", "reports", "output directory")
	dashboardPath := flag.String("dashboard", "grafana/dashboards/sync_analysys.json", "Grafana sync dashboard JSON to render")
	networkDashboardPath := flag.String("network-dashboard", "grafana/dashboards/network.json", "Grafana network dashboard JSON to render")
	systemDashboardPath := flag.String("system-dashboard", "grafana/dashboards/system_resources.json", "Grafana system resources dashboard JSON to render")
	defaultGroupsValue := flag.String("default-groups", "", "comma-separated render groups for manual/cron jobs")
	alertGroupsValue := flag.String("alert-groups", "", "comma-separated render groups for Grafana alert jobs")
	defaultPeriod := flag.Duration("default-period", time.Hour, "default report period")
	alertPeriod := flag.Duration("alert-period", time.Hour, "report period for Grafana alert webhooks")
	alertCooldown := flag.Duration("alert-cooldown", 30*time.Minute, "minimum interval between reports for the same Grafana alert fingerprint")
	jobTimeout := flag.Duration("job-timeout", 15*time.Minute, "timeout for a single report job")
	queueSize := flag.Int("queue-size", 100, "maximum queued report jobs")
	workerCount := flag.Int("workers", 1, "number of concurrent report workers")
	chartsPerPage := flag.Int("charts-per-page", 3, "number of rendered Grafana panels per PDF page")
	defaultSkipCharts := flag.Bool("skip-charts", false, "skip Grafana chart rendering by default")
	tokenFlag := flag.String("token", "", "bearer token for API requests")
	grafanaURLFlag := flag.String("grafana-url", "", "Grafana base URL for rendered charts")
	grafanaUserFlag := flag.String("grafana-user", "", "Grafana basic auth user")
	grafanaPasswordFlag := flag.String("grafana-password", "", "Grafana basic auth password")
	grafanaTokenFlag := flag.String("grafana-token", "", "Grafana service account token")
	grafanaNodeFlag := flag.String("grafana-node", "", "default Grafana node variable value")
	renderWidth := flag.Int("render-width", 1200, "Grafana rendered chart width")
	renderHeight := flag.Int("render-height", 420, "Grafana rendered chart height")
	renderWorkers := flag.Int("render-workers", 4, "number of parallel Grafana panel renders per job")
	renderTimeout := flag.Duration("render-timeout", 2*time.Minute, "timeout for each Grafana panel render")
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

	defaultGroups, err := reportd.ParseRenderGroups(firstNonEmpty(*defaultGroupsValue, os.Getenv("REPORT_DEFAULT_GROUPS"), "offset,status"))
	if err != nil {
		logger.Fatal("invalid default groups", zap.Error(err))
	}
	alertGroups, err := reportd.ParseRenderGroups(firstNonEmpty(*alertGroupsValue, os.Getenv("REPORT_ALERT_GROUPS"), "offset,status"))
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
		OutDir:               *outDir,
		DashboardPath:        *dashboardPath,
		NetworkDashboardPath: *networkDashboardPath,
		SystemDashboardPath:  *systemDashboardPath,
		Grafana: report.GrafanaConfig{
			URL:           firstNonEmpty(*grafanaURLFlag, os.Getenv("GRAFANA_URL"), "http://localhost:3000"),
			User:          firstNonEmpty(*grafanaUserFlag, os.Getenv("GRAFANA_ADMIN_USER"), os.Getenv("GF_SECURITY_ADMIN_USER")),
			Password:      firstNonEmpty(*grafanaPasswordFlag, os.Getenv("GRAFANA_ADMIN_PASSWORD"), os.Getenv("GF_SECURITY_ADMIN_PASSWORD")),
			Token:         firstNonEmpty(*grafanaTokenFlag, os.Getenv("GRAFANA_TOKEN")),
			Width:         *renderWidth,
			Height:        *renderHeight,
			Node:          firstNonEmpty(*grafanaNodeFlag, os.Getenv("REPORT_GRAFANA_NODE"), ".*"),
			RenderWorkers: *renderWorkers,
			RenderTimeout: *renderTimeout,
		},
		DefaultGroups:     defaultGroups,
		AlertGroups:       alertGroups,
		DefaultPeriod:     *defaultPeriod,
		AlertPeriod:       *alertPeriod,
		AlertCooldown:     *alertCooldown,
		JobTimeout:        *jobTimeout,
		ChartsPerPage:     *chartsPerPage,
		DefaultSkipCharts: *defaultSkipCharts,
		QueueSize:         *queueSize,
		WorkerCount:       *workerCount,
		Token:             firstNonEmpty(*tokenFlag, os.Getenv("REPORT_TOKEN")),
	}

	svc := reportd.New(store, opts, logger)
	svc.Start(ctx)

	server := &http.Server{
		Addr:              *listenAddr,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("report service started", zap.String("listen", *listenAddr), zap.String("out_dir", *outDir))
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
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
