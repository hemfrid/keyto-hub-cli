// Package credential implements a git credential helper for the Keyto Hub.
//
// git invokes this binary with the operation ("get", "store", or "erase") as
// the first argument and the request attributes on stdin (key=value lines
// terminated by a blank line).  We answer "get" requests for the Hub host only;
// all other operations and all non-Hub hosts are silently ignored so we never
// leak credentials to unrelated helpers in the chain.
package credential

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hemfrid/keyto-cli/internal/config"
)

// Helper implements the git credential helper protocol.
//
//   - op: the git operation ("get", "store", or "erase").
//   - in: stdin from git — key=value lines followed by a blank line.
//   - out: stdout to git — we write the credential lines on a successful "get".
//   - creds: the stored Keyto credentials (may be nil if not authenticated).
//   - hubHost: the hostname of the Hub (e.g. "hub.keytolabs.com").
func Helper(op string, in io.Reader, out io.Writer, creds *config.Creds, hubHost string) error {
	// "store" and "erase" are always no-ops: we manage credentials ourselves.
	if op == "store" || op == "erase" {
		return nil
	}

	if op != "get" {
		return nil
	}

	// Parse the git credential protocol: key=value lines until EOF or blank line.
	attrs := parseAttrs(in)

	// Only answer for the Hub host.
	if attrs["host"] != hubHost {
		return nil
	}

	// Require usable credentials.
	if !usable(creds) {
		return nil
	}

	// Emit the credential in git credential helper format.
	fmt.Fprintf(out, "username=keyto\npassword=%s\n\n", creds.Credential)
	return nil
}

// parseAttrs reads key=value lines from r until EOF or a blank line and returns
// the collected attributes as a map.  Lines that do not contain '=' are silently
// skipped so the helper is tolerant of unexpected git protocol extensions.
func parseAttrs(r io.Reader) map[string]string {
	attrs := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// Blank line signals end of the attribute block.
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		attrs[k] = v
	}
	return attrs
}

// usable reports whether creds can be used to authenticate: non-nil, non-empty
// Credential, and not expired.
func usable(creds *config.Creds) bool {
	if creds == nil {
		return false
	}
	if creds.Credential == "" {
		return false
	}
	// A zero ExpiresAt means "no expiry set" — treat as valid.
	if !creds.ExpiresAt.IsZero() && creds.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}
