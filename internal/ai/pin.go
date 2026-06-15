// Package ai implements `keyto ai init|update|status` — installing and
// pull-updating the company AI-capabilities bundle in any git repo.
package ai

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// PinPath is the committed provenance record, shared with Hub-provisioned
// repos (written there by ai-capabilities setup.sh --bundle). The
// ai-capabilities bot greps top-level keys line-wise — keep this file flat
// YAML with two-space block indent.
const PinPath = ".keyto/skills-bundle.yaml"

type Pin struct {
	SourceRepo       string       `yaml:"source_repo"`
	Tag              string       `yaml:"tag"`
	SourceSHA        string       `yaml:"source_sha"`
	ManifestCommit   string       `yaml:"manifest_commit"`
	FrameworkVersion string       `yaml:"framework_version"`
	FetchedAt        string       `yaml:"fetched_at"`
	InstallChannel   string       `yaml:"install_channel"`
	InstallInputs    Inputs       `yaml:"install_inputs"`
	Files            []PinnedFile `yaml:"files"`
}

type Inputs struct {
	Stack             string `yaml:"stack"`
	Tracker           string `yaml:"tracker"`
	BusinessValue     string `yaml:"business_value"`
	TelemetryEndpoint string `yaml:"telemetry_endpoint"`
	BaseBranch        string `yaml:"base_branch"`
}

type PinnedFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// LoadPin returns nil (no error) when the pin file does not exist.
func LoadPin(repoRoot string) (*Pin, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, PinPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ai: read pin: %w", err)
	}
	var p Pin
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("ai: parse pin: %w", err)
	}
	return &p, nil
}

func SavePin(repoRoot string, p *Pin) error {
	dir := filepath.Join(repoRoot, ".keyto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ai: mkdir .keyto: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("# Version-pin / provenance record for the installed AI skills bundle.\n")
	buf.WriteString("# Written by `keyto ai init`; updated by `keyto ai update`. Committed.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return fmt.Errorf("ai: encode pin: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("ai: encode pin: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, PinPath), buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("ai: write pin: %w", err)
	}
	return nil
}
