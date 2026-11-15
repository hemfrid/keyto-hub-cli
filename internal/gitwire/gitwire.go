package gitwire

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/ghname"
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
//
// The marker is a file in the repo, so it is not trusted:
//   - its org and repo must be GitHub names (ghname), since they go into the
//     remote URL;
//   - its hub_url must be the same origin (scheme, host, port) as hubURL, the
//     Hub the user is signed in to. The remote and the credential helper are
//     then built from hubURL, never from the marker's text, so a marker that
//     names another host cannot send this checkout's pushes there.
//
// Otherwise nothing is run.
func Wire(run Runner, dir string, m *project.Marker, hubURL, email, name string) error {
	signedIn, ok := origin(hubURL)
	if !ok {
		return errors.New("not signed in to a Hub, so the git remote was left unchanged; run `keyto auth`")
	}
	if markerOrigin, ok := origin(m.HubURL); !ok || markerOrigin != signedIn {
		return errors.New("this checkout's .keyto/project.json names a different Hub than the one you are signed in to, so the git remote and credential helper were left unchanged")
	}
	if !ghname.ValidOwner(m.Org) || !ghname.ValidRepo(m.Repo) {
		return errors.New("the project marker's org or repo is not a GitHub name, so the git remote was left unchanged (check .keyto/project.json)")
	}
	remoteURL := fmt.Sprintf("%s/git/%s/%s.git", strings.TrimRight(hubURL, "/"), m.Org, m.Repo)
	credKey := fmt.Sprintf("credential.%s.helper", hubURL)

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

// origin is "scheme://host:port" for an http(s) URL with no user info, with the
// scheme and host lowercased and the default port filled in, and false for
// anything else. Two URLs name the same Hub when their origins are equal.
func origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}
