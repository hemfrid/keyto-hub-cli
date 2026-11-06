package safefile_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/safefile"
)

// symlinkOrSkip makes newname a link to oldname. It skips the test on
// Windows, where creating a link needs Developer Mode or admin rights (CI runs
// on Linux only).
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs Developer Mode or admin rights on Windows")
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
}

const victimContent = "ssh-ed25519 AAAA victim@laptop\n"

func plantVictim(t *testing.T) string {
	t.Helper()
	victim := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(victim, []byte(victimContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return victim
}

func assertVictimUntouched(t *testing.T, victim string) {
	t.Helper()
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != victimContent {
		t.Fatalf("the symlink target was modified: %q (err %v)", got, err)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "keyto-tmp") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}

func assertRegularWithMode(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != perm {
		t.Fatalf("%s mode = %v, want a regular %v file", path, info.Mode(), perm)
	}
}

func TestWrite_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", ".npmrc")
	if err := safefile.Write(path, []byte("a=1\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "a=1\n" {
		t.Fatalf("content = %q", got)
	}
	assertRegularWithMode(t, path, 0o600)
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestWrite_ReplacesRegularFileAndSetsPerm(t *testing.T) {
	for _, perm := range []os.FileMode{0o600, 0o644} {
		dir := t.TempDir()
		path := filepath.Join(dir, "f")
		if err := os.WriteFile(path, []byte("old\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := safefile.Write(path, []byte("new\n"), perm); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got, _ := os.ReadFile(path); string(got) != "new\n" {
			t.Fatalf("content = %q", got)
		}
		assertRegularWithMode(t, path, perm)
		assertNoTempFiles(t, dir)
	}
}

func TestWrite_SymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	victim := plantVictim(t)
	path := filepath.Join(dir, ".npmrc")
	symlinkOrSkip(t, victim, path)
	err := safefile.Write(path, []byte("token=x\n"), 0o600)
	if !errors.Is(err, safefile.ErrSymlink) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want an ErrSymlink refusal naming %s, got %v", path, err)
	}
	assertVictimUntouched(t, victim)
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link itself must be left in place (err %v)", err)
	}
	assertNoTempFiles(t, dir)
}

func TestWrite_DirectoryRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	err := safefile.Write(path, []byte("x"), 0o600)
	if !errors.Is(err, safefile.ErrNotRegular) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("want an ErrNotRegular refusal, got %v", err)
	}
}

func TestReadRegular(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := safefile.ReadRegular(filepath.Join(dir, "absent")); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v, want false, nil", ok, err)
	}
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, ok, err := safefile.ReadRegular(path); !ok || err != nil || string(data) != "hi" {
		t.Fatalf("regular: %q ok=%v err=%v", data, ok, err)
	}
	link := filepath.Join(dir, "link")
	symlinkOrSkip(t, plantVictim(t), link)
	if data, _, err := safefile.ReadRegular(link); !errors.Is(err, safefile.ErrSymlink) || len(data) != 0 {
		t.Fatalf("symlink: want ErrSymlink and no data, got %q, %v", data, err)
	}
}

func TestWrite_RemovesStaleTempsOfTheSameBaseOnly(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, safefile.TempFileName(".npmrc", "0123456789abcdef"))
	other := filepath.Join(dir, safefile.TempFileName(".other", "0123456789abcdef"))
	for _, p := range []string{stale, other} {
		if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := safefile.Write(filepath.Join(dir, ".npmrc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale temp was not removed (err %v)", err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatalf("another file's temp was removed: %v", err)
	}
}
