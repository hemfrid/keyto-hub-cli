package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Marker is the project descriptor written to <dir>/.keyto/project.json.
type Marker struct {
	Name   string `json:"name"`
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	HubURL string `json:"hub_url"`
}

const (
	keytoDir    = ".keyto"
	projectFile = "project.json"
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
// .keyto directory if needed.
func Write(dir string, m *Marker) error {
	d := filepath.Join(dir, keytoDir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, projectFile), data, 0o644)
}
