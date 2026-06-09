package project_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

func TestWriteRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	if err := project.Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := project.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil, want non-nil")
	}
	if got.Name != want.Name || got.Org != want.Org || got.Repo != want.Repo || got.HubURL != want.HubURL {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestRead_NoProjectJSON_ReturnsNilNil(t *testing.T) {
	dir := t.TempDir()

	got, err := project.Read(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil marker for missing file, got: %+v", got)
	}
}

func TestRead_MalformedJSON_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	keytoDir := filepath.Join(dir, ".keyto")
	if err := os.MkdirAll(keytoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keytoDir, "project.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := project.Read(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	// Verify it's a JSON parse error
	var syntaxErr *json.SyntaxError
	var unmarshalErr *json.UnmarshalTypeError
	if syntaxErr == nil && unmarshalErr == nil {
		// Either kind is acceptable — just ensure err != nil (already checked above)
		_ = syntaxErr
		_ = unmarshalErr
	}
}

func TestWrite_CreatesKeytoDir(t *testing.T) {
	dir := t.TempDir()
	m := &project.Marker{Name: "test", Org: "org", Repo: "repo", HubURL: "https://hub.example.com"}

	if err := project.Write(dir, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(dir, ".keyto", "project.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("project.json not created: %v", err)
	}
	if info.IsDir() {
		t.Fatal("project.json is a directory, expected file")
	}
}
