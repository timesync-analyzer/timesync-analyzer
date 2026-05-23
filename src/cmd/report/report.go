package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"timesync-analyzer/src/internal/config"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type rowStats struct {
	Hostname string
	Protocol string
	Metric   string
	Unit     string
	Samples  int64
	Min      float64
	Max      float64
	Mean     float64
	RMS      float64
	HasData  bool
}

type grafanaConfig struct {
	URL           string
	User          string
	Password      string
	Token         string
	Width         int
	Height        int
	Node          string
	RenderWorkers int
	RenderTimeout time.Duration
}

type grafanaPanel struct {
	Title   string
	Type    string
	UID     string
	Slug    string
	PanelID int
}

type pdfChart struct {
	Title  string
	Width  int
	Height int
	RGB    []byte
}

type renderJob struct {
	Index int
	Panel grafanaPanel
}

type renderResult struct {
	Index   int
	Chart   pdfChart
	Failure string
}

type dashboardFile struct {
	UID    string               `json:"uid"`
	Title  string               `json:"title"`
	Panels []dashboardPanelJSON `json:"panels"`
}

type dashboardPanelJSON struct {
	ID     int                  `json:"id"`
	Title  string               `json:"title"`
	Type   string               `json:"type"`
	Panels []dashboardPanelJSON `json:"panels"`
}

type renderGroups struct {
	All       bool
	Frequency bool
	Offset    bool
	PathDelay bool
	Status    bool
	Network   bool
	System    bool
}

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

	to, err := parseTimeOrDefault(*toValue, time.Now().UTC())
	if err != nil {
		fail("parse --to: %v", err)
	}

	from, err := parseTimeOrDefault(*fromValue, to.Add(-*period))
	if err != nil {
		fail("parse --from: %v", err)
	}
	if !from.Before(to) {
		fail("--from must be before --to")
	}
	progress("report period: %s .. %s", from.Format(time.RFC3339), to.Format(time.RFC3339))

	progress("loading config from %s", *configPath)
	cfg := config.MustLoad(*configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	progress("connecting to database %s:%d/%s", cfg.DB.Host, cfg.DB.Port, cfg.DB.DBName)
	pool, err := pgxpool.New(ctx, cfg.DB.ConnString())
	if err != nil {
		fail("connect database: %v", err)
	}
	defer pool.Close()

	progress("querying sync statistics")
	stats, err := loadStats(ctx, pool, from, to)
	if err != nil {
		fail("load stats: %v", err)
	}
	progress("loaded %d statistics rows", len(stats))

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("create output dir: %v", err)
	}
	reportDir := filepath.Join(*outDir, reportDirName(from, to))
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		fail("create report dir: %v", err)
	}
	progress("report output dir: %s", reportDir)

	var charts []pdfChart
	if !*skipCharts {
		grafanaCfg := grafanaConfig{
			URL:           firstNonEmpty(*grafanaURLFlag, os.Getenv("GRAFANA_URL"), "http://localhost:3000"),
			User:          firstNonEmpty(*grafanaUserFlag, os.Getenv("GRAFANA_ADMIN_USER"), os.Getenv("GF_SECURITY_ADMIN_USER")),
			Password:      firstNonEmpty(*grafanaPasswordFlag, os.Getenv("GRAFANA_ADMIN_PASSWORD"), os.Getenv("GF_SECURITY_ADMIN_PASSWORD")),
			Token:         firstNonEmpty(*grafanaTokenFlag, os.Getenv("GRAFANA_TOKEN")),
			Width:         *renderWidth,
			Height:        *renderHeight,
			Node:          *grafanaNodeFlag,
			RenderWorkers: *renderWorkers,
			RenderTimeout: *renderTimeout,
		}

		groups := renderGroups{
			All:       *renderAll,
			Frequency: *renderFreq,
			Offset:    *renderOffset,
			PathDelay: *renderPathDelay,
			Status:    *renderStatus,
			Network:   *renderNetwork,
			System:    *renderSystem,
		}

		var panels []grafanaPanel
		if shouldRenderSyncDashboard(groups) {
			progress("loading sync dashboard panels from %s", *dashboardPath)
			syncPanels, err := loadDashboardPanels(*dashboardPath, "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: sync dashboard panels were not loaded: %v\n", err)
			} else {
				progress("loaded %d sync dashboard panels", len(syncPanels))
				syncPanels = filterPanelsByGroups(syncPanels, groups)
				progress("selected %d sync dashboard panels for rendering", len(syncPanels))
				panels = append(panels, syncPanels...)
			}
		}

		if groups.All || groups.Network {
			panels = appendDashboardPanels(panels, *networkDashboardPath, "Network")
		}
		if groups.All || groups.System {
			panels = appendDashboardPanels(panels, *systemDashboardPath, "System")
		}

		progress("selected %d dashboard panels for rendering", len(panels))
		if len(panels) == 0 {
			fmt.Fprintln(os.Stderr, "warning: no dashboard panels matched selected render groups")
		}
		progress("rendering Grafana dashboard panels from %s", grafanaCfg.URL)
		charts, err = renderGrafanaCharts(ctx, grafanaCfg, panels, from, to)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: Grafana charts were not embedded: %v\n", err)
		}
	}

	csvPath := filepath.Join(reportDir, "summary.csv")
	progress("writing CSV to %s", csvPath)
	if err := writeCSV(csvPath, from, to, stats); err != nil {
		fail("write csv: %v", err)
	}

	pdfPath := filepath.Join(reportDir, "summary.pdf")
	progress("writing PDF to %s", pdfPath)
	if err := writePDFReport(pdfPath, from, to, stats, charts, *chartsPerPage); err != nil {
		fail("write pdf: %v", err)
	}
	progress("done")

	fmt.Printf("Report: %s\nCSV: %s\nPDF: %s\nRows: %d\nCharts: %d\n", reportDir, csvPath, pdfPath, len(stats), len(charts))
}

