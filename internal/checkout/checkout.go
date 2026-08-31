// Package checkout implements the `keyto checkout` command: project resolution,
// clone via the Hub git proxy, and git wiring.
package checkout

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// Deps holds all injectable dependencies for Run.  Each field is a function or
// value so that tests can substitute fakes without any real network/git/stdin.
type Deps struct {
	// Creds holds the authenticated user's credentials. nil means not authed.
	Creds *config.Creds

	// List fetches the projects the authenticated user is a member of.
	List func(ctx context.Context) ([]hub.Project, error)

	// Clone clones the repository at repoURL into dir.
	Clone func(repoURL, dir string) error

	// Wire wires the git remote, credential helper, and identity in dir.
	Wire func(dir string, m *project.Marker, email, name string) error

	// ReadMarker reads the .keyto/project.json marker from dir.
	// Returns (nil, nil) if the directory is not a keyto project.
	ReadMarker func(dir string) (*project.Marker, error)

	// WriteMarker writes the .keyto/project.json marker to dir.
	WriteMarker func(dir string, m *project.Marker) error

	// OriginURL returns the URL of the "origin" remote of the git repository
	// at dir. An error (or a nil func) means dir is not a git repository or
	// has no origin — adoption of an existing checkout is then skipped.
	OriginURL func(dir string) (string, error)

	// Toplevel returns the root of the git working tree containing dir
	// (`git rev-parse --show-toplevel`). Adoption is only offered when the
	// cwd IS the toplevel — from a subdirectory the marker would land inside
	// the tracked tree instead of the repo root. An error (or a nil func)
	// skips adoption, same as OriginURL.
	Toplevel func(dir string) (string, error)

	// Cwd is the current working directory (injected so tests don't depend on os.Getwd).
	Cwd string

	// In is the reader for interactive prompts (injected; production = os.Stdin).
	In io.Reader

	// Out is the writer for prompt output and status messages (injected; production = os.Stdout).
	Out io.Writer
}

// session carries the mutable state for a single Run invocation.
// The bufio.Reader wraps Deps.In and is shared across all prompt reads so that
// each readLine call advances the same underlying position (not re-buffering
// from the same io.Reader independently).
type session struct {
	Deps
	in *bufio.Reader
}

// Run implements `keyto checkout [project]`. On success it returns the resolved
// project directory (the clone dir, or the cwd when re-wiring in place) so the
// caller can cd into it under shell integration. The returned dir is empty when
// no project was resolved (e.g. an empty project list).
//
// Resolution order:
//  1. If Creds is nil → error asking the user to run `keyto auth`.
//  2. If projectArg is set:
//     - cwd is already that project → re-wire in place.
//     - otherwise fetch List, find by name, then: cwd's git origin matches the
//     project → offer to adopt in place (marker + wire, no clone); else
//     prompt for dir, clone, write marker, wire.
//  3. If projectArg is empty:
//     - cwd has a marker → prompt "Work in <name>? [Y/n]"; yes → re-wire; no → picker.
//     - otherwise → picker (numbered list from List); empty list → message and
//     return. A selection matching cwd's git origin offers adoption as above.
func Run(ctx context.Context, projectArg string, d Deps) (string, error) {
	if d.Creds == nil {
		return "", fmt.Errorf("not authenticated — run `keyto auth`")
	}

	s := &session{Deps: d, in: bufio.NewReader(d.In)}
	if projectArg != "" {
		return s.runWithArg(ctx, projectArg)
	}
	return s.runInteractive(ctx)
}

