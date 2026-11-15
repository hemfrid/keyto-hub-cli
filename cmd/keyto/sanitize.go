package main

import (
	"fmt"
	"io"

	"github.com/hemfrid/keyto-hub-cli/internal/termsafe"
)

// sanitizeForTerminal makes untrusted server text safe to print as one line.
// See termsafe.Sanitize.
func sanitizeForTerminal(s string, keepColour bool) string { return termsafe.Sanitize(s, keepColour) }

// signedInLine is "<prefix> <name> (<email>)". The name and email are the
// Hub's text (from the token response, or the credentials file it was saved
// to), so both are stripped of terminal control sequences.
func signedInLine(prefix, name, email string) string {
	return prefix + " " + sanitizeForTerminal(name, false) + " (" + sanitizeForTerminal(email, false) + ")"
}

// printError writes "keyto: <error>" for the user. An error can carry a tool's
// output (git, npm), which may hold terminal control sequences a server sent.
// Each line is stripped of them on its own, so a multi-line message keeps its
// lines and its indentation. Carriage returns are read as a terminal reads
// them: one at the end of a line ("\r\n") is dropped, and one inside a line
// (git's progress updates) starts the line over, so only the text after the
// last one is kept.
func printError(w io.Writer, err error) {
	fmt.Fprintln(w, "keyto:", termsafe.Text(err.Error()))
}
