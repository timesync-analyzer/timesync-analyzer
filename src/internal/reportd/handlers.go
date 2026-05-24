package reportd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"timesync-analyzer/src/internal/report"
)

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/reports", s.withAuth(s.handleReports))
	mux.HandleFunc("/api/reports/", s.withAuth(s.handleReportByID))
	mux.HandleFunc("/api/grafana/alerts", s.withAuth(s.handleGrafanaAlerts))
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Routes(mux)
	return withCORS(mux)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleReports(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, listReportsResponse{Jobs: s.listJobs()})
	case http.MethodPost:
		req, err := readCreateReportRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		spec, err := s.createSpecFromRequest(req, s.opts.DefaultPeriod, s.opts.DefaultGroups, "manual")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		job, err := s.enqueue(spec)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, createReportResponse{Job: job})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Service) handleReportByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/reports/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}

	job := s.getJob(parts[0])
	if job == nil {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, job)
		return
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if job.Status != statusDone {
		writeError(w, http.StatusConflict, "report is not ready")
		return
	}

	switch parts[1] {
	case "summary.pdf":
		if job.PDFPath == "" {
			writeError(w, http.StatusNotFound, "pdf not found")
			return
		}
		serveReportFile(w, r, job, job.PDFPath, "application/pdf")
	case "summary.csv":
		if job.CSVPath == "" {
			writeError(w, http.StatusNotFound, "csv not found")
			return
		}
		serveReportFile(w, r, job, job.CSVPath, "text/csv; charset=utf-8")
	default:
		writeError(w, http.StatusNotFound, "report artifact not found")
	}
}

func serveReportFile(w http.ResponseWriter, r *http.Request, job *reportJob, path, contentType string) {
	disposition := "attachment"
	if inline, _ := strconv.ParseBool(r.URL.Query().Get("inline")); inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, downloadFilename(job, path)))
	http.ServeFile(w, r, path)
}

func downloadFilename(job *reportJob, path string) string {
	base := filepath.Base(path)
	prefix := filepath.Base(filepath.Dir(path))
	if prefix == "" || prefix == "." || prefix == "/" {
		prefix = job.ID
	}
	return sanitizeFilename(prefix + "-" + base)
}

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"\"", "_",
		"\\", "_",
		"/", "_",
		"\r", "_",
		"\n", "_",
	)
	return replacer.Replace(name)
}

func (s *Service) createSpecFromRequest(req createReportRequest, defaultPeriod time.Duration, defaultGroups report.RenderGroups, defaultSource string) (createJobSpec, error) {
	now := time.Now().UTC()
	period := defaultPeriod
	if req.Period != "" {
		parsed, err := parseLooseDuration(req.Period)
		if err != nil {
			return createJobSpec{}, fmt.Errorf("parse period: %w", err)
		}
		period = parsed
	}
	if req.PeriodSeconds > 0 {
		period = time.Duration(req.PeriodSeconds) * time.Second
	}
	if period <= 0 {
		return createJobSpec{}, fmt.Errorf("period must be positive")
	}

	to, err := parseAPITime(req.To, now)
	if err != nil {
		return createJobSpec{}, fmt.Errorf("parse to: %w", err)
	}
	from, err := parseAPITime(req.From, to.Add(-period))
	if err != nil {
		return createJobSpec{}, fmt.Errorf("parse from: %w", err)
	}
	if !from.Before(to) {
		return createJobSpec{}, fmt.Errorf("from must be before to")
	}

	groups := defaultGroups
	groupNames := renderGroupNames(defaultGroups)
	if len(req.Groups) > 0 {
		var err error
		groups, err = ParseRenderGroupList(req.Groups)
		if err != nil {
			return createJobSpec{}, err
		}
		groupNames = normalizeGroupNames(req.Groups)
	}

	skipCharts := s.opts.DefaultSkipCharts
	if req.SkipCharts != nil {
		skipCharts = *req.SkipCharts
	}
	chartsPerPage := s.opts.ChartsPerPage
	if req.ChartsPerPage > 0 {
		chartsPerPage = req.ChartsPerPage
	}

	grafanaCfg := s.opts.Grafana
	grafanaCfg.URL = firstNonEmpty(req.GrafanaURL, grafanaCfg.URL)
	grafanaCfg.Token = firstNonEmpty(req.GrafanaToken, grafanaCfg.Token)
	grafanaCfg.User = firstNonEmpty(req.GrafanaUser, grafanaCfg.User)
	grafanaCfg.Password = firstNonEmpty(req.GrafanaPassword, grafanaCfg.Password)

	return createJobSpec{
		From:          from,
		To:            to,
		Period:        period,
		Node:          firstNonEmpty(req.Node, grafanaCfg.Node, ".*"),
		Groups:        groups,
		GroupNames:    groupNames,
		SkipCharts:    skipCharts,
		ChartsPerPage: chartsPerPage,
		Source:        firstNonEmpty(req.Source, defaultSource),
		Reason:        req.Reason,
		Grafana:       grafanaCfg,
	}, nil
}

func readCreateReportRequest(r *http.Request) (createReportRequest, error) {
	var req createReportRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, http.ErrBodyNotAllowed) {
			return req, err
		}
	}

	q := r.URL.Query()
	if req.From == "" {
		req.From = q.Get("from")
	}
	if req.To == "" {
		req.To = q.Get("to")
	}
	if req.Period == "" {
		req.Period = q.Get("period")
	}
	if req.PeriodSeconds == 0 {
		if value := q.Get("period_seconds"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return req, fmt.Errorf("parse period_seconds: %w", err)
			}
			req.PeriodSeconds = parsed
		}
	}
	if req.Node == "" {
		req.Node = q.Get("node")
	}
	if req.Source == "" {
		req.Source = q.Get("source")
	}
	if req.Reason == "" {
		req.Reason = q.Get("reason")
	}
	if req.GrafanaURL == "" {
		req.GrafanaURL = q.Get("grafana_url")
	}
	if len(req.Groups) == 0 {
		req.Groups = splitCSV(firstNonEmpty(q.Get("groups"), q.Get("group")))
	}
	if req.SkipCharts == nil {
		if value := q.Get("skip_charts"); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return req, fmt.Errorf("parse skip_charts: %w", err)
			}
			req.SkipCharts = &parsed
		}
	}
	if req.ChartsPerPage == 0 {
		if value := q.Get("charts_per_page"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return req, fmt.Errorf("parse charts_per_page: %w", err)
			}
			req.ChartsPerPage = parsed
		}
	}
	return req, nil
}
