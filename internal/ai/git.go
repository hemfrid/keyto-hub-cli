package ai

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitRoot resolves the repository root from any directory inside it.
func GitRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository — run from your project")
	}
	return strings.TrimSpace(string(out)), nil
}

// IsClean reports whether the working tree has no staged, unstaged, or
// untracked changes. init/update require a clean tree so the whole change
// lands as one reviewable diff.
func IsClean(root string) (bool, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) == 0, nil
}

// DefaultBranch resolves the repo's base branch: origin/HEAD when a remote
// exists, otherwise the first of main/master/develop that exists locally,
// otherwise "main". Used to substitute {{BASE_BRANCH}} in bundle hooks.
func DefaultBranch(root string) string {
	out, err := exec.Command("git", "-C", root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		ref := strings.TrimSpace(string(out)) // e.g. "origin/main"
		if i := strings.IndexByte(ref, '/'); i >= 0 && ref[i+1:] != "" {
			return ref[i+1:]
		}
	}
	for _, b := range []string{"main", "master", "develop"} {
		if exec.Command("git", "-C", root, "show-ref", "--verify", "--quiet", "refs/heads/"+b).Run() == nil {
			return b
		}
	}
	return "main"
}
