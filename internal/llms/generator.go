package llms

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GherkinScenario is a parsed scenario from a .feature file.
type GherkinScenario struct {
	Feature  string
	Name     string
	Tags     []string
	FilePath string
}

// ParseFeatures walks a directory and extracts scenario names from .feature files.
func ParseFeatures(dir string) ([]GherkinScenario, error) {
	var scenarios []GherkinScenario
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".feature") {
			return err
		}
		parsed, err := parseFeatureFile(path)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		scenarios = append(scenarios, parsed...)
		return nil
	})
	return scenarios, err
}

// GenerateLLMSTxt builds a markdown llms.txt from parsed features and runtime info.
func GenerateLLMSTxt(scenarios []GherkinScenario, info RuntimeInfo) string {
	var b strings.Builder

	b.WriteString("# Tuner\n\n")
	b.WriteString("> Passive observer for smoke-alarm services. Displays alerts via Television.\n\n")

	b.WriteString("## Capabilities\n")
	for _, cap := range info.Capabilities {
		b.WriteString(fmt.Sprintf("- %s\n", cap))
	}
	b.WriteString("\n")

	b.WriteString("## Connection\n")
	b.WriteString(fmt.Sprintf("- Mode: %s\n", info.Mode))
	b.WriteString(fmt.Sprintf("- Health: http://%s/healthz\n", info.HealthAddr))
	b.WriteString(fmt.Sprintf("- Broadcast: http://%s/channels\n", info.BroadcastAddr))
	b.WriteString("\n")

	b.WriteString("## Channels\n")
	for _, ch := range info.Channels {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", ch.Name, ch.Description))
	}
	b.WriteString("\n")

	if len(scenarios) > 0 {
		b.WriteString("## Features\n")
		currentFeature := ""
		for _, s := range scenarios {
			if s.Feature != currentFeature {
				b.WriteString(fmt.Sprintf("\n### %s\n", s.Feature))
				currentFeature = s.Feature
			}
			b.WriteString(fmt.Sprintf("- %s\n", s.Name))
		}
		b.WriteString("\n")
	}

	b.WriteString("## See Also\n")
	b.WriteString("- features/tuner-broadcasting.feature\n")
	b.WriteString("- features/tuner-receiving.feature\n")

	return b.String()
}

// RuntimeInfo provides dynamic context for llms.txt generation.
type RuntimeInfo struct {
	Mode          string
	HealthAddr    string
	BroadcastAddr string
	Capabilities  []string
	Channels      []ChannelInfo
}

// ChannelInfo describes a channel for llms.txt.
type ChannelInfo struct {
	Name        string
	Description string
}

func parseFeatureFile(path string) ([]GherkinScenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		scenarios []GherkinScenario
		feature   string
		tags      []string
	)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "Feature:") {
			feature = strings.TrimPrefix(line, "Feature:")
			feature = strings.TrimSpace(feature)
			continue
		}

		if strings.HasPrefix(line, "@") {
			tags = parseTags(line)
			continue
		}

		if strings.HasPrefix(line, "Scenario:") || strings.HasPrefix(line, "Scenario Outline:") {
			name := line
			name = strings.TrimPrefix(name, "Scenario Outline:")
			name = strings.TrimPrefix(name, "Scenario:")
			name = strings.TrimSpace(name)
			scenarios = append(scenarios, GherkinScenario{
				Feature:  feature,
				Name:     name,
				Tags:     tags,
				FilePath: path,
			})
			tags = nil
			continue
		}
	}

	return scenarios, scanner.Err()
}

func parseTags(line string) []string {
	parts := strings.Fields(line)
	var tags []string
	for _, p := range parts {
		if strings.HasPrefix(p, "@") {
			tags = append(tags, p)
		}
	}
	return tags
}