func loadStats(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) ([]rowStats, error) {
	const query = `
WITH expected(metric, unit, protocol, metric_order, protocol_order) AS (
	VALUES
		('offset'::text, 'ns'::text, 'ptp4l'::text, 1, 1),
		('offset'::text, 'ns'::text, 'phc2sys'::text, 1, 2),
		('offset'::text, 'ns'::text, 'pps'::text, 1, 3),
		('frequency'::text, ''::text, 'ptp4l'::text, 2, 1),
		('frequency'::text, ''::text, 'phc2sys'::text, 2, 2),
		('path_delay'::text, 'ns'::text, 'ptp4l'::text, 3, 1),
		('path_delay'::text, 'ns'::text, 'phc2sys'::text, 3, 2)
),
samples AS (
	SELECT 'offset'::text AS metric, 'ptp4l'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'offset'::text AS metric, 'phc2sys'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'offset'::text AS metric, 'pps'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.pps_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'frequency'::text AS metric, 'ptp4l'::text AS protocol, node_id, frequency::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND frequency IS NOT NULL

	UNION ALL

	SELECT 'frequency'::text AS metric, 'phc2sys'::text AS protocol, node_id, frequency::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND frequency IS NOT NULL

	UNION ALL

	SELECT 'path_delay'::text AS metric, 'ptp4l'::text AS protocol, node_id, path_delay::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND path_delay IS NOT NULL

	UNION ALL

	SELECT 'path_delay'::text AS metric, 'phc2sys'::text AS protocol, node_id, path_delay::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND path_delay IS NOT NULL
),
agg AS (
	SELECT
		metric,
		protocol,
		node_id,
		count(*)::bigint AS samples,
		min(value) AS min_value,
		max(value) AS max_value,
		avg(value) AS mean_value,
		sqrt(avg(value * value)) AS rms_value
	FROM samples
	GROUP BY metric, protocol, node_id
)
SELECT
	n.hostname,
	e.protocol,
	e.metric,
	e.unit,
	COALESCE(a.samples, 0) AS samples,
	a.min_value,
	a.max_value,
	a.mean_value,
	a.rms_value
FROM timesync.nodes n
CROSS JOIN expected e
LEFT JOIN agg a ON a.node_id = n.node_id AND a.metric = e.metric AND a.protocol = e.protocol
ORDER BY e.metric_order, e.protocol_order, lower(n.hostname), n.hostname`

	rows, err := pool.Query(ctx, query, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []rowStats
	for rows.Next() {
		var item rowStats
		var minValue, maxValue, meanValue, rmsValue pgtype.Float8
		if err := rows.Scan(&item.Hostname, &item.Protocol, &item.Metric, &item.Unit, &item.Samples, &minValue, &maxValue, &meanValue, &rmsValue); err != nil {
			return nil, err
		}
		item.HasData = item.Samples > 0
		if minValue.Valid {
			item.Min = minValue.Float64
		}
		if maxValue.Valid {
			item.Max = maxValue.Float64
		}
		if meanValue.Valid {
			item.Mean = meanValue.Float64
		}
		if rmsValue.Valid {
			item.RMS = rmsValue.Float64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func loadDashboardPanels(path, titlePrefix string) ([]grafanaPanel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var dashboard dashboardFile
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return nil, err
	}
	if dashboard.UID == "" {
		return nil, fmt.Errorf("dashboard uid is empty")
	}

	slug := safeName(firstNonEmpty(dashboard.Title, dashboard.UID))
	if slug == "" {
		slug = dashboard.UID
	}

	var panels []grafanaPanel
	var collect func([]dashboardPanelJSON)
	collect = func(items []dashboardPanelJSON) {
		for _, item := range items {
			if item.ID > 0 && item.Title != "" && item.Type != "row" {
				title := item.Title
				if titlePrefix != "" {
					title = titlePrefix + " / " + title
				}
				panels = append(panels, grafanaPanel{
					Title:   title,
					Type:    item.Type,
					UID:     dashboard.UID,
					Slug:    slug,
					PanelID: item.ID,
				})
			}
			if len(item.Panels) > 0 {
				collect(item.Panels)
			}
		}
	}
	collect(dashboard.Panels)

	if len(panels) == 0 {
		return nil, fmt.Errorf("dashboard contains no renderable panels")
	}
	return panels, nil
}

func appendDashboardPanels(panels []grafanaPanel, path, titlePrefix string) []grafanaPanel {
	label := strings.ToLower(titlePrefix)
	progress("loading %s dashboard panels from %s", label, path)
	loaded, err := loadDashboardPanels(path, titlePrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s dashboard panels were not loaded: %v\n", label, err)
		return panels
	}
	progress("loaded %d %s dashboard panels", len(loaded), label)
	return append(panels, loaded...)
}

func shouldRenderSyncDashboard(groups renderGroups) bool {
	if hasSyncRenderGroup(groups) {
		return true
	}
	return !groups.Network && !groups.System
}

func hasSyncRenderGroup(groups renderGroups) bool {
	return groups.All || groups.Frequency || groups.Offset || groups.PathDelay || groups.Status
}

func filterPanelsByGroups(panels []grafanaPanel, groups renderGroups) []grafanaPanel {
	if !hasSyncRenderGroup(groups) {
		groups.All = true
	}
	if groups.All {
		return panels
	}

	selected := make([]grafanaPanel, 0, len(panels))
	for _, panel := range panels {
		if panelMatchesGroups(panel, groups) {
			selected = append(selected, panel)
		}
	}
	return selected
}

func panelMatchesGroups(panel grafanaPanel, groups renderGroups) bool {
	title := strings.ToLower(panel.Title)
	panelType := strings.ToLower(panel.Type)

	if groups.Frequency && strings.Contains(title, "frequency") {
		return true
	}
	if groups.Offset && strings.Contains(title, "offset") {
		return true
	}
	if groups.PathDelay && strings.Contains(title, "path delay") {
		return true
	}
	if groups.Status {
		if panelType == "nodegraph" || panelType == "state-timeline" || panelType == "table" {
			return true
		}
		if strings.Contains(title, "topology") ||
			strings.Contains(title, "timeline") ||
			strings.Contains(title, "node info") ||
			strings.Contains(title, "status") {
			return true
		}
	}
	return false
}

func renderGrafanaCharts(ctx context.Context, cfg grafanaConfig, panels []grafanaPanel, from, to time.Time) ([]pdfChart, error) {
	if len(panels) == 0 {
		return nil, nil
	}
	if cfg.Width <= 0 {
		cfg.Width = 1200
	}
	if cfg.Height <= 0 {
		cfg.Height = 420
	}
	if cfg.RenderWorkers <= 0 {
		cfg.RenderWorkers = 1
	}
	if cfg.RenderWorkers > len(panels) {
		cfg.RenderWorkers = len(panels)
	}
	if cfg.RenderTimeout <= 0 {
		cfg.RenderTimeout = 2 * time.Minute
	}
	if strings.TrimSpace(cfg.Node) == "" {
		cfg.Node = ".*"
	}

	progress("render workers: %d, panel timeout: %s", cfg.RenderWorkers, cfg.RenderTimeout)

	client := &http.Client{Timeout: cfg.RenderTimeout + 15*time.Second}
	jobs := make(chan renderJob)
	results := make(chan renderResult, len(panels))

	var wg sync.WaitGroup
	for workerID := 1; workerID <= cfg.RenderWorkers; workerID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobs {
				panel := job.Panel
				progress("rendering panel %d/%d on worker %d: %s", job.Index+1, len(panels), workerID, panel.Title)

				pngData, err := fetchGrafanaPanel(ctx, client, cfg, panel, from, to)
				if err != nil {
					failure := fmt.Sprintf("%s: %v", panel.Title, err)
					progress("render failed panel %d/%d: %s", job.Index+1, len(panels), panel.Title)
					results <- renderResult{Index: job.Index, Failure: failure}
					continue
				}

				chart, err := decodePNGChart(panel.Title, pngData)
				if err != nil {
					results <- renderResult{Index: job.Index, Failure: fmt.Sprintf("%s: decode png: %v", panel.Title, err)}
					continue
				}

				progress("rendered panel %d/%d: %s", job.Index+1, len(panels), panel.Title)
				results <- renderResult{Index: job.Index, Chart: chart}
			}
		}(workerID)
	}

	go func() {
		for i, panel := range panels {
			jobs <- renderJob{Index: i, Panel: panel}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	orderedResults := make([]renderResult, len(panels))
	for result := range results {
		orderedResults[result.Index] = result
	}

	var charts []pdfChart
	var failed []string
	for _, result := range orderedResults {
		if result.Failure != "" {
			failed = append(failed, result.Failure)
			continue
		}
		if len(result.Chart.RGB) > 0 {
			charts = append(charts, result.Chart)
		}
	}

	for _, failure := range failed {
		fmt.Fprintf(os.Stderr, "warning: render chart failed: %s\n", failure)
	}
	if len(charts) == 0 && len(failed) > 0 {
		return nil, fmt.Errorf("all Grafana chart renders failed")
	}
	return charts, nil
}

func fetchGrafanaPanel(ctx context.Context, client *http.Client, cfg grafanaConfig, panel grafanaPanel, from, to time.Time) ([]byte, error) {
	renderURL := grafanaRenderURL(cfg, panel, from, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, renderURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/png")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	} else if cfg.User != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.User, cfg.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d content-type=%q", resp.StatusCode, contentType)
	}

	if !strings.HasPrefix(contentType, "image/png") {
		return nil, fmt.Errorf("unexpected content-type=%q", contentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func grafanaRenderURL(cfg grafanaConfig, panel grafanaPanel, from, to time.Time) string {
	base := strings.TrimRight(cfg.URL, "/")
	q := url.Values{}
	q.Set("orgId", "1")
	q.Set("panelId", strconv.Itoa(panel.PanelID))
	q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
	q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
	q.Set("var-node", grafanaNodeValue(cfg.Node))
	q.Set("var-ptp4l_node_type", "$__all")
	q.Set("var-phc2sys_node_type", "$__all")
	q.Set("var-pps_node_type", "$__all")
	q.Set("var-ptp4l_has_data", "1")
	q.Set("var-phc2sys_has_data", "1")
	q.Set("var-pps_has_data", "1")
	q.Set("width", strconv.Itoa(cfg.Width))
	q.Set("height", strconv.Itoa(cfg.Height))
	q.Set("tz", "UTC")
	q.Set("timeout", strconv.Itoa(renderTimeoutSeconds(cfg.RenderTimeout)))

	return fmt.Sprintf(
		"%s/render/d-solo/%s/%s?%s",
		base,
		url.PathEscape(panel.UID),
		url.PathEscape(panel.Slug),
		q.Encode(),
	)
}

func grafanaNodeValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == ".*" {
		return "$__all"
	}
	return value
}

func renderTimeoutSeconds(timeout time.Duration) int {
	seconds := int(math.Ceil(timeout.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func decodePNGChart(title string, pngData []byte) (pdfChart, error) {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return pdfChart{}, err
	}
	width, height, rgb := imageToRGB(img)
	return pdfChart{
		Title:  title,
		Width:  width,
		Height: height,
		RGB:    rgb,
	}, nil
}

func imageToRGB(img image.Image) (int, int, []byte) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)

	rgb := make([]byte, 0, width*height*3)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pix := dst.Pix[dst.PixOffset(x, y):]
			alpha := uint32(pix[3])
			if alpha == 255 {
				rgb = append(rgb, pix[0], pix[1], pix[2])
				continue
			}
			r := byte((uint32(pix[0])*alpha + 255*(255-alpha)) / 255)
			g := byte((uint32(pix[1])*alpha + 255*(255-alpha)) / 255)
			b := byte((uint32(pix[2])*alpha + 255*(255-alpha)) / 255)
			rgb = append(rgb, r, g, b)
		}
	}
	return width, height, rgb
}

func writeCSV(path string, from, to time.Time, stats []rowStats) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if err := writer.Write([]string{
		"period_from",
		"period_to",
		"hostname",
		"protocol",
		"metric",
		"unit",
		"samples",
		"min",
		"max",
		"mean",
		"rms",
	}); err != nil {
		return err
	}

	for _, item := range stats {
		if err := writer.Write([]string{
			from.Format(time.RFC3339),
			to.Format(time.RFC3339),
			item.Hostname,
			item.Protocol,
			item.Metric,
			item.Unit,
			fmt.Sprintf("%d", item.Samples),
			formatMetric(item, item.Min),
			formatMetric(item, item.Max),
			formatMetric(item, item.Mean),
			formatMetric(item, item.RMS),
		}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writePDFReport(path string, from, to time.Time, stats []rowStats, charts []pdfChart, chartsPerPage int) error {
	lines := []string{
		"Time Sync Summary",
		fmt.Sprintf("Period: %s .. %s", from.Format(time.RFC3339), to.Format(time.RFC3339)),
		fmt.Sprintf("Generated: %s", time.Now().UTC().Format(time.RFC3339)),
	}

	for _, section := range []struct {
		metric string
		title  string
	}{
		{metric: "offset", title: "Offset Statistics (ns)"},
		{metric: "frequency", title: "Frequency Statistics"},
		{metric: "path_delay", title: "Path Delay Statistics (ns)"},
	} {
		lines = append(lines,
			"",
			section.title,
			fmt.Sprintf("%-22s %-8s %8s %14s %14s %14s %14s", "host", "proto", "samples", "min", "max", "mean", "rms"),
			strings.Repeat("-", 104),
		)

		for _, item := range stats {
			if item.Metric != section.metric {
				continue
			}
			lines = append(lines, fmt.Sprintf(
				"%-22s %-8s %8d %14s %14s %14s %14s",
				truncate(item.Hostname, 22),
				item.Protocol,
				item.Samples,
				formatPDFMetric(item, item.Min),
				formatPDFMetric(item, item.Max),
				formatPDFMetric(item, item.Mean),
				formatPDFMetric(item, item.RMS),
			))
		}
	}

	return writeSimplePDF(path, lines, charts, chartsPerPage)
}

func writeSimplePDF(path string, lines []string, charts []pdfChart, chartsPerPage int) error {
	const (
		pageWidth      = 595.0
		pageHeight     = 842.0
		leftMargin     = 36.0
		topMargin      = 36.0
		fontSize       = 8.0
		linesPerPage   = 64
		contentTopY    = pageHeight - topMargin
		contentStreamT = "BT /F1 %.0f Tf %.0f %.0f Td\n%sET\n"
	)

	if len(lines) == 0 {
		lines = []string{""}
	}
	if chartsPerPage <= 0 {
		chartsPerPage = 1
	}
	if chartsPerPage > 6 {
		chartsPerPage = 6
	}

	var objects []string
	addObject := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}

	catalogID := addObject("")
	pagesID := addObject("")
	fontID := addObject("<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")

	var pageIDs []int
	for start := 0; start < len(lines); start += linesPerPage {
		end := start + linesPerPage
		if end > len(lines) {
			end = len(lines)
		}

		var stream bytes.Buffer
		for i, line := range lines[start:end] {
			if i > 0 {
				stream.WriteString("0 -12 Td\n")
			}
			stream.WriteString("(")
			stream.WriteString(escapePDFText(line))
			stream.WriteString(") Tj\n")
		}

		content := fmt.Sprintf(contentStreamT, fontSize, leftMargin, contentTopY, stream.String())
		contentID := addObject(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		pageID := addObject(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesID,
			pageWidth,
			pageHeight,
			fontID,
			contentID,
		))
		pageIDs = append(pageIDs, pageID)
	}

	var validCharts []pdfChart
	for _, chart := range charts {
		if chart.Width <= 0 || chart.Height <= 0 || len(chart.RGB) == 0 {
			continue
		}
		validCharts = append(validCharts, chart)
	}

	imageSeq := 1
	for start := 0; start < len(validCharts); start += chartsPerPage {
		end := start + chartsPerPage
		if end > len(validCharts) {
			end = len(validCharts)
		}
		pageCharts := validCharts[start:end]

		var content strings.Builder
		var xobjects strings.Builder
		slotGap := 14.0
		slotCount := float64(len(pageCharts))
		slotHeight := (pageHeight - 2*topMargin - slotGap*(slotCount-1)) / slotCount
		maxImageWidth := pageWidth - 2*leftMargin
		titleBlockHeight := 18.0

		for slotIndex, chart := range pageCharts {
			compressedRGB := zlibCompress(chart.RGB)
			imageID := addObject(fmt.Sprintf(
				"<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream",
				chart.Width,
				chart.Height,
				len(compressedRGB),
				string(compressedRGB),
			))

			imageName := fmt.Sprintf("Im%d", imageSeq)
			imageSeq++
			fmt.Fprintf(&xobjects, "/%s %d 0 R ", imageName, imageID)

			slotTopY := pageHeight - topMargin - float64(slotIndex)*(slotHeight+slotGap)
			maxImageHeight := slotHeight - titleBlockHeight
			scale := math.Min(maxImageWidth/float64(chart.Width), maxImageHeight/float64(chart.Height))
			displayWidth := float64(chart.Width) * scale
			displayHeight := float64(chart.Height) * scale
			imageX := (pageWidth - displayWidth) / 2
			imageY := slotTopY - titleBlockHeight - displayHeight

			fmt.Fprintf(
				&content,
				"BT /F1 9 Tf %.0f %.2f Td (%s) Tj ET\nq %.2f 0 0 %.2f %.2f %.2f cm /%s Do Q\n",
				leftMargin,
				slotTopY-10,
				escapePDFText(truncate(chart.Title, 80)),
				displayWidth,
				displayHeight,
				imageX,
				imageY,
				imageName,
			)
		}

		contentString := content.String()
		contentID := addObject(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(contentString), contentString))
		pageID := addObject(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 %d 0 R >> /XObject << %s>> >> /Contents %d 0 R >>",
			pagesID,
			pageWidth,
			pageHeight,
			fontID,
			xobjects.String(),
			contentID,
		))
		pageIDs = append(pageIDs, pageID)
	}

	var kids strings.Builder
	for _, pageID := range pageIDs {
		fmt.Fprintf(&kids, "%d 0 R ", pageID)
	}
	objects[catalogID-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID)
	objects[pagesID-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(pageIDs))

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")

	offsets := make([]int, 0, len(objects)+1)
	offsets = append(offsets, 0)
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}

	xrefOffset := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, catalogID, xrefOffset)

	return os.WriteFile(path, out.Bytes(), 0o644)
}

func zlibCompress(data []byte) []byte {
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	_, _ = writer.Write(data)
	_ = writer.Close()
	return buf.Bytes()
}

func parseTimeOrDefault(value string, fallback time.Time) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}

	localLayouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range localLayouts {
		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func reportDirName(from, to time.Time) string {
	return fmt.Sprintf("report_%s-%s", reportFileTime(from), reportFileTime(to))
}

func reportFileTime(value time.Time) string {
	return value.UTC().Format("2006_01_02_15_04_05")
}

func formatMetric(item rowStats, value float64) string {
	if !item.HasData || math.IsNaN(value) || math.IsInf(value, 0) {
		return ""
	}
	return fmt.Sprintf("%.3f", value)
}

func formatPDFMetric(item rowStats, value float64) string {
	if !item.HasData || math.IsNaN(value) || math.IsInf(value, 0) {
		return "-"
	}
	return fmt.Sprintf("%.3f", value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func safeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAllowed {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return value[:limit]
	}
	return value[:limit-1] + "~"
}

func escapePDFText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "(", "\\(")
	value = strings.ReplaceAll(value, ")", "\\)")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[report] "+format+"\n", args...)
}
