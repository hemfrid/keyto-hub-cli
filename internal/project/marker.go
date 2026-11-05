package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hemfrid/keyto-hub-cli/internal/safefile"
)

// Marker is the project descriptor written to <dir>/.keyto/project.json.
type Marker struct {
	Name   string `json:"name"`
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	HubURL string `json:"hub_url"`
}

const (
	keytoDir     = ".keyto"
	projectFile  = "project.json"
	keytoDirPerm = 0o755
	markerPerm   = 0o644 // not a secret; committed-adjacent metadata
)

// Read reads the marker from <dir>/.keyto/project.json.
// Returns (nil, nil) if the file does not exist — that means the directory is
// not a keyto project.  Returns a non-nil error only on I/O or JSON parse
// failures.
func Read(dir string) (*Marker, error) {
	path := filepath.Join(dir, keytoDir, projectFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Write writes the marker to <dir>/.keyto/project.json, creating the
// .keyto directory if needed. A cloned repo is untrusted, so neither .keyto
// nor project.json may be a symlink: the write refuses rather than follow one
// (cli#37), and goes through safefile (0600 temp, rename into place).
func Write(dir string, m *Marker) error {
	d := filepath.Join(dir, keytoDir)
	if err := ensureRealDir(d); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return safefile.Write(filepath.Join(d, projectFile), data, markerPerm)
}

// ensureRealDir creates d, or accepts it when it is already a real directory.
// A symlink at d is refused: renaming into it would land the marker in the
// link's target.
func ensureRealDir(d string) error {
	info, err := os.Lstat(d)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return os.Mkdir(d, keytoDirPerm)
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("refusing %s: %w — remove it and re-run", d, safefile.ErrSymlink)
	case !info.IsDir():
		return fmt.Errorf("refusing %s: it is not a directory — remove it and re-run", d)
	}
	return nil
}
