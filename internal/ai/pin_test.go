package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePin() *Pin {
	return &Pin{
		SourceRepo:       "hemfrid/ai-capabilities",
		Tag:              "v0.2.0",
		SourceSHA:        "abc123",
		ManifestCommit:   "def456",
		FrameworkVersion: "1.0.0",
		FetchedAt:        "2026-06-12T10:00:00Z",
		InstallChannel:   "cli",
		InstallInputs: Inputs{
			TelemetryEndpoint: "https://hub.keytolabs.com/api/telemetry/events",
			BaseBranch:        "main",
		},
		Files: []PinnedFile{
			{Path: ".claude/agents/api-designer.md", SHA256: strings.Repeat("aa", 32)},
		},
	}
}

func TestPinRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := SavePin(root, samplePin()); err != nil {
		t.Fatalf("SavePin: %v", err)
	}
	got, err := LoadPin(root)
	if err != nil {
		t.Fatalf("LoadPin: %v", err)
	}
	if got == nil {
		t.Fatal("LoadPin returned nil for existing pin")
	}
	if got.Tag != "v0.2.0" || got.InstallChannel != "cli" || len(got.Files) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.InstallInputs.BaseBranch != "main" {
		t.Errorf("install_inputs lost: %+v", got.InstallInputs)
	}
}

func TestPinIsLineGreppable(t *testing.T) {
	// The ai-capabilities bot greps `^install_channel:` and the session-start
	// hook greps `^fetched_at:` / `^manifest_commit:` — top-level scalar keys
	// must each sit on their own line, two-space block indent.
	root := t.TempDir()
	if err := SavePin(root, samplePin()); err != nil {
		t.Fatalf("SavePin: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, PinPath))
	if err != nil {
		t.Fatalf("read pin: %v", err)
	}
	for _, key := range []string{"install_channel: cli", "manifest_commit: def456", "fetched_at:"} {
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, key) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pin not line-greppable for %q:\n%s", key, raw)
		}
	}
	if !strings.Contains(string(raw), "\n  telemetry_endpoint:") {
		t.Errorf("install_inputs must use two-space indent (bot parser contract):\n%s", raw)
	}
}

func TestLoadPinMissingReturnsNil(t *testing.T) {
	got, err := LoadPin(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPin on empty dir: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil pin, got %+v", got)
	}
}
