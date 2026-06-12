package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/browser"
	"github.com/hemfrid/keyto-hub-cli/internal/ui"
)

// keyto start opens the running app in the browser once the dev server is
// listening. The probing/opening primitives are package-level seams so the
// orchestration is unit-tested without a real server or browser.

// openAppPollInterval / openAppMaxAttempts bound the wait: ~60s before giving
// up (the dev server's first compile can be slow on a cold checkout).
const (
	openAppPollInterval = 400 * time.Millisecond
	openAppMaxAttempts  = 150
)

// appURL is the local URL the dev server is expected to serve on. `next dev`
// honors $PORT (default 3000).
func appURL() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	return "http://localhost:" + port
}

// probeURL does a single GET and reports whether the server responded at all
// (any HTTP status counts as "up" — a 404/500 still means it's listening).
var probeURL = func(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// openInBrowser is a seam over the real browser opener so tests don't launch one.
var openInBrowser = browser.OpenURL

// waitThenOpen polls probe until the URL responds (or ctx/deadline expires),
// then opens it in the browser exactly once. It is intentionally best-effort:
// every failure path is silent except a single status line, because the dev
// server itself is the user-facing process.
func waitThenOpen(ctx context.Context, url string, probe func(context.Context, string) bool, open func(string) error, tick time.Duration, attempts int, out io.Writer) {
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return
		}
		if probe(ctx, url) {
			fmt.Fprintf(out, "• opening %s in your browser…\n", url)
			_ = open(url)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
	}
}

// browserOpener returns the boot.OpenApp dependency, or nil when there is no
// human at an interactive terminal (CI, piped output) — in which case opening a
// browser is pointless and the feature stays off.
func browserOpener() func(context.Context) {
	if !ui.IsStderrTTY() || os.Getenv("CI") != "" {
		return nil
	}
	return func(ctx context.Context) {
		waitThenOpen(ctx, appURL(), probeURL, openInBrowser, openAppPollInterval, openAppMaxAttempts, os.Stderr)
	}
}
