package reportd

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"timesync-analyzer/src/internal/report"
)

func parseAPITime(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback.UTC(), nil
	}

	if parsed, ok, err := parseNowExpression(value, time.Now().UTC()); ok || err != nil {
		return parsed, err
	}

	if numeric, err := strconv.ParseInt(value, 10, 64); err == nil {
		if numeric > 100_000_000_000 {
			return time.UnixMilli(numeric).UTC(), nil
		}
		return time.Unix(numeric, 0).UTC(), nil
	}

	return report.ParseTimeOrDefault(value, fallback)
}

func parseNowExpression(value string, now time.Time) (time.Time, bool, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "now" {
		return now, true, nil
	}
	if strings.HasPrefix(value, "now-") || strings.HasPrefix(value, "now+") {
		sign := value[3]
		duration, err := parseLooseDuration(value[4:])
		if err != nil {
			return time.Time{}, true, err
		}
		if sign == '-' {
			return now.Add(-duration), true, nil
		}
		return now.Add(duration), true, nil
	}
	return time.Time{}, false, nil
}

func parseLooseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return time.Duration(seconds * float64(time.Second)), nil
	}
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed, nil
	}
	lower := strings.ToLower(value)
	multiplier := time.Duration(0)
	switch {
	case strings.HasSuffix(lower, "d"):
		multiplier = 24 * time.Hour
	case strings.HasSuffix(lower, "w"):
		multiplier = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	number := strings.TrimSpace(lower[:len(lower)-1])
	amount, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(amount * float64(multiplier)), nil
}

func ParseRenderGroups(value string) (report.RenderGroups, error) {
	return ParseRenderGroupList(splitCSV(value))
}

func ParseRenderGroupList(values []string) (report.RenderGroups, error) {
	var groups report.RenderGroups
	for _, value := range normalizeGroupNames(values) {
		switch value {
		case "":
			continue
		case "all":
			groups.All = true
		case "freq", "frequency":
			groups.Frequency = true
		case "offset":
			groups.Offset = true
		case "path_delay", "path-delay", "pathdelay":
			groups.PathDelay = true
		case "status":
			groups.Status = true
		case "network":
			groups.Network = true
		case "system":
			groups.System = true
		default:
			return groups, fmt.Errorf("unknown render group %q", value)
		}
	}
	return groups, nil
}

func renderGroupNames(groups report.RenderGroups) []string {
	var names []string
	if groups.All {
		names = append(names, "all")
	}
	if groups.Frequency {
		names = append(names, "frequency")
	}
	if groups.Offset {
		names = append(names, "offset")
	}
	if groups.PathDelay {
		names = append(names, "path_delay")
	}
	if groups.Status {
		names = append(names, "status")
	}
	if groups.Network {
		names = append(names, "network")
	}
	if groups.System {
		names = append(names, "system")
	}
	return names
}

func normalizeGroupNames(values []string) []string {
	var normalized []string
	for _, value := range values {
		for _, part := range splitCSV(value) {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				normalized = append(normalized, part)
			}
		}
	}
	return normalized
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func absPath(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
