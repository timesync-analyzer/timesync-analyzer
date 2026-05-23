package report

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"timesync-analyzer/src/internal/storage"
)

func Generate(ctx context.Context, statsLoader storage.ReportStatsLoader, opts Options) (Result, error) {
	if statsLoader == nil {
		return Result{}, fmt.Errorf("report stats loader is nil")
	}
	if !opts.From.Before(opts.To) {
		return Result{}, fmt.Errorf("from must be before to")
	}
	if opts.OutDir == "" {
		opts.OutDir = "reports"
	}
	if opts.DashboardPath == "" {
		opts.DashboardPath = "grafana/dashboards/sync_analysys.json"
	}
	if opts.NetworkDashboardPath == "" {
		opts.NetworkDashboardPath = "grafana/dashboards/network.json"
	}
	if opts.SystemDashboardPath == "" {
		opts.SystemDashboardPath = "grafana/dashboards/system_resources.json"
	}
	if opts.Grafana.URL == "" {
		opts.Grafana.URL = "http://localhost:3000"
	}

	progress("report period: %s", reportPeriodHeader(opts.From, opts.To))
	progress("querying sync statistics")
	stats, err := statsLoader.LoadReportStats(ctx, opts.From, opts.To)
	if err != nil {
		return Result{}, fmt.Errorf("load stats: %w", err)
	}
	progress("loaded %d statistics rows", len(stats))

	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output dir: %w", err)
	}
	reportDir := filepath.Join(opts.OutDir, reportDirName(opts.From, opts.To))
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create report dir: %w", err)
	}
	progress("report output dir: %s", reportDir)

	var charts []pdfChart
	if !opts.SkipCharts {
		var panels []grafanaPanel
		if shouldRenderSyncDashboard(opts.Groups) {
			progress("loading sync dashboard panels from %s", opts.DashboardPath)
			syncPanels, err := loadDashboardPanels(opts.DashboardPath, "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: sync dashboard panels were not loaded: %v\n", err)
			} else {
				progress("loaded %d sync dashboard panels", len(syncPanels))
				syncPanels = filterPanelsByGroups(syncPanels, opts.Groups)
				progress("selected %d sync dashboard panels for rendering", len(syncPanels))
				panels = append(panels, syncPanels...)
			}
		}

		if opts.Groups.All || opts.Groups.Network {
			panels = appendDashboardPanels(panels, opts.NetworkDashboardPath, "Network")
		}
		if opts.Groups.All || opts.Groups.System {
			panels = appendDashboardPanels(panels, opts.SystemDashboardPath, "System")
		}

		progress("selected %d dashboard panels for rendering", len(panels))
		if len(panels) == 0 {
			fmt.Fprintln(os.Stderr, "warning: no dashboard panels matched selected render groups")
		}
		progress("rendering Grafana dashboard panels from %s", opts.Grafana.URL)
		charts, err = renderGrafanaCharts(ctx, opts.Grafana, panels, opts.From, opts.To)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: Grafana charts were not embedded: %v\n", err)
		}
	}

	csvPath := filepath.Join(reportDir, "summary.csv")
	progress("writing CSV to %s", csvPath)
	if err := writeCSV(csvPath, opts.From, opts.To, stats); err != nil {
		return Result{}, fmt.Errorf("write csv: %w", err)
	}

	pdfPath := filepath.Join(reportDir, "summary.pdf")
	progress("writing PDF to %s", pdfPath)
	if err := opts.PDFRenderer.WritePDF(pdfPath, opts.From, opts.To, stats, charts, opts.ChartsPerPage); err != nil {
		return Result{}, fmt.Errorf("write pdf: %w", err)
	}
	progress("done")

	return Result{
		ReportDir: reportDir,
		CSVPath:   csvPath,
		PDFPath:   pdfPath,
		Rows:      len(stats),
		Charts:    len(charts),
	}, nil
}
