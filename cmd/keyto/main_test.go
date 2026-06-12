package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
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

func TestDispatch_CheckoutRoutesToRunCheckout(t *testing.T) {
	called := false
	orig := runCheckout
	runCheckout = func(ctx context.Context, args []string) error { called = true; return nil }
	t.Cleanup(func() { runCheckout = orig })
	if err := dispatch([]string{"checkout"}); err != nil {
		t.Fatalf("dispatch(checkout) returned error: %v", err)
	}
	if !called {
		t.Fatal("dispatch did not route 'checkout' to runCheckout")
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

// TestRunEnvSync_Integration exercises the full envsync.Run path end-to-end
// against a local httptest.Server that stands in for the Hub values endpoint.
// It uses Deps.Cwd injection (no os.Chdir — process-global mutation) and wires
// the fake Hub via a staticFetch-style HubFetcher backed by httptest.NewServer.
func TestRunEnvSync_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}

	// Stand up a fake Hub values endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"env":     "uat",
			"values":  map[string]string{"SENDGRID_API_KEY": "sg_integration_value"},
			"missing": []string{},
		})
	}))
	defer srv.Close()

	// Build a fully isolated project directory in a temp dir.
	projectDir := t.TempDir()

	// Write project marker.
	keytoDir := filepath.Join(projectDir, ".keyto")
	if err := os.MkdirAll(keytoDir, 0o755); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	marker := map[string]string{"name": "acme-web", "org": "hemfrid", "repo": "acme-web", "hub_url": srv.URL}
	markerData, _ := json.MarshalIndent(marker, "", "  ")
	if err := os.WriteFile(filepath.Join(keytoDir, "project.json"), markerData, 0o644); err != nil {
		t.Fatalf("write project.json: %v", err)
	}

	// Write inventory.
	inv := map[string]interface{}{
		"schemaVersion": 1,
		"keys": []map[string]interface{}{
			{"key": "DATABASE_URL", "localSource": "container", "service": "postgres", "usages": []string{}},
			{"key": "SENDGRID_API_KEY", "localSource": "uat", "usages": []string{}},
			{"key": "DEV_USER_EMAIL", "localSource": "placeholder", "usages": []string{}},
		},
	}
	invData, _ := json.MarshalIndent(inv, "", "  ")
	if err := os.WriteFile(filepath.Join(keytoDir, "env-inventory.json"), invData, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	// Wire the fake Hub via a HubFetcher that delegates to the httptest server.
	// This exercises the same code path as the real hub.Client.FetchEnvValues
	// without touching any process-global state.
	fakeFetcher := func(ctx context.Context, org, repo, env string, keys []string) (map[string]string, []string, error) {
		hubClient := &hub.Client{BaseURL: srv.URL, Credential: "tok_integration"}
		return hubClient.FetchEnvValues(ctx, org, repo, env, keys)
	}

	outPath := filepath.Join(projectDir, ".env.test")
	creds := &config.Creds{
		Credential: "tok_integration",
		HubURL:     srv.URL,
		UserEmail:  "alice@keytogroup.com",
		UserName:   "Alice",
	}

	d := envsync.Deps{
		Creds: creds,
		Cwd:   projectDir, // inject project dir — no os.Chdir
		Fetch: fakeFetcher,
		Out:   &strings.Builder{},
	}

	if err := envsync.Run(context.Background(), []string{"--out", outPath}, d); err != nil {
		t.Fatalf("envsync.Run integration error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .env.test: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "SENDGRID_API_KEY=sg_integration_value") {
		t.Errorf("integration: .env missing SENDGRID_API_KEY; got:\n%s", content)
	}
	if !strings.Contains(content, "DATABASE_URL=postgres://") {
		t.Errorf("integration: .env missing DATABASE_URL; got:\n%s", content)
	}
	if !strings.Contains(content, "# DEV_USER_EMAIL=") {
		t.Errorf("integration: .env missing placeholder comment; got:\n%s", content)
	}
}
