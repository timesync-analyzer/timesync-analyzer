package reportd

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"timesync-analyzer/src/internal/report"
	"timesync-analyzer/src/internal/storage"
)

func New(statsLoader storage.ReportStatsLoader, opts Options, logger *zap.Logger) *Service {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 100
	}
	if opts.WorkerCount <= 0 {
		opts.WorkerCount = 1
	}
	if opts.DefaultPeriod <= 0 {
		opts.DefaultPeriod = time.Hour
	}
	if opts.AlertPeriod <= 0 {
		opts.AlertPeriod = opts.DefaultPeriod
	}
	if opts.JobTimeout <= 0 {
		opts.JobTimeout = 15 * time.Minute
	}
	if opts.ChartsPerPage <= 0 {
		opts.ChartsPerPage = 3
	}

	return &Service{
		statsLoader: statsLoader,
		opts:        opts,
		logger:      logger,
		queue:       make(chan string, opts.QueueSize),
		jobs:        make(map[string]*reportJob),
		alertLast:   make(map[string]time.Time),
	}
}

func (s *Service) Start(ctx context.Context) {
	for i := 1; i <= s.opts.WorkerCount; i++ {
		workerID := i
		go s.worker(ctx, workerID)
	}
	if s.opts.ReportTTL > 0 && s.opts.CleanupInterval > 0 {
		s.logger.Info("report cleanup enabled",
			zap.Duration("ttl", s.opts.ReportTTL),
			zap.Duration("interval", s.opts.CleanupInterval),
			zap.String("out_dir", s.opts.OutDir))
		go s.cleanupLoop(ctx)
	}
	if s.opts.AutoReportInterval > 0 {
		window := s.opts.AutoReportWindow
		if window <= 0 {
			window = s.opts.AutoReportInterval
		}
		s.logger.Info("auto report generation enabled",
			zap.Duration("interval", s.opts.AutoReportInterval),
			zap.Duration("window", window))
		go s.autoReportLoop(ctx)
	}
}

func (s *Service) enqueue(spec createJobSpec) (*reportJob, error) {
	now := time.Now().UTC()
	id := fmt.Sprintf("%s-%06d", now.Format("20060102T150405Z"), s.seq.Add(1))

	initialStatus := statusQueued
	var scheduledAt *time.Time
	if spec.Delay > 0 {
		initialStatus = statusScheduled
		t := now.Add(spec.Delay)
		scheduledAt = &t
	}

	job := &reportJob{
		ID:                  id,
		Source:              spec.Source,
		Status:              initialStatus,
		CreatedAt:           now,
		ScheduledAt:         scheduledAt,
		From:                spec.From,
		To:                  spec.To,
		Period:              spec.Period,
		Node:                spec.Node,
		TriggerNode:         spec.TriggerNode,
		Groups:              append([]string(nil), spec.GroupNames...),
		SkipCharts:          spec.SkipCharts,
		ChartsPerPage:       spec.ChartsPerPage,
		Reason:              spec.Reason,
		AlertFingerprint:    spec.AlertFingerprint,
		StatusURL:           "/api/reports/" + id,
		PDFURL:              "/api/reports/" + id + "/summary.pdf",
		CSVURL:              "/api/reports/" + id + "/summary.csv",
		RequestedGrafanaURL: spec.Grafana.URL,
		Grafana:             spec.Grafana,
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.jobOrder = append([]string{id}, s.jobOrder...)
	s.trimOldJobsLocked(500)
	response := cloneJob(job)
	s.mu.Unlock()

	if spec.Delay > 0 {
		time.AfterFunc(spec.Delay, func() {
			s.releaseScheduled(id)
		})
		s.logger.Info("report job scheduled",
			zap.String("id", id), zap.String("source", spec.Source),
			zap.Duration("delay", spec.Delay), zap.Time("scheduled_at", *scheduledAt))
		return response, nil
	}

	select {
	case s.queue <- id:
		s.logger.Info("report job queued", zap.String("id", id), zap.String("source", spec.Source), zap.Time("from", spec.From), zap.Time("to", spec.To))
		return response, nil
	default:
		s.mu.Lock()
		delete(s.jobs, id)
		s.removeJobOrderLocked(id)
		s.mu.Unlock()
		return nil, fmt.Errorf("report queue is full")
	}
}

func (s *Service) releaseScheduled(id string) {
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil || job.Status != statusScheduled {
		s.mu.Unlock()
		return
	}
	job.Status = statusQueued
	s.mu.Unlock()

	select {
	case s.queue <- id:
		s.logger.Info("scheduled report job released", zap.String("id", id))
	default:
		s.markJobFailed(id, fmt.Errorf("queue full when scheduled job became ready"))
	}
}

func (s *Service) worker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case jobID := <-s.queue:
			s.runJob(ctx, workerID, jobID)
		}
	}
}

