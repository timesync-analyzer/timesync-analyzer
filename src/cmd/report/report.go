package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/report"
	"timesync-analyzer/src/internal/storage"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to analyzer config")
	envPath := flag.String("env", "", "path to .env file (defaults to <config-dir>/.env)")
	dashboardPath := flag.String("dashboard", "grafana/dashboards/sync_analysys.json", "Grafana dashboard JSON to render")
	networkDashboardPath := flag.String("network-dashboard", "grafana/dashboards/network.json", "Grafana network dashboard JSON to render")
	systemDashboardPath := flag.String("system-dashboard", "grafana/dashboards/system_resources.json", "Grafana system resources dashboard JSON to render")
	fromValue := flag.String("from", "", "period start, RFC3339 or YYYY-MM-DD HH:MM:SS")
	toValue := flag.String("to", "", "period end, RFC3339 or YYYY-MM-DD HH:MM:SS")
	period := flag.Duration("period", 24*time.Hour, "period length when --from is not set")
	outDir := flag.String("out", "reports", "output directory")
	grafanaURLFlag := flag.String("grafana-url", "", "Grafana base URL for rendered charts")
	grafanaUserFlag := flag.String("grafana-user", "", "Grafana basic auth user")
	grafanaPasswordFlag := flag.String("grafana-password", "", "Grafana basic auth password")
	grafanaTokenFlag := flag.String("grafana-token", "", "Grafana service account token")
	grafanaNodeFlag := flag.String("grafana-node", ".*", "Grafana node variable value")
	renderWidth := flag.Int("render-width", 1200, "Grafana rendered chart width")
	renderHeight := flag.Int("render-height", 420, "Grafana rendered chart height")
	renderWorkers := flag.Int("render-workers", 4, "number of parallel Grafana panel renders")
	renderTimeout := flag.Duration("render-timeout", 2*time.Minute, "timeout for each Grafana panel render")
	chartsPerPage := flag.Int("charts-per-page", 3, "number of rendered Grafana panels per PDF page")
	skipCharts := flag.Bool("skip-charts", false, "skip Grafana chart rendering")
	renderAll := flag.Bool("all", false, "render all sync, network, and system dashboard panels")
	renderFreq := flag.Bool("freq", false, "render frequency panels")
	renderOffset := flag.Bool("offset", false, "render offset panels")
	renderPathDelay := flag.Bool("path_delay", false, "render path delay panels")
	renderStatus := flag.Bool("status", false, "render status/topology/timeline/table panels")
	renderNetwork := flag.Bool("network", false, "render panels from the network dashboard")
	renderSystem := flag.Bool("system", false, "render panels from the system resources dashboard")
	flag.Parse()

	resolvedEnv := *envPath
	if resolvedEnv == "" {
		resolvedEnv = filepath.Join(filepath.Dir(*configPath), ".env")
	}
	if err := godotenv.Load(resolvedEnv); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load env file %q: %v\n", resolvedEnv, err)
	}
	progress("loaded env from %s", resolvedEnv)

	to, err := report.ParseTimeOrDefault(*toValue, time.Now().UTC())
	if err != nil {
		fail("parse --to: %v", err)
	}

	from, err := report.ParseTimeOrDefault(*fromValue, to.Add(-*period))
	if err != nil {
		fail("parse --from: %v", err)
	}
	if !from.Before(to) {
		fail("--from must be before --to")
	}

	progress("loading config from %s", *configPath)
	cfg := config.MustLoad(*configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	progress("connecting to database %s:%d/%s", cfg.DB.Host, cfg.DB.Port, cfg.DB.DBName)
	store, err := storage.NewPostgresStorage(ctx, cfg.DB, zap.NewNop())
	if err != nil {
		fail("connect database: %v", err)
	}
	defer store.Close()

	pdfRenderer := report.MarotoPDFRenderer{}
	if err != nil {
		fail("%v", err)
	}

	result, err := report.Generate(ctx, store, report.Options{
		From:                 from,
		To:                   to,
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
			Node:          *grafanaNodeFlag,
			RenderWorkers: *renderWorkers,
			RenderTimeout: *renderTimeout,
		},
		Groups: report.RenderGroups{
			All:       *renderAll,
			Frequency: *renderFreq,
			Offset:    *renderOffset,
			PathDelay: *renderPathDelay,
			Status:    *renderStatus,
			Network:   *renderNetwork,
			System:    *renderSystem,
		},
		SkipCharts:    *skipCharts,
		ChartsPerPage: *chartsPerPage,
		PDFRenderer:   pdfRenderer,
	})
	if err != nil {
		fail("%v", err)
	}

	fmt.Printf(
		"Report: %s\nCSV: %s\nPDF: %s\nRows: %d\nCharts: %d\n",
		result.ReportDir,
		result.CSVPath,
		result.PDFPath,
		result.Rows,
		result.Charts,
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[report] "+format+"\n", args...)
}
