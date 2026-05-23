package reportd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Service) handleGrafanaAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var payload grafanaWebhookPayload
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "decode Grafana webhook: "+err.Error())
		return
	}

	var created []*reportJob
	var skipped []string
	now := time.Now().UTC()

	for i, alert := range payload.Alerts {
		alertStatus := firstNonEmpty(alert.Status, payload.Status)
		if alertStatus != "firing" {
			skipped = append(skipped, "status="+alertStatus)
			continue
		}

		fingerprint := alertFingerprint(alert, payload.CommonLabels)
		if fingerprint == "" {
			fingerprint = fmt.Sprintf("alert-%d-%d", now.UnixNano(), i)
		}
		if !s.reserveAlertFingerprint(fingerprint, now) {
			skipped = append(skipped, fingerprint+": cooldown")
			continue
		}

		node := firstNonEmpty(alert.Labels["node"], alert.Labels["hostname"], payload.CommonLabels["node"], payload.CommonLabels["hostname"], s.opts.Grafana.Node)
		alertName := firstNonEmpty(alert.Labels["alertname"], payload.CommonLabels["alertname"], "grafana alert")
		reason := alertName
		if alert.PanelURL != "" {
			reason += " " + alert.PanelURL
		}

		spec := createJobSpec{
			From:             now.Add(-s.opts.AlertPeriod),
			To:               now,
			Period:           s.opts.AlertPeriod,
			Node:             node,
			Groups:           s.opts.AlertGroups,
			GroupNames:       renderGroupNames(s.opts.AlertGroups),
			SkipCharts:       s.opts.DefaultSkipCharts,
			ChartsPerPage:    s.opts.ChartsPerPage,
			Source:           "grafana_alert",
			Reason:           reason,
			AlertFingerprint: fingerprint,
			Grafana:          s.opts.Grafana,
		}

		job, err := s.enqueue(spec)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		created = append(created, job)
	}

	status := http.StatusOK
	if len(created) > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, grafanaAlertResponse{Created: created, Skipped: skipped})
}

func alertFingerprint(alert grafanaWebhookAlert, commonLabels map[string]string) string {
	if alert.Fingerprint != "" {
		return alert.Fingerprint
	}
	var parts []string
	for _, key := range []string{"alertname", "node", "hostname", "instance"} {
		if value := firstNonEmpty(alert.Labels[key], commonLabels[key]); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, "|")
}
