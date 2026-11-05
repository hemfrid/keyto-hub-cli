package envsync

import (
	"fmt"

	"github.com/hemfrid/keyto-hub-cli/internal/safefile"
)

// env sync writes secrets and reads the old file back, so both go through
// internal/safefile, which never follows a symlink (see its package doc).

const envFilePerm = 0o600

// readRegularFile reads path without following a symlink. ok=false when the
// file doesn't exist.
func readRegularFile(path string) (data []byte, ok bool, err error) {
	data, ok, err = safefile.ReadRegular(path)
	if err != nil {
		return nil, false, fmt.Errorf("env sync: %w", err)
	}
	return data, ok, nil
}

// writeEnvFile writes content to path as a 0600 regular file, atomically and
// without following a symlink at path.
func writeEnvFile(path, content string) error {
	if err := safefile.Write(path, []byte(content), envFilePerm); err != nil {
		return fmt.Errorf("env sync: %w", err)
	}
	return nil
}
