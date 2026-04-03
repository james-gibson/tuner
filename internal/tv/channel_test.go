package tv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateTOML(t *testing.T) {
	p := Preset{
		Name:        "test-channel",
		Description: "A test channel",
		Command:     `echo "hello"`,
		Watch:       5.0,
		Preview:     `echo "preview"`,
	}
	toml, err := GenerateTOML(p)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(toml, `name = "test-channel"`) {
		t.Fatalf("expected name in TOML, got:\n%s", toml)
	}
	if !strings.Contains(toml, `description = "A test channel"`) {
		t.Fatalf("expected description in TOML, got:\n%s", toml)
	}
	if !strings.Contains(toml, `command = 'sh -c "echo \"hello\""'`) {
		t.Fatalf("expected command in TOML, got:\n%s", toml)
	}
	if !strings.Contains(toml, "watch = 5.0") {
		t.Fatalf("expected watch in TOML, got:\n%s", toml)
	}
	if !strings.Contains(toml, `[ui.preview_panel]`) {
		t.Fatalf("expected ui.preview_panel in TOML, got:\n%s", toml)
	}
}

func TestWriteChannel(t *testing.T) {
	dir := t.TempDir()
	p := Preset{
		Name:        "test",
		Description: "Test",
		Command:     "echo test",
		Watch:       1.0,
		Preview:     "echo preview",
	}
	path, written, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !written {
		t.Fatal("expected file to be written on first call")
	}
	expected := filepath.Join(dir, "ocd-test.toml")
	if path != expected {
		t.Fatalf("expected path %q, got %q", expected, path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), `name = "test"`) {
		t.Fatalf("expected name in file, got:\n%s", content)
	}
}

func TestWriteAllPresets(t *testing.T) {
	dir := t.TempDir()
	paths, written, err := WriteAllPresets(dir)
	if err != nil {
		t.Fatalf("write all: %v", err)
	}
	if len(paths) != 4 {
		t.Fatalf("expected 4 preset files, got %d", len(paths))
	}
	if written != 4 {
		t.Fatalf("expected 4 files written, got %d", written)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("file %q does not exist: %v", p, err)
		}
	}
}

func TestWriteChannelCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cable")
	p := Preset{
		Name:    "test",
		Command: "echo test",
		Watch:   1.0,
		Preview: "echo test",
	}
	path, written, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !written {
		t.Fatal("expected file to be written")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestWriteChannelSkipsIdentical(t *testing.T) {
	dir := t.TempDir()
	p := Preset{
		Name:        "test",
		Description: "Test",
		Command:     "echo test",
		Watch:       1.0,
		Preview:     "echo preview",
	}

	// First write should succeed
	path, written, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !written {
		t.Fatal("expected file to be written on first call")
	}

	// Second write with same content should skip
	path2, written2, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if written2 {
		t.Fatal("expected file to be skipped on second call with identical content")
	}
	if path != path2 {
		t.Fatalf("expected same path, got %q and %q", path, path2)
	}

	// Write with different content should write
	p.Description = "Modified"
	_, written3, err := WriteChannel(dir, p)
	if err != nil {
		t.Fatalf("third write: %v", err)
	}
	if !written3 {
		t.Fatal("expected file to be written when content differs")
	}
}

func TestWriteAllPresetsSkipsIdentical(t *testing.T) {
	dir := t.TempDir()

	// First call should write all
	paths, written, err := WriteAllPresets(dir)
	if err != nil {
		t.Fatalf("first write all: %v", err)
	}
	if written != len(paths) {
		t.Fatalf("expected %d files written, got %d", len(paths), written)
	}

	// Second call should skip all
	paths2, written2, err := WriteAllPresets(dir)
	if err != nil {
		t.Fatalf("second write all: %v", err)
	}
	if written2 != 0 {
		t.Fatalf("expected 0 files written on second call, got %d", written2)
	}
	if len(paths2) != len(paths) {
		t.Fatalf("expected same number of paths, got %d and %d", len(paths), len(paths2))
	}
}
