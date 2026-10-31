package envsync

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// A cloned repo is untrusted: it can ship .env or .dev.vars as a symlink to
// any file the user can write (~/.ssh/authorized_keys, a shell rc). The sync
// writes secrets there and reads the old file back, so it never follows a
// link: the destination must be absent or a regular file, the new content
// goes to a fresh O_EXCL temp file in the same directory, and a rename puts it
// in place (a rename replaces the directory entry, never a link's target).

const tempAttempts = 5

// checkRegularOrAbsent returns exists=false when nothing is at path, and an
// error naming the file when path is a symlink or not a regular file.
func checkRegularOrAbsent(path string) (exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("env sync: inspect %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return true, fmt.Errorf("env sync: refusing %s: it is a symlink; env sync writes secrets and never follows links — remove it and re-run", path)
	}
	if !info.Mode().IsRegular() {
		return true, fmt.Errorf("env sync: refusing %s: it is not a regular file — remove it and re-run", path)
	}
	return true, nil
}

// readRegularFile reads path without following a symlink. ok=false when the
// file doesn't exist.
func readRegularFile(path string) (data []byte, ok bool, err error) {
	exists, err := checkRegularOrAbsent(path)
	if err != nil || !exists {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, false, fmt.Errorf("env sync: open %s: %w", path, err)
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("env sync: read %s: %w", path, err)
	}
	return data, true, nil
}

// writeEnvFile writes content to path as a 0600 regular file, atomically and
// without following a symlink at path.
func writeEnvFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	if _, err := checkRegularOrAbsent(path); err != nil {
		return err
	}
	tmp, f, err := createTemp(dir, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("env sync: write %s: %w", path, err)
	}
	if err := fillAndClose(f, content); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("env sync: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("env sync: write %s: %w", path, err)
	}
	return nil
}

// createTemp opens a new 0600 file next to the destination with O_EXCL (and
// O_NOFOLLOW where available), so nothing pre-planted can be reused.
func createTemp(dir, base string) (string, *os.File, error) {
	var lastErr error
	for i := 0; i < tempAttempts; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		name := filepath.Join(dir, "."+base+".keyto-tmp-"+hex.EncodeToString(b[:]))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
		if err == nil {
			return name, f, nil
		}
		lastErr = err
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	return "", nil, lastErr
}

func fillAndClose(f *os.File, content string) error {
	_, werr := f.WriteString(content)
	if werr == nil {
		// The umask can only narrow the mode; set it explicitly anyway.
		werr = f.Chmod(0o600)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
