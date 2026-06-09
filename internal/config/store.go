// Package config manages the ~/.keyto credential store.
// The store location can be overridden by setting KEYTO_HOME (for tests and
// power users); it falls back to $HOME/.keyto.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Creds holds the identity and access credential persisted after a successful
// `keyto auth` login.
type Creds struct {
	Credential string    `json:"credential"`
	HubURL     string    `json:"hub_url"`
	UserEmail  string    `json:"user_email"`
	UserName   string    `json:"user_name"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// ErrNotAuthed is returned by Load when no credentials file exists.
var ErrNotAuthed = errors.New("not authenticated: run 'keyto auth'")

// dir returns the path to the keyto config directory.
// It honours KEYTO_HOME if set; otherwise it falls back to ~/.keyto.
func dir() string {
	if h := os.Getenv("KEYTO_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".keyto")
}

// credPath returns the full path to the credentials file.
func credPath() string {
	return filepath.Join(dir(), "credentials")
}

// Save persists c to disk.  The config directory is created with mode 0700 and
// the credentials file is written (and explicitly chmod'd) with mode 0600 so
// that secrets are never group- or world-readable regardless of the process
// umask.
func Save(c *Creds) error {
	d := dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("config: create directory %s: %w", d, err)
	}
	// Explicit chmod guards against a pre-existing directory with a looser
	// mode (e.g. t.TempDir() creates 0755; we always tighten to 0700).
	if err := os.Chmod(d, 0o700); err != nil {
		return fmt.Errorf("config: chmod directory %s: %w", d, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal credentials: %w", err)
	}

	path := credPath()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write credentials: %w", err)
	}

	// Explicit chmod after write guards against a permissive umask.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: chmod credentials: %w", err)
	}

	return nil
}

// Load reads and returns the saved credentials.
// It returns ErrNotAuthed when the credentials file does not exist.
// On non-Windows platforms it refuses to read a file that is group- or
// world-readable, returning an error that describes the offending mode.
func Load() (*Creds, error) {
	path := credPath()

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotAuthed
		}
		return nil, fmt.Errorf("config: stat %s: %w", path, err)
	}

	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf(
				"credentials file %s has insecure permissions (expect 0600, got %04o)",
				path, perm,
			)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var c Creds
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse credentials: %w", err)
	}

	return &c, nil
}
