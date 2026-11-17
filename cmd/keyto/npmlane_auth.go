package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// npmOutputTail is how much of npm's output is kept to look for its error
// lines. npm prints them last.
const npmOutputTail = 64 << 10

// tailBuffer keeps the last max bytes written to it. npm's output goes to the
// terminal as before; this only remembers the end of it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// npm's line for a refused request: "401 Unauthorized - GET <url> - <reason>".
var npm401Line = regexp.MustCompile(`^401 [^-]*- [A-Z]+ (\S+)`)

// npmErrorLine returns the text of one of npm's own error lines ("npm error …",
// or "npm ERR! …" from npm before v10), and false for any other line. Colour
// codes are dropped first: npm colours its prefix when asked to.
func npmErrorLine(line string) (string, bool) {
	line = strings.TrimSpace(sanitizeForTerminal(line, false))
	for _, prefix := range []string{"npm error ", "npm ERR! "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// npmLaneRefusedToken reports whether npm's output says the Hub's npm registry
// (the lane) answered 401: npm's error code is E401, and its 401 line names a
// URL under that registry. It reads npm's own error lines only, so "401" in a
// package name, a script's output or another registry's refusal does not count.
func npmLaneRefusedToken(output, registry string) bool {
	if registry == "" {
		return false
	}
	base := strings.TrimRight(registry, "/") + "/"
	codeE401, laneURL := false, false
	for _, raw := range strings.Split(output, "\n") {
		line, ok := npmErrorLine(raw)
		if !ok {
			continue
		}
		if line == "code E401" {
			codeE401 = true
		}
		if m := npm401Line.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], base) {
			laneURL = true
		}
	}
	return codeE401 && laneURL
}

// npmLaneRefused is a failed install whose npm output says the Hub's npm
// registry answered 401. It is a hint, not proof: npm's output can be written
// by any package in the tree (npm re-emits a dependency's postinstall output
// as "npm error" lines). `keyto start` asks the Hub itself before it signs in
// again (probeHubCredential).
type npmLaneRefused struct{ err error }

func (e *npmLaneRefused) Error() string {
	// boot.Run prefixes this with "npm install: ".
	return "the Hub's npm registry refused the stored credential (E401): " + e.err.Error()
}

func (e *npmLaneRefused) Unwrap() error { return e.err }

// isNpmLaneRefused reports whether err carries the npm lane hint.
func isNpmLaneRefused(err error) bool {
	var hint *npmLaneRefused
	return errors.As(err, &hint)
}

// npmInstallError turns a failed install into the error boot.Run returns: the
// npm lane hint when npm's output says the Hub's registry refused the token,
// else npm's error unchanged. npm's output is already on the terminal, so it
// is not repeated in the message.
func npmInstallError(err error, output, registry string) error {
	if err == nil || !npmLaneRefusedToken(output, registry) {
		return err
	}
	return &npmLaneRefused{err: err}
}

// probeTimeout bounds the probe. It runs in front of the user, after a failed
// install: a Hub that never answers must not stall `keyto start`.
var probeTimeout = 10 * time.Second

// probeHubCredential asks the Hub one authenticated question with the stored
// credential (the project list), within probeTimeout. A typed 401 back means
// the Hub itself refuses that credential; nil means it accepts it. It is a
// package var so tests can stub it.
var probeHubCredential = func(ctx context.Context) error {
	creds, err := config.Load()
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	c := &hub.Client{BaseURL: creds.HubURL, Credential: creds.Credential}
	_, err = c.ListProjects(ctx)
	return err
}

// laneVerdict is what `keyto start` does about a failed run.
type laneVerdict int

const (
	// leaveAsIs: not a sign-in problem; the error goes back unchanged.
	leaveAsIs laneVerdict = iota
	// signInAgain: the Hub refuses the stored credential.
	signInAgain
	// hubAcceptsCredential: npm named a 401 from the Hub's registry, but the
	// Hub accepts the stored credential.
	hubAcceptsCredential
	// hubDidNotAnswer: npm named a 401, and the Hub could not be asked.
	hubDidNotAnswer
)

// judgeBootError decides whether a failed `keyto start` run is a sign-in
// problem. A 401 the Hub answered directly is one. The npm lane hint is one
// only when the Hub confirms it on a probe, which runs for the hint alone;
// probeErr is the probe's answer when it ran.
func judgeBootError(ctx context.Context, err error) (verdict laneVerdict, probeErr error) {
	if isAuthError(err) {
		return signInAgain, nil
	}
	if !isNpmLaneRefused(err) {
		return leaveAsIs, nil
	}
	probeErr = probeHubCredential(ctx)
	switch {
	case hub.IsUnauthorized(probeErr):
		return signInAgain, nil
	case probeErr == nil:
		return hubAcceptsCredential, nil
	default:
		return hubDidNotAnswer, probeErr
	}
}

// unconfirmedLaneError words a failed install whose 401 the Hub did not
// confirm. npm's own error stays in the chain; the hint's "refused the stored
// credential" wording is dropped, because the Hub did not say that.
func unconfirmedLaneError(err error, verdict laneVerdict, probeErr error) error {
	var hint *npmLaneRefused
	if !errors.As(err, &hint) {
		return err
	}
	if verdict == hubAcceptsCredential {
		return fmt.Errorf("the Hub accepts your sign-in; npm install failed: %w\n  npm's output above shows the cause. If it names a 401 from the Hub's npm registry, check the token line for that registry in ~/.npmrc", hint.err)
	}
	return fmt.Errorf("npm install failed: %w\n  npm's output names a 401 from the Hub's npm registry, but keyto could not check your sign-in with the Hub (%s), so it did not sign in again. If this persists, run `keyto auth`", hint.err, sanitizeForTerminal(probeErr.Error(), false))
}

// npmInstallArgs are npm's arguments for the install. With the Hub's registry
// in use, the @hemfrid scope flag is added, and --color=always when colour is
// wanted: npm then writes to a pipe (see npmInstall) and would drop its colour.
// Without the registry nothing is added.
func npmInstallArgs(sub, laneRegistry string, colour bool) []string {
	args := append([]string{sub}, npmLaneArgs(laneRegistry)...)
	if laneRegistry != "" && colour {
		args = append(args, "--color=always")
	}
	return args
}
