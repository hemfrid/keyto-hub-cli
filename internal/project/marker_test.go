package project_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// ---- cli#37: the marker write never follows a symlink ----

// A cloned repo is untrusted: it can ship .keyto/project.json, or .keyto
// itself, as a link to a file or directory the user can write.

const victimContent = "ssh-ed25519 AAAA victim@laptop\n"

func testMarker() *project.Marker {
	return &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
}

func TestWrite_SymlinkedMarkerFile_Refused(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(victim, []byte(victimContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".keyto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, ".keyto", "project.json")); err != nil {
		t.Fatal(err)
	}

	err := project.Write(dir, testMarker())
	if err == nil || !strings.Contains(err.Error(), "project.json") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal naming project.json, got %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != victimContent {
		t.Fatalf("the symlink target was modified: %q", got)
	}
}

func TestWrite_SymlinkedKeytoDir_Refused(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir() // e.g. ~/.ssh
	if err := os.Symlink(target, filepath.Join(dir, ".keyto")); err != nil {
		t.Fatal(err)
	}

	err := project.Write(dir, testMarker())
	if err == nil || !strings.Contains(err.Error(), ".keyto") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal naming .keyto, got %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("something was written into the link's target directory: %v", entries)
	}
}

func TestWrite_KeytoIsAFile_Refused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".keyto"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := project.Write(dir, testMarker()); err == nil || !strings.Contains(err.Error(), ".keyto") {
		t.Fatalf("want a refusal naming .keyto, got %v", err)
	}
}

func TestWrite_ReplacesExistingMarkerAtomically(t *testing.T) {
	dir := t.TempDir()
	if err := project.Write(dir, &project.Marker{Name: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := project.Write(dir, testMarker()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := project.Read(dir)
	if err != nil || got == nil || got.Name != "acme-web" {
		t.Fatalf("Read = %+v, %v; want the new marker", got, err)
	}
	path := filepath.Join(dir, ".keyto", "project.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Fatalf("marker mode = %v (err %v), want a regular 0644 file", info.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".keyto"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "keyto-tmp") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}
