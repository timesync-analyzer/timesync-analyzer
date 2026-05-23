package report

import (
	"time"

	"timesync-analyzer/src/internal/storage"
)

type PDFRenderer interface {
	WritePDF(path string, from, to time.Time, stats []storage.ReportStats, charts []pdfChart, chartsPerPage int) error
}
