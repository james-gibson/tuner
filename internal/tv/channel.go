package tv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const channelTemplate = `[metadata]
name = "{{.Name}}"
description = "{{.Description}}"

[source]
command = 'sh -c "{{.Command | escapeDoubleQuotes}}"'
watch = {{printf "%.1f" .Watch}}

[preview]
command = 'sh -c "{{.Preview | escapeDoubleQuotes}}"'

[ui]
layout = "portrait"

[ui.preview_panel]
size = 40
`

// GenerateTOML renders a channel to TOML string.
func GenerateTOML(p Preset) (string, error) {
	funcMap := template.FuncMap{
		"escapeDoubleQuotes": func(s string) string {
			return strings.ReplaceAll(s, `"`, `\"`)
		},
	}
	tmpl, err := template.New("channel").Funcs(funcMap).Parse(channelTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// WriteChannel writes a TOML channel file to the cable directory.
// If the file exists and content is identical, it skips writing.
func WriteChannel(cableDir string, p Preset) (string, bool, error) {
	expanded := expandHome(cableDir)
	if err := os.MkdirAll(expanded, 0755); err != nil {
		return "", false, fmt.Errorf("create cable dir: %w", err)
	}

	content, err := GenerateTOML(p)
	if err != nil {
		return "", false, err
	}

	path := filepath.Join(expanded, fmt.Sprintf("ocd-%s.toml", p.Name))

	// Check if file exists with same content
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		// File exists and content is identical - skip write
		return path, false, nil
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", false, fmt.Errorf("write channel: %w", err)
	}
	return path, true, nil
}

// WriteAllPresets writes all built-in presets to the cable directory.
// Returns paths and count of files actually written (vs skipped).
func WriteAllPresets(cableDir string) ([]string, int, error) {
	var paths []string
	written := 0
	for _, p := range BuiltinPresets() {
		path, didWrite, err := WriteChannel(cableDir, p)
		if err != nil {
			return paths, written, err
		}
		paths = append(paths, path)
		if didWrite {
			written++
		}
	}
	return paths, written, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// LaunchTelevision opens Television with the specified channel.
// channelName is the name of the channel (e.g., "dns", "ntp", "ping").
// If channelName is empty, Television opens with the channel picker.
// If cableDir is non-empty, Television uses that directory exclusively via --cable-dir.
// Returns an error if television is not installed.
func LaunchTelevision(channelName, cableDir string) error {
	// Check if television is available
	tvPath, err := exec.LookPath("television")
	if err != nil {
		tvPath, err = exec.LookPath("tv")
		if err != nil {
			return fmt.Errorf("television not found in PATH (tried 'television' and 'tv')")
		}
	}

	// Build command args
	var args []string

	// If isolated cable directory specified, use --cable-dir to prevent mixing
	// with TV's default channels
	if cableDir != "" {
		expanded := expandHome(cableDir)
		args = append(args, "--cable-dir", expanded)
	}

	// TV accepts channel name as positional argument
	// When launching a specific channel, use --no-remote to prevent switching
	// to TV's built-in channels (keeps tuner channels isolated)
	if channelName != "" {
		args = append(args, "--no-remote", channelName)
	}

	cmd := exec.Command(tvPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
