package signal

import "time"

// Severity weights for signal strength calculation.
const (
	SeverityCritical = 1.0
	SeverityWarn     = 0.6
	SeverityInfo     = 0.3
)

// Alert represents an alert with severity and timing.
type Alert struct {
	ID          string    `json:"id"`
	Severity    string    `json:"severity"`
	TriggeredAt time.Time `json:"triggered_at"`
	Source      string    `json:"source"`
	Message     string    `json:"message"`
}

// Strength calculates signal strength with linear time decay.
// Formula: signal = severity_weight * (1 - age/maxAge)
// Returns 0 for alerts older than maxAge.
func Strength(alert Alert, maxAge time.Duration) float64 {
	weight := severityWeight(alert.Severity)
	age := time.Since(alert.TriggeredAt)
	if age > maxAge {
		return 0
	}
	decay := 1.0 - (float64(age) / float64(maxAge))
	return weight * decay
}

// VizBar renders a signal strength as an ASCII bar.
// bars is the total bar width (e.g. 12).
func VizBar(strength float64, bars int) string {
	filled := int(strength * float64(bars))
	if filled > bars {
		filled = bars
	}
	if filled < 0 {
		filled = 0
	}
	return repeat("█", filled) + repeat("░", bars-filled)
}

func severityWeight(severity string) float64 {
	switch severity {
	case "critical":
		return SeverityCritical
	case "warn", "warning":
		return SeverityWarn
	case "info":
		return SeverityInfo
	default:
		return SeverityInfo
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
