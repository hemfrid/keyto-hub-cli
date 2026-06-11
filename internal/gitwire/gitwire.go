package gitwire

import (
	"fmt"
	"os/exec"

	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// Runner is a function that executes a git sub-command in the given directory.
// It is injectable so that unit tests can record calls without spawning a real
// git process.
type Runner func(dir string, args ...string) error

// Wire configures a cloned repository to work with the Keyto Hub:
//
//  1. Points the "origin" remote at the Hub's git proxy.
//  2. Makes `!keyto credential` the SOLE credential helper for the Hub host.
//  3. Sets the committer email and name from the authenticated user's profile.
//
// Step 2 first writes an empty helper value, which resets any inherited helpers
// (e.g. a global `credential.helper osxkeychain`) for the Hub host, then adds
// the keyto helper. Without the reset, a generic helper runs first and caches
// the Hub credential — which would mask revocation/rotation at the Hub (the
// stale cached copy keeps working). This mirrors how `gh` scopes itself to
// github.com.
//
// The reset uses `--replace-all` rather than a plain `config` set: after the
// first wiring the key already holds two values (the empty reset and the keyto
// helper), and a plain set refuses to overwrite a multi-valued key ("cannot
// overwrite multiple values with a single value"). `--replace-all` collapses
// any existing values to the single empty one, so re-wiring an already-wired
// repo — e.g. resuming a project via `keyto start` — stays idempotent and also
// repairs repos left in the multi-valued state by older versions.
//
// The first error encountered is returned immediately; subsequent steps are
// not attempted.
func Wire(run Runner, dir string, m *project.Marker, email, name string) error {
	remoteURL := fmt.Sprintf("%s/git/%s/%s.git", m.HubURL, m.Org, m.Repo)
	credKey := fmt.Sprintf("credential.%s.helper", m.HubURL)

	steps := [][]string{
		{"remote", "set-url", "origin", remoteURL},
		{"config", "--replace-all", credKey, ""},
		{"config", "--add", credKey, "!keyto credential"},
		{"config", "user.email", email},
		{"config", "user.name", name},
	}

	for _, args := range steps {
		if err := run(dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// RealRunner is the production Runner that executes git sub-commands in dir.
func RealRunner(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
		return fmt.Errorf("git %v: %w", args, err)
	}
	return nil
}
