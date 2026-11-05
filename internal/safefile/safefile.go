// Package safefile reads and writes files the CLI does not fully trust the
// location of, without ever following a symlink at the final path component.
//
// A cloned repo is untrusted: it can ship .env, .dev.vars or
// .keyto/project.json as a symlink to any file the user can write
// (~/.ssh/authorized_keys, a shell rc), and a dotfile manager may have made
// ~/.npmrc a link. So the destination must be absent or a regular file, the
// new content goes to a fresh O_EXCL 0600 temp file in the same directory, and
// a rename puts it in place (a rename replaces the directory entry, never a
// link's target). Used by `keyto env sync`, the ~/.npmrc package lane and the
// checkout marker.
package safefile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// ErrSymlink and ErrNotRegular are the refusals; match them with errors.Is.
var (
	ErrSymlink    = errors.New("it is a symlink; keyto never reads or writes through a link")
	ErrNotRegular = errors.New("it is not a regular file")
)

const (
	tempAttempts = 5
	tempInfix    = ".keyto-tmp-"
	tempSuffix   = ".local"
	tempRandLen  = 8 // bytes; 16 hex characters in the name
	parentPerm   = 0o755
	tempPerm     = 0o600
)

func refuse(path string, why error) error {
	return fmt.Errorf("refusing %s: %w — remove it and re-run", path, why)
}

// CheckRegularOrAbsent returns exists=false when nothing is at path, and an
// error naming the file (ErrSymlink / ErrNotRegular) when path is a symlink or
// not a regular file.
func CheckRegularOrAbsent(path string) (exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return true, refuse(path, ErrSymlink)
	}
	if !info.Mode().IsRegular() {
		return true, refuse(path, ErrNotRegular)
	}
	return true, nil
}

// ReadRegular reads path without following a symlink. ok=false when the file
// doesn't exist.
func ReadRegular(path string) (data []byte, ok bool, err error) {
	exists, err := CheckRegularOrAbsent(path)
	if err != nil || !exists {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

// Write puts content at path as a regular file with mode perm, atomically and
// without following a symlink at path. The parent directory is created if
// missing; the caller checks the parent itself when it can be planted too.
func Write(path string, content []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, parentPerm); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	if _, err := CheckRegularOrAbsent(path); err != nil {
		return err
	}
	removeStaleTemps(dir, filepath.Base(path))
	tmp, f, err := createTemp(dir, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := fillAndClose(f, content, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// createTemp opens a new 0600 file next to the destination with O_EXCL (and
// O_NOFOLLOW where available), so nothing pre-planted can be reused.
func createTemp(dir, base string) (string, *os.File, error) {
	var lastErr error
	for i := 0; i < tempAttempts; i++ {
		var b [tempRandLen]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		name := filepath.Join(dir, TempFileName(base, hex.EncodeToString(b[:])))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, tempPerm)
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

// TempFileName is "<base>.keyto-tmp-<hex>.local". A write killed before its
// rename leaves this file, possibly full of secrets, until the next write
// removes it, so the name must already be git-ignored: ".env.keyto-tmp-….local"
// matches the Next.js template's ".env*.local" and the Workers template's
// ".env.*", and ".dev.vars.keyto-tmp-….local" matches the Workers template's
// ".dev.vars.*". No loader picks it up: Next.js and Vite read only .env.local
// and .env.<mode>.local, wrangler only .dev.vars.<env>, and npm only .npmrc.
func TempFileName(base, hex string) string {
	return base + tempInfix + hex + tempSuffix
}

// removeStaleTemps deletes temp files an interrupted write left next to base
// (they may hold secrets). Only regular files with exactly createTemp's name
// shape go, today's or the "." + base + ".keyto-tmp-<hex>" one earlier builds
// used; a symlink or anything else is left alone. Best-effort.
func removeStaleTemps(dir, base string) {
	hexPart := fmt.Sprintf(`[0-9a-f]{%d}`, tempRandLen*2)
	pattern := regexp.MustCompile(`^(?:` + regexp.QuoteMeta(base+tempInfix) + hexPart + regexp.QuoteMeta(tempSuffix) +
		`|\.` + regexp.QuoteMeta(base+tempInfix) + hexPart + `)$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		// ReadDir reports the entry's own type (it doesn't follow links).
		if e.Type().IsRegular() && pattern.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func fillAndClose(f *os.File, content []byte, perm fs.FileMode) error {
	_, werr := f.Write(content)
	if werr == nil {
		// The umask can only narrow the mode; set it explicitly anyway.
		werr = f.Chmod(perm)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
