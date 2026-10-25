package tv

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/tuner/internal/channelsource"
	"github.com/james-gibson/tuner/internal/mdns"
)

func discoveredSmokeAlarm(instance string, port int) mdns.Service {
	return mdns.Service{
		Name:        instance + "._smoke-alarm._tcp.local.",
		ServiceType: mdns.SmokeAlarmServiceType,
		Host:        "192.168.1.50",
		Port:        port,
	}
}

// A discovered instance gets its own TOML, and adding a third instance does
// not regenerate the two that were already there.
func TestWatchDiscoveredWritesOneFilePerInstance(t *testing.T) {
	dir := t.TempDir()
	reg := channelsource.NewRegistry()
	unsubscribe := WatchDiscovered(reg, dir)
	defer unsubscribe()

	reg.Observe(discoveredSmokeAlarm("smoke-alarm-a", 18088))
	reg.Observe(discoveredSmokeAlarm("smoke-alarm-b", 18089))

	pathA := filepath.Join(dir, "ocd-smoke-alarm-a.toml")
	pathB := filepath.Join(dir, "ocd-smoke-alarm-b.toml")
	for _, path := range []string{pathA, pathB} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}

	before, err := os.Stat(pathA)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	contentBefore, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	reg.Observe(discoveredSmokeAlarm("smoke-alarm-c", 18090))
	if _, statErr := os.Stat(filepath.Join(dir, "ocd-smoke-alarm-c.toml")); statErr != nil {
		t.Fatalf("third instance did not get a channel: %v", statErr)
	}

	after, err := os.Stat(pathA)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	contentAfter, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(contentAfter, contentBefore) {
		t.Fatal("an existing channel was rewritten when a third instance was discovered")
	}

	content := string(contentBefore)
	if !strings.Contains(content, "192.168.1.50:18088/status") {
		t.Fatalf("channel command does not point at the instance endpoint:\n%s", content)
	}
}

func TestWatchDiscoveredRewritesReboundEndpoint(t *testing.T) {
	dir := t.TempDir()
	reg := channelsource.NewRegistry()
	unsubscribe := WatchDiscovered(reg, dir)
	defer unsubscribe()

	now := time.Now()
	first := discoveredSmokeAlarm("smoke-alarm-primary", 18088)
	first.DiscoveredAt = now.Add(-time.Minute)
	reg.Observe(first)

	rebound := discoveredSmokeAlarm("smoke-alarm-primary", 18089)
	rebound.DiscoveredAt = now
	reg.Observe(rebound)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("a rebind produced %d files, want 1", len(entries))
	}

	content, err := os.ReadFile(filepath.Join(dir, "ocd-smoke-alarm-primary.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), "192.168.1.50:18089/status") {
		t.Fatalf("channel was not updated to the rebound endpoint:\n%s", content)
	}
}

func TestWatchDiscoveredRemovesDepartedChannel(t *testing.T) {
	dir := t.TempDir()
	reg := channelsource.NewRegistry()
	unsubscribe := WatchDiscovered(reg, dir)
	defer unsubscribe()

	a := discoveredSmokeAlarm("smoke-alarm-a", 18088)
	b := discoveredSmokeAlarm("smoke-alarm-b", 18089)
	reg.Observe(a)
	reg.Observe(b)

	reg.Retire(a)

	if _, err := os.Stat(filepath.Join(dir, "ocd-smoke-alarm-a.toml")); !os.IsNotExist(err) {
		t.Fatalf("departed instance's channel was not removed (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ocd-smoke-alarm-b.toml")); err != nil {
		t.Fatalf("remaining instance's channel was removed: %v", err)
	}
}

// A smoke-alarm calling itself "ntp" must not replace tuner's NTP channel.
func TestWatchDiscoveredDoesNotShadowPresets(t *testing.T) {
	dir := t.TempDir()
	reg := channelsource.NewRegistry()
	unsubscribe := WatchDiscovered(reg, dir)
	defer unsubscribe()

	preset := BuiltinPresets()["ntp"]
	path, _, err := WriteChannel(dir, preset)
	if err != nil {
		t.Fatalf("write preset: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preset: %v", err)
	}

	reg.Observe(discoveredSmokeAlarm("ntp", 18088))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preset: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("a discovered instance overwrote a preset channel")
	}
}

func TestChannelFileSanitizesInstanceNames(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{name: "smoke-alarm-late", want: "ocd-smoke-alarm-late.toml"},
		{name: "demo/alarm-a", want: "ocd-demo-alarm-a.toml"},
		{name: ".", want: "ocd-channel.toml"},
		{name: "", want: "ocd-channel.toml"},
	} {
		if got := ChannelFile(tc.name); got != tc.want {
			t.Errorf("ChannelFile(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Network names must never select a path: a lezz-demo seed contains a
	// slash, and a hostile label could contain "..", which filepath.Join would
	// resolve outside the cable directory.
	for _, name := range []string{"../escape", "demo/alarm-a", "..", ".", "a\\b"} {
		file := ChannelFile(name)
		if strings.ContainsAny(file, `/\`) {
			t.Errorf("ChannelFile(%q) = %q still contains a path separator", name, file)
		}
		if strings.HasPrefix(strings.TrimPrefix(file, "ocd-"), ".") {
			t.Errorf("ChannelFile(%q) = %q is hidden or a dot entry", name, file)
		}
	}
}

func TestRemoveChannel(t *testing.T) {
	dir := t.TempDir()
	p := Preset{Name: "smoke-alarm-late", Command: "echo", Watch: 1, Preview: "echo"}
	path, _, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	removedPath, removed, err := RemoveChannel(dir, p.Name)
	if err != nil || !removed {
		t.Fatalf("remove: %v (removed=%v)", err, removed)
	}
	if removedPath != path {
		t.Fatalf("removed %q, wrote %q", removedPath, path)
	}

	// Removing an absent channel is not an error: a source may depart before
	// its first file was ever written.
	if _, removed, err := RemoveChannel(dir, p.Name); err != nil || removed {
		t.Fatalf("second remove: err=%v removed=%v", err, removed)
	}
}
