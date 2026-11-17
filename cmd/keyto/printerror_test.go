package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/gitwire"
)

// Every error the CLI prints goes through printError. A tool's output (git,
// npm) ends up inside those errors, and it can carry terminal control
// sequences sent by a server.
func TestPrintError_StripsControlSequencesAndKeepsTheLines(t *testing.T) {
	err := errors.New("git [remote set-url origin x]: exit status 128\nremote: \x1b]0;pwned\x07hello\x1b[2J\r\nfatal: ‮evil\x1b[31m red")
	var out bytes.Buffer
	printError(&out, err)
	got := out.String()
	if strings.ContainsAny(got, "\x1b\x07\r‮") {
		t.Errorf("control character in %q", got)
	}
	want := "keyto: git [remote set-url origin x]: exit status 128\nremote: hello\nfatal: evil red\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrintError_AnOrdinaryErrorIsUnchanged(t *testing.T) {
	var out bytes.Buffer
	printError(&out, fmt.Errorf("env sync: %w", errors.New("no .keyto/project.json found — run `keyto start` first")))
	if got, want := out.String(), "keyto: env sync: no .keyto/project.json found — run `keyto start` first\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	out.Reset()
	printError(&out, errors.New("npm install failed: exit status 1\n  npm's output names a 401\tfrom the registry"))
	if got, want := out.String(), "keyto: npm install failed: exit status 1\n  npm's output names a 401\tfrom the registry\n"; got != want {
		t.Errorf("a multi-line tip must keep its lines and indent: got %q, want %q", got, want)
	}
}

// git writes its progress with carriage returns, each update overwriting the
// last. A terminal shows only what follows the last one on a line; so does
// printError. Windows line endings count as line breaks.
func TestPrintError_GitProgressShowsOnlyItsLastUpdate(t *testing.T) {
	err := errors.New("git [fetch origin]: exit status 128\n" +
		"Receiving objects:  10% (1/10)\rReceiving objects:  50% (5/10)\rReceiving objects: 100% (10/10), done.\n" +
		"remote: Counting objects: 3\rremote: Counting objects: 9, done.\r\n" +
		"fatal: unable to access 'https://hub.example.com/git/hemfrid/acme-web.git/': denied\r\n" +
		"hint: try again")
	var out bytes.Buffer
	printError(&out, err)
	want := "keyto: git [fetch origin]: exit status 128\n" +
		"Receiving objects: 100% (10/10), done.\n" +
		"remote: Counting objects: 9, done.\n" +
		"fatal: unable to access 'https://hub.example.com/git/hemfrid/acme-web.git/': denied\n" +
		"hint: try again\n"
	if got := out.String(); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestPrintError_ATrailingCarriageReturnDoesNotBlankTheLine(t *testing.T) {
	var out bytes.Buffer
	printError(&out, errors.New("fatal: bad\r"))
	if got, want := out.String(), "keyto: fatal: bad\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The real git runner, with a git that prints an escape sequence and fails.
func TestPrintError_GitOutputFromTheRealRunnerIsSanitized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'remote: \\033]0;pwned\\007\\033[2Jdenied\\n' >&2\nexit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := gitwire.RealRunner(t.TempDir(), "remote", "set-url", "origin", "https://hub.example.com/git/hemfrid/acme-web.git")
	if err == nil {
		t.Fatal("expected the fake git to fail")
	}
	if !strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("the test needs git's raw output in the error to mean anything; got %q", err.Error())
	}
	var out bytes.Buffer
	printError(&out, err)
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("git's escape sequence reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "remote: denied") {
		t.Errorf("the readable part of git's output should remain: %q", out.String())
	}
}

// The path main() takes: a failing command's error reaches the user only
// through printError.
func TestRun_AFailingCommandIsPrintedWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"no-such\x1b[2Jcommand\x1b]0;pwned\x07"}, &out)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control character in %q", out.String())
	}
	if !strings.HasPrefix(out.String(), "keyto: ") || !strings.Contains(out.String(), "no-such") {
		t.Errorf("unexpected output: %q", out.String())
	}
}
