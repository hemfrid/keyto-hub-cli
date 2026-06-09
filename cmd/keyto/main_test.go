package main

import (
	"strings"
	"testing"
)

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