func (s *Service) runJob(parent context.Context, workerID int, jobID string) {
	job := s.markJobRunning(jobID)
	if job == nil {
		return
	}

	s.logger.Info("report job started", zap.Int("worker", workerID), zap.String("id", job.ID), zap.String("source", job.Source))

	ctx, cancel := context.WithTimeout(parent, s.opts.JobTimeout)
	defer cancel()

	groups, err := ParseRenderGroupList(job.Groups)
	if err != nil {
		s.markJobFailed(job.ID, err)
		return
	}

	grafanaCfg := job.Grafana
	if grafanaCfg.URL == "" {
		grafanaCfg = s.opts.Grafana
	}
	grafanaCfg.Node = firstNonEmpty(job.Node, grafanaCfg.Node)

	result, err := report.Generate(ctx, s.statsLoader, report.Options{
		From:                 job.From,
		To:                   job.To,
		OutDir:               s.opts.OutDir,
		DashboardPath:        s.opts.DashboardPath,
		NetworkDashboardPath: s.opts.NetworkDashboardPath,
		SystemDashboardPath:  s.opts.SystemDashboardPath,
		Grafana:              grafanaCfg,
		Groups:               groups,
		SkipCharts:           job.SkipCharts,
		ChartsPerPage:        job.ChartsPerPage,
		PDFRenderer:          report.MarotoPDFRenderer{},
	})
	if err != nil {
		s.markJobFailed(job.ID, err)
		return
	}

	s.markJobDone(job.ID, result)
	s.logger.Info("report job done", zap.String("id", job.ID), zap.String("pdf", result.PDFPath), zap.Int("rows", result.Rows), zap.Int("charts", result.Charts))
}

func (s *Service) markJobRunning(id string) *reportJob {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	job.Status = statusRunning
	job.StartedAt = &now
	return cloneJob(job)
}

func (s *Service) markJobDone(id string, result report.Result) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return
	}
	job.Status = statusDone
	job.FinishedAt = &now
	job.ReportDir = absPath(result.ReportDir)
	job.PDFPath = absPath(result.PDFPath)
	job.CSVPath = absPath(result.CSVPath)
	job.Rows = result.Rows
	job.Charts = result.Charts
}

func (s *Service) markJobFailed(id string, err error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return
	}
	job.Status = statusFailed
	job.FinishedAt = &now
	job.Error = err.Error()
	s.logger.Error("report job failed", zap.String("id", id), zap.Error(err))
}

func (s *Service) getJob(id string) *reportJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	return cloneJob(job)
}

func (s *Service) listJobs() []*reportJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]*reportJob, 0, len(s.jobOrder))
	for _, id := range s.jobOrder {
		if job := s.jobs[id]; job != nil {
			jobs = append(jobs, cloneJob(job))
		}
	}
	return jobs
}

func (s *Service) reserveAlertFingerprint(fingerprint string, now time.Time) bool {
	if s.opts.AlertCooldown <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.alertLast[fingerprint]
	if ok && now.Sub(last) < s.opts.AlertCooldown {
		return false
	}
	s.alertLast[fingerprint] = now
	return true
}

func (s *Service) trimOldJobsLocked(limit int) {
	if limit <= 0 || len(s.jobOrder) <= limit {
		return
	}
	for _, id := range s.jobOrder[limit:] {
		delete(s.jobs, id)
	}
	s.jobOrder = s.jobOrder[:limit]
}

func (s *Service) removeJobOrderLocked(id string) {
	for i, existing := range s.jobOrder {
		if existing == id {
			s.jobOrder = append(s.jobOrder[:i], s.jobOrder[i+1:]...)
			return
		}
	}
}

func cloneJob(job *reportJob) *reportJob {
	if job == nil {
		return nil
	}
	clone := *job
	clone.Groups = append([]string(nil), job.Groups...)
	return &clone
}
