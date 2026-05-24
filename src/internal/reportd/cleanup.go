package reportd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

const reportDirPrefix = "report_"

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(s.opts.CleanupInterval)
	defer ticker.Stop()

	s.runCleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCleanup(ctx)
		}
	}
}

func (s *Service) runCleanup(ctx context.Context) {
	cutoff := time.Now().Add(-s.opts.ReportTTL)
	entries, err := os.ReadDir(s.opts.OutDir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.Warn("cleanup: read output dir failed", zap.String("dir", s.opts.OutDir), zap.Error(err))
		}
		return
	}

	active := s.activeReportDirs()
	removed := make(map[string]struct{})

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), reportDirPrefix) {
			continue
		}
		path := filepath.Join(s.opts.OutDir, entry.Name())
		abs := absPath(path)
		if _, busy := active[abs]; busy {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			s.logger.Warn("cleanup: stat failed", zap.String("path", path), zap.Error(err))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			s.logger.Warn("cleanup: remove failed", zap.String("path", path), zap.Error(err))
			continue
		}
		removed[abs] = struct{}{}
		s.logger.Info("cleanup: removed expired report", zap.String("path", path), zap.Time("mtime", info.ModTime()))
	}

	if len(removed) > 0 {
		s.dropJobsForDirs(removed)
	}
}

func (s *Service) activeReportDirs() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[string]struct{})
	for _, job := range s.jobs {
		if job == nil {
			continue
		}
		switch job.Status {
		case statusScheduled, statusQueued, statusRunning:
		default:
			continue
		}
		if job.ReportDir != "" {
			active[job.ReportDir] = struct{}{}
		}
	}
	return active
}

func (s *Service) dropJobsForDirs(removed map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job == nil || job.ReportDir == "" {
			continue
		}
		if _, gone := removed[job.ReportDir]; !gone {
			continue
		}
		delete(s.jobs, id)
		s.removeJobOrderLocked(id)
	}
}
