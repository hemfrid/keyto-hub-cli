// Package credential implements a git credential helper for the Keyto Hub.
//
// git invokes this binary with the operation ("get", "store", or "erase") as
// the first argument and the request attributes on stdin (key=value lines
// terminated by a blank line). We answer "get" requests for the Hub host only;
// all other hosts are silently ignored so we never leak credentials to
// unrelated helpers in the chain.
//
// Friendly re-login (S4.3): git surfaces a credential helper's stderr to the
// user, so when the stored credential is expired (on "get") or the Hub rejected
// it (git calls "erase" after a 401 from a revoked/expired credential) we print
// a clear "run keyto auth" hint instead of letting git fail cryptically or fall
// back to an interactive Username: prompt.
package credential

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
)

const (
	expiredMsg  = "keyto: your sign-in has expired — run `keyto auth` to sign in again"
	rejectedMsg = "keyto: the Hub rejected your credential (expired or revoked) — run `keyto auth` to sign in again"
)

// Helper implements the git credential helper protocol.
//
//   - op: the git operation ("get", "store", or "erase").
//   - in: stdin from git — key=value lines followed by a blank line.
//   - out: stdout to git — the credential lines on a successful "get".
//   - errOut: stderr to the user — friendly re-login hints (never secrets).
//   - creds: the stored Keyto credentials (may be nil if not authenticated).
//   - hubHost: the hostname of the Hub (e.g. "hub.keytolabs.com").
func Helper(
	op string,
	in io.Reader,
	out, errOut io.Writer,
	creds *config.Creds,
	hubHost string,
) error {
	// "store" is always a no-op: we manage credentials ourselves.
	if op == "store" {
		return nil
	}
	if op != "get" && op != "erase" {
		return nil
	}

	attrs := parseAttrs(in)

	// Only act for the Hub host. When not signed in, hubHost is "" and never
	// matches a real request host, so we stay silent and defer to the next
	// helper in git's chain.
	if attrs["host"] != hubHost {
		return nil
	}

	if op == "erase" {
		// git calls erase after the Hub rejected the credential (a 401 from a
		// revoked or server-expired credential). Nudge a clean re-login.
		fmt.Fprintln(errOut, rejectedMsg)
		return nil
	}

	// op == "get"
	if creds == nil || creds.Credential == "" {
		return nil
	}
	if creds.Expired() {
		fmt.Fprintln(errOut, expiredMsg)
		return nil
	}

	// Emit the credential in git credential helper format.
	fmt.Fprintf(out, "username=keyto\npassword=%s\n\n", creds.Credential)
	return nil
}

// parseAttrs reads key=value lines from r until EOF or a blank line and returns
// the collected attributes as a map. Lines that do not contain '=' are silently
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
