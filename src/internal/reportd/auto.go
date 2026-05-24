package reportd

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const autoReportSource = "auto"

func (s *Service) autoReportLoop(ctx context.Context) {
	interval := s.opts.AutoReportInterval
	window := s.opts.AutoReportWindow
	if window <= 0 {
		window = interval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.triggerAutoReport(window)
		}
	}
}

func (s *Service) triggerAutoReport(window time.Duration) {
	now := time.Now().UTC()
	spec := createJobSpec{
		From:          now.Add(-window),
		To:            now,
		Period:        window,
		Node:          firstNonEmpty(s.opts.Grafana.Node, ".*"),
		Groups:        s.opts.DefaultGroups,
		GroupNames:    renderGroupNames(s.opts.DefaultGroups),
		SkipCharts:    s.opts.DefaultSkipCharts,
		ChartsPerPage: s.opts.ChartsPerPage,
		Source:        autoReportSource,
		Reason:        "auto-generated",
		Grafana:       s.opts.Grafana,
	}

	job, err := s.enqueue(spec)
	if err != nil {
		s.logger.Warn("auto report enqueue failed",
			zap.Error(err),
			zap.Time("from", spec.From),
			zap.Time("to", spec.To))
		return
	}
	s.logger.Info("auto report enqueued",
		zap.String("id", job.ID),
		zap.Time("from", job.From),
		zap.Time("to", job.To))
}

