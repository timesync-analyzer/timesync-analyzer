package report

import "time"

type GrafanaConfig struct {
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
	PNG    []byte
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

type RenderGroups struct {
	All       bool
	Frequency bool
	Offset    bool
	PathDelay bool
	Status    bool
	Network   bool
	System    bool
}

type Options struct {
	From time.Time
	To   time.Time

	OutDir               string
	DashboardPath        string
	NetworkDashboardPath string
	SystemDashboardPath  string

	Grafana       GrafanaConfig
	Groups        RenderGroups
	SkipCharts    bool
	ChartsPerPage int
	PDFRenderer   PDFRenderer
}

type Result struct {
	ReportDir string
	CSVPath   string
	PDFPath   string
	Rows      int
	Charts    int
}
