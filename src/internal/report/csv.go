package report

import (
	"encoding/csv"
	"fmt"
	"os"
	"time"

	"timesync-analyzer/src/internal/storage"
)

func writeCSV(path string, from, to time.Time, stats []storage.ReportStats) error {
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
