package signal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// TargetStatus mirrors the target entries of a monitor's /status document.
// Only the fields a signal reading needs are decoded: the document also carries
// protocol, endpoint, latency and regression, none of which move a bar.
type TargetStatus struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	CheckedAt time.Time `json:"checked_at"`
}

// StatusDocument is the monitor /status payload as tuner reads it.
//
// A monitor may describe alerting in either of two ways and tuner has to cope
// with both: a smoke-alarm instance reports its monitored `targets` (there is
// no `alerts` key at all), while an `alerts` array is the shape tuner-viz and
// the older presets were written against (tuner#23).
type StatusDocument struct {
	RawAlerts []Alert        `json:"alerts"`
	Targets   []TargetStatus `json:"targets"`
}

// DecodeStatus reads a status payload in any of the three shapes tuner is
// pointed at: a smoke-alarm /status document, a document with an explicit
// `alerts` array, or a bare array of alerts (what `tuner-viz signal --stdin`
// consumes). It always returns alerts, never the raw document.
func DecodeStatus(r io.Reader) ([]Alert, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read status: %w", err)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errors.New("empty status document")
	}

	switch trimmed[0] {
	case '[':
		var alerts []Alert
		if err := json.Unmarshal(body, &alerts); err != nil {
			return nil, fmt.Errorf("decode alert array: %w", err)
		}
		return alerts, nil
	case '{':
		var doc StatusDocument
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("decode status document: %w", err)
		}
		return doc.Alerts(), nil
	default:
		return nil, fmt.Errorf("unrecognized status document: %q", firstLine(trimmed))
	}
}

// Alerts returns the document as signal alerts. An explicit `alerts` array
// wins; otherwise the monitor's targets are mapped, and only the ones that are
// not healthy become alerts.
func (d StatusDocument) Alerts() []Alert {
	if len(d.RawAlerts) > 0 {
		return d.RawAlerts
	}

	alerts := make([]Alert, 0, len(d.Targets))
	for _, target := range d.Targets {
		if alert, ok := alertFor(target); ok {
			alerts = append(alerts, alert)
		}
	}
	return alerts
}

// alertFor maps one monitored target to an alert, or reports that it is not
// one. A healthy target is not an alert even though smoke-alarm gives it a
// severity ("info"): severity there rates a result, it does not announce one.
func alertFor(target TargetStatus) (Alert, bool) {
	state := strings.ToLower(strings.TrimSpace(target.State))
	switch state {
	case "healthy", "unknown", "":
		return Alert{}, false
	}

	return Alert{
		ID:          target.ID,
		Severity:    severityFor(target.Severity, state),
		TriggeredAt: target.CheckedAt,
		// The target is the source: a bar's label should name what is alerting,
		// not the instance that happens to be hosting the check.
		Source:  target.ID,
		Message: target.Message,
	}, true
}

// severityFor prefers the monitor's own severity and falls back to the state
// when it carries none. A target that is not healthy is at least a warning;
// only the failure states escalate to critical.
func severityFor(severity, state string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return "critical"
	case "warn", "warning":
		return "warn"
	case "info":
		return "info"
	}

	switch state {
	case "unhealthy", "outage", "regression", "down", "failed":
		return "critical"
	case "degraded":
		return "warn"
	default:
		return "warn"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
