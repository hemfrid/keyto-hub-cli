package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

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

// probeHubCredential asks the Hub one authenticated question with the stored
// credential (the project list). A typed 401 back means the Hub itself refuses
// that credential. It is a package var so tests can stub it.
var probeHubCredential = func(ctx context.Context) error {
	creds, err := config.Load()
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	c := &hub.Client{BaseURL: creds.HubURL, Credential: creds.Credential}
	_, err = c.ListProjects(ctx)
	return err
}

// hubRefusesStoredCredential is the decision behind a re-login in `keyto
// start`: a 401 the Hub answered directly, or the npm lane hint confirmed by
// the Hub's own 401 on a probe. The probe runs only for the hint.
func hubRefusesStoredCredential(ctx context.Context, err error) bool {
	if isAuthError(err) {
		return true
	}
	return isNpmLaneRefused(err) && hub.IsUnauthorized(probeHubCredential(ctx))
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
