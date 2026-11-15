package main

import "github.com/hemfrid/keyto-hub-cli/internal/termsafe"

// sanitizeForTerminal makes untrusted server text safe to print as one line.
// See termsafe.Sanitize.
func sanitizeForTerminal(s string, keepColour bool) string { return termsafe.Sanitize(s, keepColour) }

// signedInLine is "<prefix> <name> (<email>)". The name and email are the
// Hub's text (from the token response, or the credentials file it was saved
// to), so both are stripped of terminal control sequences.
func signedInLine(prefix, name, email string) string {
	return prefix + " " + sanitizeForTerminal(name, false) + " (" + sanitizeForTerminal(email, false) + ")"
}
