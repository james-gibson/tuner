package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/james-gibson/tuner/internal/featurebrowser"
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

// TestCmdChannelNeedsNoConfigFile guards tuner#16: cmdChannel was the only
// subcommand that defaulted --config to a file in the cwd, so the documented
// `tuner channel <name>` step failed on a machine with no tuner.config.yaml.
// Built-in defaults set tv.auto_launch, so the preset is written to the default
// cable dir — which is why HOME is redirected into the test's temp dir.
func TestCmdChannelNeedsNoConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // deliberately no tuner.config.yaml here

	out := captureStdout(t, func() {
		cmdChannel([]string{"dns"})
	})

	want := filepath.Join(home, ".config", "tuner", "cable", "ocd-dns.toml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("preset not written to the default cable dir: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "wrote channel:") {
		t.Fatalf("expected the written path on stdout, got:\n%s", out)
	}
}

// TestCmdChannelPrintsPresetWithoutAutoLaunch covers the preview path with no
// config file: a config that disables auto_launch must still resolve the preset
// and print its TOML rather than erroring.
func TestCmdChannelPrintsPresetWithoutAutoLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)

	cfgPath := filepath.Join(dir, "no-launch.yaml")
	if err := os.WriteFile(cfgPath, []byte("tv:\n  auto_launch: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmdChannel([]string{"dns", "--config=" + cfgPath})
	})

	if !strings.Contains(out, `name = "dns"`) || !strings.Contains(out, "[source]") {
		t.Fatalf("expected the dns preset TOML, got:\n%s", out)
	}
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

// fakeADHDEndpoint serves the MCP methods `tuner features` calls. The lab's
// certification tools are declared absent, so a served contract exists only as
// a fixture.
func fakeADHDEndpoint(t *testing.T) *httptest.Server {
	t.Helper()

	results := map[string]string{
		"adhd.features.certified": `{"caller_rung":3,"features":[
			{"feature_file":"adhd/mdns-discovery","certified":true,"cluster_member":"smoke:alarm-a/t1"},
			{"feature_file":"adhd/headless-mode","certified":false}]}`,
		"adhd.skills.certified": `{"skills":[{"skill_name":"open-the-pickle-jar","certified_rung":3}]}`,
		"adhd.features.verify":  `{"still_certified":false,"note":"isotope rotated","current_isotope_id":"iso-2"}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"adhd"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[
				{"name":"adhd.features.certified"},
				{"name":"adhd.skills.certified"},
				{"name":"adhd.features.verify"}]}}`, req.ID)
		case "tools/call":
			raw, ok := results[req.Params.Name]
			if !ok {
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID)
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, raw)
		default:
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCmdFeaturesRendersMergedView(t *testing.T) {
	adhd := fakeADHDEndpoint(t)

	out := captureStdout(t, func() {
		cmdFeatures([]string{"--endpoint=" + adhd.URL})
	})

	for _, want := range []string{
		"1 / 2 certified (50%)",
		"Gherkin Features",
		"Cluster Skills",
		"certified by alarm-a/t1",
		"rung 3 assigned by adhd",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("features output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdFeaturesJSONIsTheMergedView(t *testing.T) {
	adhd := fakeADHDEndpoint(t)

	out := captureStdout(t, func() {
		cmdFeatures([]string{"--endpoint=" + adhd.URL, "--json"})
	})

	var view featurebrowser.View
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("--json output is not a view: %v\n%s", err, out)
	}
	if view.Status != featurebrowser.StatusServed || view.Summary.FeaturesCertified != 1 {
		t.Fatalf("unexpected view: %+v", view)
	}
	if len(view.Skills) != 1 || view.Skills[0].SkillName != "open-the-pickle-jar" {
		t.Fatalf("skills missing from the JSON view: %+v", view.Skills)
	}
}

func TestCmdFeaturesVerifyReportsRotation(t *testing.T) {
	adhd := fakeADHDEndpoint(t)

	out := captureStdout(t, func() {
		cmdFeatures([]string{"--endpoint=" + adhd.URL, "--verify=adhd/mdns-discovery"})
	})

	if !strings.Contains(out, "isotope rotated") || !strings.Contains(out, "iso-2") {
		t.Fatalf("verify output does not report the rotation:\n%s", out)
	}
}
