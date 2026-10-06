package main

import "github.com/hemfrid/keyto-hub-cli/internal/termsafe"

// sanitizeForTerminal makes untrusted server text safe to print as one line.
// See termsafe.Sanitize.
func sanitizeForTerminal(s string, keepColour bool) string { return termsafe.Sanitize(s, keepColour) }
