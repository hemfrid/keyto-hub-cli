package main

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

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
// or "npm ERR! …" from npm before v10), and false for any other line.
func npmErrorLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
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

// npmInstallError turns a failed install into the error boot.Run returns. A
// token the Hub's npm registry refused becomes a typed 401, so `keyto start`
// signs in again and retries (the retry rewrites the lane's line in ~/.npmrc
// with the new credential). Anything else is npm's error unchanged. npm's
// output is already on the terminal, so it is not repeated in the message.
func npmInstallError(err error, output, registry string) error {
	if err == nil || !npmLaneRefusedToken(output, registry) {
		return err
	}
	refused := &hub.StatusError{
		Op:         "npm install",
		StatusCode: 401,
		Message:    "npm install: the Hub's npm registry refused the stored credential (E401)",
	}
	return fmt.Errorf("%w: %w", refused, err)
}
