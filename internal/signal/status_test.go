package signal

import (
	"strings"
	"testing"
	"time"
)

// smokeAlarmStatus is a real ocd-smoke-alarm /status document (trimmed to the
// keys tuner reads). It has no `alerts` key: the instance reports targets.
const smokeAlarmStatus = `{
  "service": "ocd-smoke-alarm",
  "version": "0.4.0",
  "now": "2026-10-27T22:40:00Z",
  "live": true,
  "ready": true,
  "summary": {"total": 3, "healthy": 1, "degraded": 1, "unhealthy": 1},
  "components": [{"name": "engine", "healthy": true, "updated_at": "2026-10-27T22:40:00Z"}],
  "targets": [
    {
      "id": "peer-a",
      "state": "healthy",
      "severity": "info",
      "message": "reachable",
      "regression": false,
      "checked_at": "2026-10-27T22:39:58Z",
      "latency_ms": 7
    },
    {
      "id": "peer-b",
      "state": "degraded",
      "severity": "warn",
      "message": "slow: 900ms",
      "regression": false,
      "checked_at": "2026-10-27T22:39:30Z",
      "latency_ms": 900
    },
    {
      "id": "peer-c",
      "state": "unhealthy",
      "severity": "critical",
      "message": "connection refused",
      "regression": true,
      "checked_at": "2026-10-27T22:39:00Z",
      "latency_ms": 0
    }
  ]
}`

func TestDecodeStatusMapsSmokeAlarmTargets(t *testing.T) {
	alerts, err := DecodeStatus(strings.NewReader(smokeAlarmStatus))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts from 3 targets (the healthy one is not an alert), got %d: %+v", len(alerts), alerts)
	}

	degraded := alerts[0]
	if degraded.Source != "peer-b" || degraded.ID != "peer-b" {
		t.Fatalf("expected source and id peer-b, got %+v", degraded)
	}
	if degraded.Severity != "warn" {
		t.Fatalf("expected warn, got %q", degraded.Severity)
	}
	if !degraded.TriggeredAt.Equal(time.Date(2026, 10, 27, 22, 39, 30, 0, time.UTC)) {
		t.Fatalf("expected triggered_at to come from checked_at, got %s", degraded.TriggeredAt)
	}
	if degraded.Message != "slow: 900ms" {
		t.Fatalf("expected the target message to carry through, got %q", degraded.Message)
	}

	critical := alerts[1]
	if critical.Source != "peer-c" || critical.Severity != "critical" {
		t.Fatalf("expected peer-c critical, got %+v", critical)
	}
}

func TestDecodeStatusHealthyInstanceHasNoAlerts(t *testing.T) {
	body := `{"targets":[{"id":"peer","state":"healthy","severity":"info","checked_at":"2026-10-27T22:39:58Z"}]}`

	alerts, err := DecodeStatus(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("a healthy target is not an alert, got %+v", alerts)
	}
}

func TestDecodeStatusDerivesSeverityWhenTargetHasNone(t *testing.T) {
	body := `{"targets":[
		{"id":"down","state":"unhealthy","checked_at":"2026-10-27T22:39:00Z"},
		{"id":"slow","state":"degraded","checked_at":"2026-10-27T22:39:00Z"},
		{"id":"pending","state":"unknown","checked_at":"2026-10-27T22:39:00Z"}
	]}`

	alerts, err := DecodeStatus(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts (unknown is not one yet), got %+v", alerts)
	}
	if alerts[0].Severity != "critical" {
		t.Fatalf("expected an unhealthy target with no severity to be critical, got %q", alerts[0].Severity)
	}
	if alerts[1].Severity != "warn" {
		t.Fatalf("expected a degraded target with no severity to be warn, got %q", alerts[1].Severity)
	}
}

func TestDecodeStatusPrefersExplicitAlertsArray(t *testing.T) {
	body := `{"alerts":[{"id":"a1","severity":"critical","triggered_at":"2026-10-27T22:39:00Z","source":"disk"}],"targets":[{"id":"peer","state":"unhealthy","checked_at":"2026-10-27T22:39:00Z"}]}`

	alerts, err := DecodeStatus(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Source != "disk" {
		t.Fatalf("expected the explicit alerts array to win, got %+v", alerts)
	}
}

func TestDecodeStatusAcceptsBareAlertArray(t *testing.T) {
	body := `[{"id":"a1","severity":"warn","triggered_at":"2026-10-27T22:39:00Z","source":"disk"}]`

	alerts, err := DecodeStatus(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Severity != "warn" {
		t.Fatalf("expected the array to pass through, got %+v", alerts)
	}
}

func TestDecodeStatusRejectsNonStatusBodies(t *testing.T) {
	for name, body := range map[string]string{
		"empty":  "   ",
		"html":   "<html><body>not a status document</body></html>",
		"null":   "null",
		"broken": `{"targets":`,
		"number": "42",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStatus(strings.NewReader(body)); err == nil {
				t.Fatalf("expected an error for %q", body)
			}
		})
	}
}
