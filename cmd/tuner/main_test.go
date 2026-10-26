package main

import (
	"io"
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
