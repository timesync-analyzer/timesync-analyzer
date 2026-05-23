package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

func shouldRenderSyncDashboard(groups RenderGroups) bool {
	if hasSyncRenderGroup(groups) {
		return true
	}
	return !groups.Network && !groups.System
}

func hasSyncRenderGroup(groups RenderGroups) bool {
	return groups.All || groups.Frequency || groups.Offset || groups.PathDelay || groups.Status
}

func filterPanelsByGroups(panels []grafanaPanel, groups RenderGroups) []grafanaPanel {
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

func panelMatchesGroups(panel grafanaPanel, groups RenderGroups) bool {
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

func renderGrafanaCharts(ctx context.Context, cfg GrafanaConfig, panels []grafanaPanel, from, to time.Time) ([]pdfChart, error) {
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

func fetchGrafanaPanel(ctx context.Context, client *http.Client, cfg GrafanaConfig, panel grafanaPanel, from, to time.Time) ([]byte, error) {
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

func grafanaRenderURL(cfg GrafanaConfig, panel grafanaPanel, from, to time.Time) string {
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
		PNG:    pngData,
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
