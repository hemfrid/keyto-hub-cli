package gitwire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/gitwire"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

type call struct {
	dir  string
	args []string
}

func makeFakeRunner(calls *[]call, failAt int) gitwire.Runner {
	count := 0
	return func(dir string, args ...string) error {
		n := count
		count++
		*calls = append(*calls, call{dir: dir, args: args})
		if n == failAt {
			return errors.New("fake runner error")
		}
		return nil
	}
}

func TestWire_MakesExactly5CallsInOrder(t *testing.T) {
	var calls []call
	runner := makeFakeRunner(&calls, -1) // never fail

	m := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	err := gitwire.Wire(runner, "/repo", m, "https://hub.example.com", "alice@example.com", "Alice Example")
	if err != nil {
		t.Fatalf("Wire returned unexpected error: %v", err)
	}

	if len(calls) != 5 {
		t.Fatalf("expected 5 git calls, got %d: %v", len(calls), calls)
	}

	// Call 0: set remote URL
	assertCall(t, calls[0], "/repo", []string{
		"remote", "set-url", "origin", "https://hub.example.com/git/hemfrid/acme-web.git",
	})

	// Call 1: reset inherited credential helpers for the Hub host. --replace-all
	// collapses any pre-existing values, so re-wiring an already-wired repo is
	// idempotent — a plain `config` set errors on a multi-valued key.
	assertCall(t, calls[1], "/repo", []string{
		"config", "--replace-all", "credential.https://hub.example.com.helper", "",
	})

	// Call 2: add keyto as the sole credential helper for the Hub host
	assertCall(t, calls[2], "/repo", []string{
		"config", "--add", "credential.https://hub.example.com.helper", "!keyto credential",
	})

	// Call 3: user.email
	assertCall(t, calls[3], "/repo", []string{
		"config", "user.email", "alice@example.com",
	})

	// Call 4: user.name
	assertCall(t, calls[4], "/repo", []string{
		"config", "user.name", "Alice Example",
	})
}

func TestWire_StopsOnSecondCallError(t *testing.T) {
	var calls []call
	runner := makeFakeRunner(&calls, 1) // fail at index 1 (2nd call)

	m := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	err := gitwire.Wire(runner, "/repo", m, "https://hub.example.com", "alice@example.com", "Alice Example")
	if err == nil {
		t.Fatal("expected error from Wire when runner fails, got nil")
	}

	// Only 2 calls should have been made (index 0 and 1); 3rd and 4th must NOT be made
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 calls before stop, got %d: %v", len(calls), calls)
	}
}

func assertCall(t *testing.T, c call, wantDir string, wantArgs []string) {
	t.Helper()
	if c.dir != wantDir {
		t.Errorf("call dir: got %q, want %q", c.dir, wantDir)
	}
	if len(c.args) != len(wantArgs) {
		t.Errorf("call args length: got %d (%v), want %d (%v)", len(c.args), c.args, len(wantArgs), wantArgs)
		return
	}
	for i, a := range wantArgs {
		if c.args[i] != a {
			t.Errorf("call args[%d]: got %q, want %q", i, c.args[i], a)
		}
	}
}

// The marker is a file committed in the repo, so its org and repo are not
// trusted either: a value that is not a GitHub owner or repo name could change
// where the remote URL points. Nothing is run for such a marker.
func TestWire_RefusesAMarkerOrgOrRepoThatIsNotAGitHubName(t *testing.T) {
	cases := []struct{ org, repo string }{
		{"hemfrid/../../other", "acme-web"},
		{"hemfrid", "../../other/repo"},
		{"hemfrid", "acme-web?x=1"},
		{"evil.example@hemfrid", "acme-web"},
		{"hemfrid", "acme-web\n[core]\n\tsshCommand = evil"},
		{"", "acme-web"},
		{"hemfrid", ""},
		{"hemfrid", "acme-web.git"},
	}
	for _, c := range cases {
		var calls []call
		m := &project.Marker{Name: "acme-web", Org: c.org, Repo: c.repo, HubURL: "https://hub.example.com"}
		err := gitwire.Wire(makeFakeRunner(&calls, -1), "/repo", m, "https://hub.example.com", "alice@example.com", "Alice")
		if err == nil {
			t.Errorf("org %q repo %q: want an error", c.org, c.repo)
		}
		if len(calls) != 0 {
			t.Errorf("org %q repo %q: %d git command(s) ran: %v", c.org, c.repo, len(calls), calls)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\n\r\x1b") {
			t.Errorf("org %q repo %q: the error carries a control character: %q", c.org, c.repo, err.Error())
		}
	}
}

// The marker is a file committed in the repo, so its hub_url is not trusted:
// the remote and the credential helper are wired only for the Hub the user is
// signed in to, and a marker naming any other origin is refused before git runs.
func TestWire_RefusesAMarkerThatNamesAnotherHub(t *testing.T) {
	const signedIn = "https://hub.example.com"
	for _, markerHub := range []string{
		"https://evil.example",
		"https://hub.example.com.evil.example",
		"https://hub.example.com@evil.example",
		"https://evil.example/https://hub.example.com",
		"http://hub.example.com",
		"https://hub.example.com:8443",
		"hub.example.com",
		"",
		"file:///tmp/repo",
		"ext::sh -c touch% /tmp/pwned",
		"https://hub.example.com\nhelper = evil",
	} {
		var calls []call
		m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: markerHub}
		err := gitwire.Wire(makeFakeRunner(&calls, -1), "/repo", m, signedIn, "alice@example.com", "Alice")
		if err == nil {
			t.Errorf("marker hub_url %q: want an error", markerHub)
		}
		if len(calls) != 0 {
			t.Errorf("marker hub_url %q: git ran %d time(s): %v", markerHub, len(calls), calls)
		}
		if err != nil && markerHub != "" && strings.Contains(err.Error(), markerHub) {
			t.Errorf("marker hub_url %q: the error echoes the marker's text: %v", markerHub, err)
		}
	}
}

func TestWire_RefusesWhenNotSignedIn(t *testing.T) {
	var calls []call
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	if err := gitwire.Wire(makeFakeRunner(&calls, -1), "/repo", m, "", "a@example.com", "A"); err == nil || len(calls) != 0 {
		t.Fatalf("err=%v calls=%v, want an error and no git call", err, calls)
	}
}

// The same origin written differently (case, trailing slash, explicit default
// port) is the same Hub, and the remote and helper are built from the signed-in
// Hub's URL, never the marker's spelling.
func TestWire_SameOriginWrittenDifferently_WiresTheSignedInHub(t *testing.T) {
	for _, markerHub := range []string{"https://HUB.Example.com/", "https://hub.example.com:443", "HTTPS://hub.example.com"} {
		var calls []call
		m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: markerHub}
		if err := gitwire.Wire(makeFakeRunner(&calls, -1), "/repo", m, "https://hub.example.com", "a@example.com", "A"); err != nil {
			t.Fatalf("marker hub_url %q: %v", markerHub, err)
		}
		assertCall(t, calls[0], "/repo", []string{"remote", "set-url", "origin", "https://hub.example.com/git/hemfrid/acme-web.git"})
		assertCall(t, calls[1], "/repo", []string{"config", "--replace-all", "credential.https://hub.example.com.helper", ""})
	}
}