// runWithArg handles the case where the user specified a project name explicitly.
func (s *session) runWithArg(ctx context.Context, name string) (string, error) {
	// Check if cwd is already that project.
	m, err := s.ReadMarker(s.Cwd)
	if err != nil {
		return "", fmt.Errorf("read project marker: %w", err)
	}
	if m != nil && m.Name == name {
		// Re-wire in place.
		if err := s.Wire(s.Cwd, m, s.Creds.UserEmail, s.Creds.UserName); err != nil {
			return "", err
		}
		fmt.Fprintln(s.Out, "ready")
		return s.Cwd, nil
	}

	// Fetch project list and find the named project.
	projects, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	proj, found := findProject(projects, name)
	if !found {
		return "", fmt.Errorf("project %q not found or you are not a member", name)
	}

	if dir, adopted, err := s.maybeAdopt(proj); err != nil || adopted {
		return dir, err
	}

	// Prompt for checkout directory.
	dir, err := s.promptDir(proj.Name)
	if err != nil {
		return "", err
	}

	return s.cloneAndWire(proj, dir)
}

// runInteractive handles the case where no project name was given.
func (s *session) runInteractive(ctx context.Context) (string, error) {
	// Check if cwd has a marker.
	m, err := s.ReadMarker(s.Cwd)
	if err != nil {
		return "", fmt.Errorf("read project marker: %w", err)
	}

	if m != nil {
		// Prompt the user whether to work in the current project.
		fmt.Fprintf(s.Out, "Work in %s? [Y/n] ", m.Name)
		answer, err := s.readLine()
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read input: %w", err)
		}
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" || answer == "yes" {
			if err := s.Wire(s.Cwd, m, s.Creds.UserEmail, s.Creds.UserName); err != nil {
				return "", err
			}
			fmt.Fprintln(s.Out, "ready")
			return s.Cwd, nil
		}
		// Fall through to picker.
	}

	return s.runPicker(ctx)
}

// runPicker shows a numbered list of projects and lets the user pick one.
func (s *session) runPicker(ctx context.Context) (string, error) {
	projects, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		fmt.Fprintln(s.Out, "No projects found. Ask a project owner to add you as a member.")
		return "", nil
	}

	fmt.Fprintln(s.Out, "Your projects:")
	for i, p := range projects {
		fmt.Fprintf(s.Out, "  %d. %s (%s)\n", i+1, p.Name, p.Role)
	}
	fmt.Fprintf(s.Out, "Select a project [1-%d]: ", len(projects))

	line, err := s.readLine()
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read input: %w", err)
	}
	line = strings.TrimSpace(line)

	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(projects) {
		return "", fmt.Errorf("invalid selection %q: enter a number between 1 and %d", line, len(projects))
	}
	proj := projects[idx-1]

	if dir, adopted, err := s.maybeAdopt(proj); err != nil || adopted {
		return dir, err
	}

	dir, err := s.promptDir(proj.Name)
	if err != nil {
		return "", err
	}

	return s.cloneAndWire(proj, dir)
}

// maybeAdopt handles the case where the cwd is already a plain git checkout of
// the selected project (cloned outside keyto, so no marker yet): instead of
// cloning a duplicate, offer to adopt it in place. Adoption never touches the
// working tree — it only writes the .keyto marker and re-wires git config
// (origin → Hub proxy, credential helper, identity). It reports adopted=false
// when the cwd is not a matching checkout root or the user declines.
func (s *session) maybeAdopt(proj hub.Project) (dir string, adopted bool, err error) {
	if s.OriginURL == nil || s.Toplevel == nil {
		return "", false, nil
	}
	origin, oerr := s.OriginURL(s.Cwd)
	if oerr != nil || !originMatches(origin, proj.Org, proj.Repo) {
		return "", false, nil
	}
	top, terr := s.Toplevel(s.Cwd)
	if terr != nil || !samePath(top, s.Cwd) {
		return "", false, nil
	}

	fmt.Fprintf(s.Out, "This folder is already a checkout of %s/%s. Use it? [Y/n] ", proj.Org, proj.Repo)
	answer, rerr := s.readLine()
	if rerr != nil && rerr != io.EOF {
		return "", false, fmt.Errorf("read input: %w", rerr)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return "", false, nil
	}

	m := &project.Marker{
		Name:   proj.Name,
		Org:    proj.Org,
		Repo:   proj.Repo,
		HubURL: s.Creds.HubURL,
	}
	if err := s.WriteMarker(s.Cwd, m); err != nil {
		return "", false, fmt.Errorf("write project marker: %w", err)
	}
	if err := s.Wire(s.Cwd, m, s.Creds.UserEmail, s.Creds.UserName); err != nil {
		return "", false, err
	}
	fmt.Fprintf(s.Out, "Adopted existing checkout of %s in %s\n", proj.Name, s.Cwd)
	return s.Cwd, true, nil
}

