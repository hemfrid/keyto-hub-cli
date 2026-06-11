package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
)

// reuseCredential decides whether `keyto auth` should reuse an existing
// credential instead of minting a new CLI token on the Hub.
func TestReuseCredential(t *testing.T) {
	const hub = "https://hub.keytolabs.com"
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	valid := &config.Creds{HubURL: hub, ExpiresAt: future, UserName: "alice"}

	cases := []struct {
		name  string
		c     *config.Creds
		force bool
		want  bool
	}{
		{"valid credential, same hub", valid, false, true},
		{"no stored credential", nil, false, false},
		{"expired credential", &config.Creds{HubURL: hub, ExpiresAt: past}, false, false},
		{"different hub", &config.Creds{HubURL: "https://other.example", ExpiresAt: future}, false, false},
		{"--force always re-auths", valid, true, false},
		{"no-expiry credential treated as valid", &config.Creds{HubURL: hub}, false, true},
	}
	for _, tc := range cases {
		if got := reuseCredential(tc.c, hub, tc.force); got != tc.want {
			t.Errorf("%s: reuseCredential = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	err := dispatch([]string{"bogus"})
	if err == nil {
		t.Fatal("expected non-nil error for unknown command, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected error to contain 'unknown command', got: %q", err.Error())
	}
}

func TestDispatch_NoArgs(t *testing.T) {
	err := dispatch([]string{})
	if err != nil {
		t.Fatalf("expected nil error for no args, got: %v", err)
	}
}

func TestDispatch_Help(t *testing.T) {
	err := dispatch([]string{"help"})
	if err != nil {
		t.Fatalf("expected nil error for help command, got: %v", err)
	}
}

// dispatch must route the "update" command to runUpdate (and not fall through
// to "unknown command"). runUpdate is stubbed so no real network update runs.
func TestDispatch_UpdateRoutesToRunUpdate(t *testing.T) {
	called := false
	orig := runUpdate
	runUpdate = func() error { called = true; return nil }
	t.Cleanup(func() { runUpdate = orig })

	if err := dispatch([]string{"update"}); err != nil {
		t.Fatalf("dispatch(update) returned error: %v", err)
	}
	if !called {
		t.Fatal("dispatch did not route 'update' to runUpdate")
	}
}

// cloneArgs must inject the keyto credential helper BEFORE the clone subcommand
// so the clone authenticates to the Hub proxy instead of prompting for a username.
func TestCloneArgs_InjectsCredentialHelperBeforeClone(t *testing.T) {
	args := cloneArgs("https://hub.keytolabs.com", "https://hub.keytolabs.com/git/o/r.git", "/tmp/r")

	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d: %v", len(args), args)
	}
	if args[0] != "-c" {
		t.Fatalf("expected first arg %q, got %q (full: %v)", "-c", args[0], args)
	}
	if args[1] != "credential.https://hub.keytolabs.com.helper=!keyto credential" {
		t.Fatalf("unexpected helper config: %q", args[1])
	}
	if args[2] != "clone" {
		t.Fatalf("expected %q to precede repo/dir, got %q (full: %v)", "clone", args[2], args)
	}
	if args[3] != "https://hub.keytolabs.com/git/o/r.git" || args[4] != "/tmp/r" {
		t.Fatalf("unexpected repo/dir args: %v", args[3:])
	}
}

// With no Hub URL (e.g. unauthenticated), cloneArgs must not inject a helper and
// must fall back to a plain clone.
func TestCloneArgs_NoHubURLOmitsHelper(t *testing.T) {
	args := cloneArgs("", "https://x/git/o/r.git", "/tmp/r")

	if len(args) != 3 || args[0] != "clone" {
		t.Fatalf("expected plain [clone <url> <dir>], got %v", args)
	}
}

// TestDispatch_EnvSubcommand_Unknown ensures `keyto env unknown` returns an error.
func TestDispatch_EnvSubcommand_Unknown(t *testing.T) {
	// `keyto env` with a subcommand that is not "sync" should fail.
	// Note: `keyto env sync` requires real auth + cwd setup, so we test
	// the dispatch routing only at the unknown-subcommand level.
	err := dispatch([]string{"env", "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown env subcommand, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention 'unknown', got: %v", err)
	}
}

// TestDispatch_DevRoutesToRunDev ensures `keyto dev` routes to the runDev
// package variable (similar to the runUpdate pattern), without running Docker.
func TestDispatch_DevRoutesToRunDev(t *testing.T) {
	called := false
	origRunDev := runDev
	runDev = func(ctx context.Context, args []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runDev = origRunDev })

	if err := dispatch([]string{"dev"}); err != nil {
		t.Fatalf("dispatch(dev) returned error: %v", err)
	}
	if !called {
		t.Fatal("dispatch did not route 'dev' to runDev")
	}
}
