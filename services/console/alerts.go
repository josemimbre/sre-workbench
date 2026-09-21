package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Alerting has two halves and the console shows both, because they answer different
// questions. Prometheus knows which alerts are pending or firing *right now*, evaluated
// straight from the burn-rate rules. Alertmanager knows what was actually delivered after
// grouping, inhibition and silencing — which is not the same set, and the gap between
// them is worth seeing.

const maxNotifications = 30

// notification is one webhook delivery from Alertmanager.
type notification struct {
	At       time.Time         `json:"at"`
	Status   string            `json:"status"`
	Receiver string            `json:"receiver"`
	Labels   map[string]string `json:"group_labels"`
	Count    int               `json:"count"`
	Names    []string          `json:"names"`
}

type notificationStore struct {
	mu     sync.RWMutex
	recent []notification
}

func (s *notificationStore) add(n notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append([]notification{n}, s.recent...)
	if len(s.recent) > maxNotifications {
		s.recent = s.recent[:maxNotifications]
	}
}

func (s *notificationStore) list() []notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]notification, len(s.recent))
	copy(out, s.recent)
	return out
}

// handleAlertWebhook receives Alertmanager's notifications. Nothing is persisted: this is
// a laboratory, and a restart is supposed to lose the history.
func (s *server) handleAlertWebhook(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Receiver    string            `json:"receiver"`
		Status      string            `json:"status"`
		GroupLabels map[string]string `json:"groupLabels"`
		Alerts      []struct {
			Labels map[string]string `json:"labels"`
		} `json:"alerts"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"malformed webhook: " + err.Error()})
		return
	}

	names := map[string]bool{}
	for _, a := range payload.Alerts {
		if n := a.Labels["alertname"]; n != "" {
			names[n] = true
		}
	}
	unique := make([]string, 0, len(names))
	for n := range names {
		unique = append(unique, n)
	}
	sort.Strings(unique)

	s.notifications.add(notification{
		At:       time.Now(),
		Status:   payload.Status,
		Receiver: payload.Receiver,
		Labels:   payload.GroupLabels,
		Count:    len(payload.Alerts),
		Names:    unique,
	})
	s.log.Info("alert notification", "status", payload.Status, "alerts", len(payload.Alerts))

	w.WriteHeader(http.StatusNoContent)
}

type alertState struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Severity string `json:"severity"`
	SLO      string `json:"slo"`
	Service  string `json:"service"`
	Summary  string `json:"summary,omitempty"`
}

// handleAlerts reports what Prometheus currently has pending or firing, plus what
// Alertmanager has delivered here.
func (s *server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	series, err := s.prom.instantSeries(r.Context(), `ALERTS{alertstate=~"pending|firing"}`)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}

	alerts := make([]alertState, 0, len(series))
	for _, m := range series {
		alerts = append(alerts, alertState{
			Name:     m["alertname"],
			State:    m["alertstate"],
			Severity: m["severity"],
			SLO:      m["sloth_slo"],
			Service:  m["sloth_service"],
		})
	}
	// Firing before pending, then by name, so the list does not reshuffle between polls.
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].State != alerts[j].State {
			return alerts[i].State == "firing"
		}
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity == "page"
		}
		return alerts[i].Name < alerts[j].Name
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"alerts":        alerts,
		"notifications": s.notifications.list(),
	})
}
