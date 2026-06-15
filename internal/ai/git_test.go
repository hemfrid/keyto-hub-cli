package ai

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo creates a git repo with one commit and returns its root.
func initTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func TestGitRoot(t *testing.T) {
	root := initTestRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := GitRoot(sub)
	if err != nil {
		t.Fatalf("GitRoot: %v", err)
	}
	// macOS tmpdirs traverse /private symlinks — compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("GitRoot = %q, want %q", gotResolved, wantResolved)
	}
}

func TestGitRootOutsideRepo(t *testing.T) {
	if _, err := GitRoot(t.TempDir()); err == nil {
		t.Error("expected error outside a git repo")
	}
}

func TestIsClean(t *testing.T) {
	root := initTestRepo(t)
	clean, err := IsClean(root)
	if err != nil || !clean {
		t.Fatalf("fresh repo should be clean: clean=%v err=%v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err = IsClean(root)
	if err != nil || clean {
		t.Fatalf("repo with untracked file should be dirty: clean=%v err=%v", clean, err)
	}
}

func TestDefaultBranchFallsBackToLocalHeads(t *testing.T) {
	root := initTestRepo(t) // branch "main", no remote
	if got := DefaultBranch(root); got != "main" {
		t.Errorf("DefaultBranch = %q, want main", got)
	}
}

func gitCmd(root string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", root}, args...)...)
}
