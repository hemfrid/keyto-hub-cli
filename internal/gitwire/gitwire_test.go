package gitwire_test

import (
	"errors"
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

func TestWire_MakesExactly4CallsInOrder(t *testing.T) {
	var calls []call
	runner := makeFakeRunner(&calls, -1) // never fail

	m := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	err := gitwire.Wire(runner, "/repo", m, "alice@example.com", "Alice Example")
	if err != nil {
		t.Fatalf("Wire returned unexpected error: %v", err)
	}

	if len(calls) != 4 {
		t.Fatalf("expected 4 git calls, got %d: %v", len(calls), calls)
	}

	// Call 0: set remote URL
	assertCall(t, calls[0], "/repo", []string{
		"remote", "set-url", "origin", "https://hub.example.com/git/hemfrid/acme-web.git",
	})

	// Call 1: credential helper config
	assertCall(t, calls[1], "/repo", []string{
		"config", "credential.https://hub.example.com.helper", "!keyto credential",
	})

	// Call 2: user.email
	assertCall(t, calls[2], "/repo", []string{
		"config", "user.email", "alice@example.com",
	})

	// Call 3: user.name
	assertCall(t, calls[3], "/repo", []string{
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

	err := gitwire.Wire(runner, "/repo", m, "alice@example.com", "Alice Example")
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
