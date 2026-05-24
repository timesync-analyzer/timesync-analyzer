package reportd

import (
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"timesync-analyzer/src/internal/report"
	"timesync-analyzer/src/internal/storage"
)

type jobStatus string

const (
	statusScheduled jobStatus = "scheduled"
	statusQueued    jobStatus = "queued"
	statusRunning   jobStatus = "running"
	statusDone      jobStatus = "done"
	statusFailed    jobStatus = "failed"
)

type Options struct {
	OutDir               string
	DashboardPath        string
	NetworkDashboardPath string
	SystemDashboardPath  string
	Grafana              report.GrafanaConfig
	DefaultGroups        report.RenderGroups
	AlertGroups          report.RenderGroups
	DefaultPeriod        time.Duration
	AlertPeriod          time.Duration
	AlertDelay           time.Duration
	AlertCooldown        time.Duration
	JobTimeout           time.Duration
	ChartsPerPage        int
	DefaultSkipCharts    bool
	QueueSize            int
	WorkerCount          int
	Token                string
	ReportTTL            time.Duration
	CleanupInterval      time.Duration
	AutoReportInterval   time.Duration
	AutoReportWindow     time.Duration
}

type Service struct {
	statsLoader storage.ReportStatsLoader
	opts        Options
	logger      *zap.Logger

	queue chan string
	seq   atomic.Uint64

	mu        sync.Mutex
	jobs      map[string]*reportJob
	jobOrder  []string
	alertLast map[string]time.Time
}

type reportJob struct {
	ID                  string               `json:"id"`
	Source              string               `json:"source"`
	Status              jobStatus            `json:"status"`
	CreatedAt           time.Time            `json:"created_at"`
	ScheduledAt         *time.Time           `json:"scheduled_at,omitempty"`
	StartedAt           *time.Time           `json:"started_at,omitempty"`
	FinishedAt          *time.Time           `json:"finished_at,omitempty"`
	From                time.Time            `json:"from"`
	To                  time.Time            `json:"to"`
	Period              time.Duration        `json:"-"`
	Node                string               `json:"node"`
	TriggerNode         string               `json:"trigger_node,omitempty"`
	Groups              []string             `json:"groups"`
	SkipCharts          bool                 `json:"skip_charts"`
	ChartsPerPage       int                  `json:"charts_per_page"`
	Reason              string               `json:"reason,omitempty"`
	AlertFingerprint    string               `json:"alert_fingerprint,omitempty"`
	ReportDir           string               `json:"report_dir,omitempty"`
	PDFPath             string               `json:"pdf_path,omitempty"`
	CSVPath             string               `json:"csv_path,omitempty"`
	Rows                int                  `json:"rows,omitempty"`
	Charts              int                  `json:"charts,omitempty"`
	Error               string               `json:"error,omitempty"`
	StatusURL           string               `json:"status_url"`
	PDFURL              string               `json:"pdf_url,omitempty"`
	CSVURL              string               `json:"csv_url,omitempty"`
	RequestedGrafanaURL string               `json:"grafana_url,omitempty"`
	Grafana             report.GrafanaConfig `json:"-"`
}

type createReportRequest struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Period          string   `json:"period"`
	PeriodSeconds   int64    `json:"period_seconds"`
	Node            string   `json:"node"`
	Groups          []string `json:"groups"`
	SkipCharts      *bool    `json:"skip_charts"`
	ChartsPerPage   int      `json:"charts_per_page"`
	Source          string   `json:"source"`
	Reason          string   `json:"reason"`
	GrafanaURL      string   `json:"grafana_url"`
	GrafanaToken    string   `json:"grafana_token"`
	GrafanaUser     string   `json:"grafana_user"`
	GrafanaPassword string   `json:"grafana_password"`
}

type grafanaWebhookPayload struct {
	Status       string                 `json:"status"`
	Receiver     string                 `json:"receiver"`
	CommonLabels map[string]string      `json:"commonLabels"`
	Alerts       []grafanaWebhookAlert  `json:"alerts"`
	ExternalURL  string                 `json:"externalURL"`
	Raw          map[string]interface{} `json:"-"`
}

type grafanaWebhookAlert struct {
	Status       string             `json:"status"`
	Labels       map[string]string  `json:"labels"`
	Annotations  map[string]string  `json:"annotations"`
	StartsAt     time.Time          `json:"startsAt"`
	EndsAt       time.Time          `json:"endsAt"`
	Fingerprint  string             `json:"fingerprint"`
	DashboardURL string             `json:"dashboardURL"`
	PanelURL     string             `json:"panelURL"`
	Values       map[string]float64 `json:"values"`
}

type createJobSpec struct {
	From             time.Time
	To               time.Time
	Period           time.Duration
	Delay            time.Duration
	Node             string
	TriggerNode      string
	Groups           report.RenderGroups
	GroupNames       []string
	SkipCharts       bool
	ChartsPerPage    int
	Source           string
	Reason           string
	AlertFingerprint string
	Grafana          report.GrafanaConfig
}

type createReportResponse struct {
	Job *reportJob `json:"job"`
}

type listReportsResponse struct {
	Jobs []*reportJob `json:"jobs"`
}

type grafanaAlertResponse struct {
	Created []*reportJob `json:"created"`
	Skipped []string     `json:"skipped"`
}
