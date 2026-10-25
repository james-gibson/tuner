package tv

import (
	"errors"
	"fmt"
	"io/fs"
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
	if err := os.MkdirAll(expanded, 0o750); err != nil {
		return "", false, fmt.Errorf("create cable dir: %w", err)
	}

	content, err := GenerateTOML(p)
	if err != nil {
		return "", false, err
	}

	path := filepath.Join(expanded, ChannelFile(p.Name))

	// Check if file exists with same content. path is rooted at the operator's
	// configured cable directory and joined with a generated filename.
	existing, err := os.ReadFile(path) //nolint:gosec // G304: path is confined to the configured cable dir
	if err == nil && string(existing) == content {
		// File exists and content is identical - skip write
		return path, false, nil
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", false, fmt.Errorf("write channel: %w", err)
	}
	return path, true, nil
}

// RemoveChannel deletes a channel's TOML file from the cable directory.
// It reports whether a file was actually removed.
func RemoveChannel(cableDir, name string) (string, bool, error) {
	path := filepath.Join(expandHome(cableDir), ChannelFile(name))
	// path is confined to the configured cable directory by ChannelFile.
	if err := os.Remove(path); err != nil { //nolint:gosec // G304: path is confined to the configured cable dir
		if errors.Is(err, fs.ErrNotExist) {
			return path, false, nil
		}
		return "", false, fmt.Errorf("remove channel: %w", err)
	}
	return path, true, nil
}

// ChannelFile is the TOML file name for a channel.
//
// Channel names come from two places: built-in presets, and DNS-SD instance
// labels discovered on the network. A label is not a file name -- a lezz demo
// seed is called "cluster/alarm-a", which filepath.Join would turn into a
// subdirectory, and a label containing ".." would escape the cable directory
// entirely -- so every byte outside [A-Za-z0-9_-] is folded to '-'.
func ChannelFile(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, name)

	// Leading dots would create a hidden file, and a name made only of dots or
	// dashes would reduce to "" or "-".
	safe = strings.Trim(safe, ".-")
	if safe == "" {
		safe = "channel"
	}
	return fmt.Sprintf("ocd-%s.toml", safe)
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

	// tvPath comes from exec.LookPath; args are assembled from the operator's
	// config, not from untrusted input.
	cmd := exec.Command(tvPath, args...) //nolint:gosec // G204: binary resolved via LookPath, args from config
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
