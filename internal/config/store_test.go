package config_test

import (
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/hemfrid/keyto-cli/internal/config"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	t.Setenv("KEYTO_HOME", t.TempDir())

	want := &config.Creds{
		Credential: "ghp_test_credential_abc123",
		HubURL:     "https://hub.keytolabs.com",
		UserEmail:  "alice@example.com",
		UserName:   "alice",
		ExpiresAt:  time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	}

	if err := config.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Credential != want.Credential {
		t.Errorf("Credential: got %q, want %q", got.Credential, want.Credential)
	}
	if got.HubURL != want.HubURL {
		t.Errorf("HubURL: got %q, want %q", got.HubURL, want.HubURL)
	}
	if got.UserEmail != want.UserEmail {
		t.Errorf("UserEmail: got %q, want %q", got.UserEmail, want.UserEmail)
	}
	if got.UserName != want.UserName {
		t.Errorf("UserName: got %q, want %q", got.UserName, want.UserName)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

func TestSave_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits not enforced on Windows")
	}
	t.Setenv("KEYTO_HOME", t.TempDir())

	creds := &config.Creds{
		Credential: "token",
		HubURL:     "https://hub.keytolabs.com",
		UserEmail:  "bob@example.com",
		UserName:   "bob",
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}

	if err := config.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Check credentials file is mode 0600.
	credFile := os.Getenv("KEYTO_HOME") + "/credentials"
	info, err := os.Stat(credFile)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file perm: got %04o, want 0600", perm)
	}

	// Check directory is mode 0700.
	dirInfo, err := os.Stat(os.Getenv("KEYTO_HOME"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("KEYTO_HOME dir perm: got %04o, want 0700", perm)
	}
}

func TestLoad_NoFile_ErrNotAuthed(t *testing.T) {
	t.Setenv("KEYTO_HOME", t.TempDir())

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected ErrNotAuthed, got nil")
	}
	if !errors.Is(err, config.ErrNotAuthed) {
		t.Errorf("expected errors.Is(err, ErrNotAuthed), got: %v", err)
	}
}

func TestLoad_OverPermissionedFile_Error(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits not enforced on Windows")
	}
	t.Setenv("KEYTO_HOME", t.TempDir())

	// Pre-create a credentials file with insecure 0644 permissions.
	credFile := os.Getenv("KEYTO_HOME") + "/credentials"
	if err := os.MkdirAll(os.Getenv("KEYTO_HOME"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(credFile, []byte(`{"credential":"x"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for over-permissioned file, got nil")
	}
}
