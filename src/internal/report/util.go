package report

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"timesync-analyzer/src/internal/storage"
)

func ParseTimeOrDefault(value string, fallback time.Time) (time.Time, error) {
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
	return value.In(time.Local).Format("2006_01_02_15_04_05")
}

func reportPeriodHeader(from, to time.Time) string {
	return fmt.Sprintf("From: %s To: %s", reportDisplayTime(from), reportDisplayTime(to))
}

func reportDisplayTime(value time.Time) string {
	return value.In(time.Local).Format("2006.01.02 15:04:05")
}

func formatMetric(item storage.ReportStats, value float64) string {
	if !item.HasData || math.IsNaN(value) || math.IsInf(value, 0) {
		return ""
	}
	return fmt.Sprintf("%.3f", value)
}

func formatPDFMetric(item storage.ReportStats, value float64) string {
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

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[report] "+format+"\n", args...)
}
