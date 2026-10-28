package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestServiceTypesKeepsStdoutClean guards tuner#22: cmdMdns parses stdout as
// records (and --format=json parses it as JSON), so serviceTypes must not emit
// the resolved list as a line of output.
func TestServiceTypesKeepsStdoutClean(t *testing.T) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	got := serviceTypes(nil)

	os.Stdout = orig
	if err = w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	if len(out) != 0 {
		t.Fatalf("serviceTypes wrote %q to stdout, want nothing", out)
	}
	if !strings.Contains(got, "_tuner._tcp") {
		t.Fatalf("serviceTypes returned %q, want the built-in type list", got)
	}
}

func TestServiceTypesHonoursServicesFlag(t *testing.T) {
	got := serviceTypes([]string{"--services=_foo._tcp,_bar._tcp"})
	if want := "_foo._tcp,_bar._tcp"; got != want {
		t.Fatalf("serviceTypes() = %q, want %q", got, want)
	}
}

// smokeAlarmStatusDoc is an ocd-smoke-alarm /status body: it reports monitored
// targets and has no `alerts` key at all (tuner#23).
const smokeAlarmStatusDoc = `{
  "service": "ocd-smoke-alarm",
  "live": true,
  "ready": true,
  "summary": {"total": 2, "healthy": 1, "unhealthy": 1},
  "components": [],
  "targets": [
    {"id": "peer-a", "state": "healthy", "severity": "info", "message": "reachable", "checked_at": "2026-10-27T22:39:58Z"},
    {"id": "peer-c", "state": "unhealthy", "severity": "critical", "message": "connection refused", "checked_at": "2026-10-27T22:39:00Z"}
  ]
}`

func captureStdout(t *testing.T, run func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	run()

	os.Stdout = orig
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("close pipe: %v", closeErr)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

func statusServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCmdSignalsRendersTargets is the regression tuner#23 reported: pointing
// signals at a real smoke-alarm /status printed "No active alerts" whatever the
// targets said, because the command only looked for an `alerts` key.
func TestCmdSignalsRendersTargets(t *testing.T) {
	srv := statusServer(t, smokeAlarmStatusDoc)

	out := captureStdout(t, func() {
		cmdSignals([]string{"--endpoint=" + srv.URL})
	})

	if strings.Contains(out, "No active alerts") {
		t.Fatalf("an unhealthy target was reported as no alerts:\n%s", out)
	}
	if !strings.Contains(out, "peer-c") {
		t.Fatalf("expected the unhealthy target in the bars, got:\n%s", out)
	}
	if !strings.Contains(out, "critical") {
		t.Fatalf("expected the target severity in the bars, got:\n%s", out)
	}
	if strings.Contains(out, "peer-a") {
		t.Fatalf("a healthy target must not appear as an alert, got:\n%s", out)
	}
}

// TestCmdSignalsJSONIsAlertArray covers the preset path: `tuner signals --json`
// has to hand tuner-viz the array it reads from stdin.
func TestCmdSignalsJSONIsAlertArray(t *testing.T) {
	srv := statusServer(t, smokeAlarmStatusDoc)

	out := captureStdout(t, func() {
		cmdSignals([]string{"--endpoint=" + srv.URL, "--json"})
	})

	var alerts []struct {
		ID          string `json:"id"`
		Severity    string `json:"severity"`
		TriggeredAt string `json:"triggered_at"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &alerts); err != nil {
		t.Fatalf("--json output is not an alert array: %v\n%s", err, out)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert (the healthy target is not one), got %d: %s", len(alerts), out)
	}
	if alerts[0].Source != "peer-c" || alerts[0].Severity != "critical" || alerts[0].TriggeredAt == "" {
		t.Fatalf("unexpected alert: %+v", alerts[0])
	}
}