// samePath reports whether two paths refer to the same directory, resolving
// symlinks when possible (e.g. macOS /tmp → /private/tmp) and comparing
// case-insensitively on darwin, whose default filesystem is case-insensitive.
func samePath(a, b string) bool {
	na, nb := normalizePath(a), normalizePath(b)
	if na == nb {
		return true
	}
	if runtime.GOOS == "darwin" {
		return strings.EqualFold(na, nb)
	}
	return false
}

// normalizePath cleans p and resolves symlinks; if resolution fails (path does
// not exist), the cleaned path is used as-is.
func normalizePath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

// originMatches reports whether a git remote URL points at the given org/repo.
// It accepts the Hub proxy form (https://hub/git/org/repo.git), a plain HTTPS
// remote (https://host/org/repo[.git]), and the SSH form (git@host:org/repo[.git]).
func originMatches(origin, org, repo string) bool {
	u := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(origin), "/"), ".git")
	tail := org + "/" + repo
	return strings.HasSuffix(u, "/"+tail) || strings.HasSuffix(u, ":"+tail)
}

// cloneAndWire clones proj into dir, writes the marker, and wires git. It
// returns the checkout directory on success.
func (s *session) cloneAndWire(proj hub.Project, dir string) (string, error) {
	repoURL := remoteURL(proj, s.Creds.HubURL)

	if err := s.Clone(repoURL, dir); err != nil {
		return "", fmt.Errorf("clone %s: %w", repoURL, err)
	}

	m := &project.Marker{
		Name:   proj.Name,
		Org:    proj.Org,
		Repo:   proj.Repo,
		HubURL: s.Creds.HubURL,
	}
	if err := s.WriteMarker(dir, m); err != nil {
		return "", fmt.Errorf("write project marker: %w", err)
	}

	if err := s.Wire(dir, m, s.Creds.UserEmail, s.Creds.UserName); err != nil {
		return "", err
	}
	fmt.Fprintf(s.Out, "Cloned %s into %s\n", proj.Name, dir)
	return dir, nil
}

// remoteURL builds the Hub git proxy URL for a project.
func remoteURL(p hub.Project, hubURL string) string {
	return fmt.Sprintf("%s/git/%s/%s.git", hubURL, p.Org, p.Repo)
}

// findProject returns the first project with the given name, and whether it was found.
func findProject(projects []hub.Project, name string) (hub.Project, bool) {
	for _, p := range projects {
		if p.Name == name {
			return p, true
		}
	}
	return hub.Project{}, false
}

// promptDir prints a prompt and reads the checkout directory.
// If the user presses Enter without typing, the default (<cwd>/<name>) is used.
func (s *session) promptDir(name string) (string, error) {
	defaultDir := filepath.Join(s.Cwd, name)
	fmt.Fprintf(s.Out, "Checkout directory [%s]: ", defaultDir)
	line, err := s.readLine()
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read input: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultDir, nil
	}
	return line, nil
}

// readLine reads a single line from the shared buffered reader, stripping the
// trailing newline.  Using a single bufio.Reader across the session ensures
// that multiple prompt reads from the same io.Reader do not re-buffer and lose
// previously read bytes.
func (s *session) readLine() (string, error) {
	line, err := s.in.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err == io.EOF && line != "" {
		// Last line without a trailing newline is still a valid input.
		return line, nil
	}
	return line, err
}
