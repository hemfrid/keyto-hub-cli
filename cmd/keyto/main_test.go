package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/auth"
	"github.com/hemfrid/keyto-hub-cli/internal/boot"
	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// tipDeps builds a prereq.Deps with scripted detection for startPrereqTip tests.
// present maps command name → version string (also reported by Version).
func tipDeps(present map[string]string) prereq.Deps {
	return prereq.Deps{
		HasCommand: func(name string) bool { _, ok := present[name]; return ok },
		Version:    func(name string) (string, error) { return present[name], nil },
	}
}

func TestStartPrereqTip(t *testing.T) {
	cases := []struct {
		name        string
		present     map[string]string
		wantEmpty   bool
		wantMissing []string
	}{
		{
			name:      "docker and node20 present → no tip",
			present:   map[string]string{"docker": "27", "node": "v20.11.0"},
			wantEmpty: true,
		},
		{
			name:      "docker and modern node24 present → no tip",
			present:   map[string]string{"docker": "27", "node": "v24.2.0"},
			wantEmpty: true,
		},
		{
			name:        "docker missing → tip names Docker",
			present:     map[string]string{"node": "v20.11.0"},
			wantMissing: []string{"Docker"},
		},
		{
			name:        "node missing → tip names Node 20",
			present:     map[string]string{"docker": "27"},
			wantMissing: []string{"Node 20"},
		},
		{
			name:        "node too old → tip names Node 20",
			present:     map[string]string{"docker": "27", "node": "v18.19.0"},
			wantMissing: []string{"Node 20"},
		},
		{
			name:        "both missing → tip names both",
			present:     map[string]string{},
			wantMissing: []string{"Docker", "Node 20"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := startPrereqTip(tipDeps(tc.present))
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("expected no tip, got: %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("expected a tip naming %v, got empty", tc.wantMissing)
			}
			for _, m := range tc.wantMissing {
				if !strings.Contains(got, m) {
					t.Errorf("tip %q missing expected token %q", got, m)
				}
			}
		})
	}
}

func TestNodeVersionTipOK(t *testing.T) {
	ok := []string{"v20.0.0", "v20.11.0", "20.9.0", "v21.0.0", "v22.3.0", "v24.2.0"}
	notOK := []string{"v18.19.0", "v19.9.0", "", "garbage"}
	for _, v := range ok {
		if !nodeVersionTipOK(v) {
			t.Errorf("nodeVersionTipOK(%q) = false, want true", v)
		}
	}
	for _, v := range notOK {
		if nodeVersionTipOK(v) {
			t.Errorf("nodeVersionTipOK(%q) = true, want false", v)
		}
	}
}

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

// gitOriginURL must return the origin URL of a real repo, and error on a
// non-repo dir or a repo without an origin remote.
func TestGitOriginURL(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if _, err := gitOriginURL(ctx, dir); err == nil {
		t.Error("expected error for a non-repo directory, got nil")
	}

	git("init")
	if _, err := gitOriginURL(ctx, dir); err == nil {
		t.Error("expected error for a repo without an origin remote, got nil")
	}

	git("remote", "add", "origin", "https://github.com/hemfrid/acme-web.git")
	got, err := gitOriginURL(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://github.com/hemfrid/acme-web.git" {
		t.Errorf("origin = %q, want https://github.com/hemfrid/acme-web.git", got)
	}
}

// checkoutDeps must wire the OriginURL dep so checkout.Run can offer adopting
// an existing plain-git checkout instead of cloning a duplicate.
func TestCheckoutDeps_WiresOriginURL(t *testing.T) {
	d := checkoutDeps(context.Background(), nil, "/tmp/cwd")
	if d.OriginURL == nil {
		t.Fatal("checkoutDeps must set OriginURL")
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

// routeStubs swaps the boot/checkout entry points and the marker reader for
// fakes so the start router's boot-vs-checkout decision can be asserted without
// any real clone or local boot. It returns pointers to the called flags.
func routeStubs(t *testing.T, marker *project.Marker) (boot, checkout *bool) {
	b, c := false, false
	ob, oc, om := runBoot, runCheckout, readMarker
	runBoot = func(ctx context.Context, args []string) error { b = true; return nil }
	runCheckout = func(ctx context.Context, args []string) error { c = true; return nil }
	readMarker = func(dir string) (*project.Marker, error) { return marker, nil }
	t.Cleanup(func() { runBoot, runCheckout, readMarker = ob, oc, om })
	return &b, &c
}

func TestStartRouter_NoArgInProject_RoutesToBoot(t *testing.T) {
	boot, checkout := routeStubs(t, &project.Marker{Name: "demo"})
	if err := runStartRouter(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !*boot || *checkout {
		t.Fatalf("want boot only; boot=%v checkout=%v", *boot, *checkout)
	}
}

func TestStartRouter_MatchingName_RoutesToBoot(t *testing.T) {
	boot, checkout := routeStubs(t, &project.Marker{Name: "demo"})
	_ = runStartRouter(context.Background(), []string{"demo"})
	if !*boot || *checkout {
		t.Fatalf("want boot only; boot=%v checkout=%v", *boot, *checkout)
	}
}

func TestStartRouter_ForeignName_RoutesToCheckout(t *testing.T) {
	boot, checkout := routeStubs(t, &project.Marker{Name: "demo"})
	_ = runStartRouter(context.Background(), []string{"other"})
	if *boot || !*checkout {
		t.Fatalf("want checkout only; boot=%v checkout=%v", *boot, *checkout)
	}
}

func TestStartRouter_NoArgNoProject_RoutesToCheckout(t *testing.T) {
	boot, checkout := routeStubs(t, nil)
	_ = runStartRouter(context.Background(), nil)
	if *boot || !*checkout {
		t.Fatalf("want checkout (deprecated picker); boot=%v checkout=%v", *boot, *checkout)
	}
}

func TestDispatch_DevRoutesToBoot(t *testing.T) {
	orig := runBoot
	called := false
	runBoot = func(ctx context.Context, args []string) error { called = true; return nil }
	t.Cleanup(func() { runBoot = orig })
	if err := dispatch([]string{"dev"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("dispatch did not route 'dev' to runBoot")
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

// TestIsAuthError verifies the 401/Unauthorized classifier used to trigger the
// automatic re-auth retry.
func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"401 status string", fmt.Errorf("list-projects failed: 401 Unauthorized"), true},
		{"lowercase unauthorized", errors.New("request rejected: unauthorized"), true},
		{"bare 401 number", errors.New("got 401"), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"403 is not auth-reauth", errors.New("forbidden: 403"), false},
	}
	for _, tc := range cases {
		if got := isAuthError(tc.err); got != tc.want {
			t.Errorf("%s: isAuthError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// reauthStubs swaps the reauth seam for a fake that returns the supplied creds
// (and counts calls) so the auto-reauth retry path can be asserted without
// launching a real browser. It also points KEYTO_HOME at a temp dir so any
// config.Load inside the command under test is hermetic.
func reauthStubs(t *testing.T, minted *config.Creds) *int {
	t.Helper()
	t.Setenv("KEYTO_HOME", t.TempDir())
	calls := 0
	orig := reauth
	reauth = func(ctx context.Context) (*config.Creds, error) {
		calls++
		return minted, nil
	}
	t.Cleanup(func() { reauth = orig })
	return &calls
}

func mintedCreds() *config.Creds {
	return &config.Creds{
		Credential: "tok_reauthed",
		HubURL:     "https://hub.keytolabs.com",
		UserEmail:  "alice@keytogroup.com",
		UserName:   "Alice",
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}
}

// validCreds writes a non-expired credential to KEYTO_HOME so the command under
// test does NOT re-auth up front (it must reach the run + 401 path).
func validCreds(t *testing.T) {
	t.Helper()
	if err := config.Save(&config.Creds{
		Credential: "tok_valid",
		HubURL:     "https://hub.keytolabs.com",
		UserEmail:  "alice@keytogroup.com",
		UserName:   "Alice",
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed valid creds: %v", err)
	}
}

func TestCheckout_NoError_NoReauth(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := checkoutRun
	checkoutRun = func(ctx context.Context, arg string, d checkout.Deps) (string, error) {
		runs++
		return "/tmp/demo", nil
	}
	t.Cleanup(func() { checkoutRun = orig })

	if err := runCheckoutImpl(context.Background(), []string{"demo"}); err != nil {
		t.Fatalf("runCheckoutImpl error: %v", err)
	}
	if runs != 1 {
		t.Errorf("checkoutRun called %d times, want 1", runs)
	}
	if *reauthCalls != 0 {
		t.Errorf("reauth called %d times, want 0", *reauthCalls)
	}
}

func TestCheckout_AuthError_ReauthsAndRetriesOnce(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := checkoutRun
	checkoutRun = func(ctx context.Context, arg string, d checkout.Deps) (string, error) {
		runs++
		if runs == 1 {
			return "", fmt.Errorf("list-projects failed: 401 Unauthorized")
		}
		// Retry must use the freshly-minted credential.
		if d.Creds == nil || d.Creds.Credential != "tok_reauthed" {
			t.Errorf("retry did not use minted creds; got %+v", d.Creds)
		}
		return "/tmp/demo", nil
	}
	t.Cleanup(func() { checkoutRun = orig })

	if err := runCheckoutImpl(context.Background(), []string{"demo"}); err != nil {
		t.Fatalf("runCheckoutImpl error: %v", err)
	}
	if runs != 2 {
		t.Errorf("checkoutRun called %d times, want 2 (run + retry)", runs)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want 1", *reauthCalls)
	}
}

func TestCheckout_PersistentAuthError_ReauthsOnceRunsTwiceReturnsError(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := checkoutRun
	checkoutRun = func(ctx context.Context, arg string, d checkout.Deps) (string, error) {
		runs++
		return "", fmt.Errorf("list-projects failed: 401 Unauthorized")
	}
	t.Cleanup(func() { checkoutRun = orig })

	err := runCheckoutImpl(context.Background(), []string{"demo"})
	if err == nil {
		t.Fatal("expected the persistent 401 to be returned, got nil")
	}
	if !isAuthError(err) {
		t.Errorf("expected the returned error to still be an auth error, got: %v", err)
	}
	if runs != 2 {
		t.Errorf("checkoutRun called %d times, want exactly 2 (no infinite re-auth loop)", runs)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want exactly 1 (single retry)", *reauthCalls)
	}
}

func TestCheckout_NilCreds_ReauthsUpFront(t *testing.T) {
	// KEYTO_HOME is an empty temp dir → config.Load returns ErrNotAuthed → nil
	// creds → reauth up front, before the run.
	reauthCalls := reauthStubs(t, mintedCreds())

	runs := 0
	orig := checkoutRun
	checkoutRun = func(ctx context.Context, arg string, d checkout.Deps) (string, error) {
		runs++
		if d.Creds == nil || d.Creds.Credential != "tok_reauthed" {
			t.Errorf("run did not receive up-front-minted creds; got %+v", d.Creds)
		}
		return "/tmp/demo", nil
	}
	t.Cleanup(func() { checkoutRun = orig })

	if err := runCheckoutImpl(context.Background(), []string{"demo"}); err != nil {
		t.Fatalf("runCheckoutImpl error: %v", err)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want 1 (up front)", *reauthCalls)
	}
	if runs != 1 {
		t.Errorf("checkoutRun called %d times, want 1", runs)
	}
}

func TestCheckout_ExpiredCreds_ReauthsUpFront(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	// Seed an EXPIRED credential so the up-front guard fires.
	if err := config.Save(&config.Creds{
		Credential: "tok_expired",
		HubURL:     "https://hub.keytolabs.com",
		ExpiresAt:  time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed expired creds: %v", err)
	}

	runs := 0
	orig := checkoutRun
	checkoutRun = func(ctx context.Context, arg string, d checkout.Deps) (string, error) {
		runs++
		if d.Creds == nil || d.Creds.Credential != "tok_reauthed" {
			t.Errorf("run did not receive up-front-minted creds; got %+v", d.Creds)
		}
		return "/tmp/demo", nil
	}
	t.Cleanup(func() { checkoutRun = orig })

	if err := runCheckoutImpl(context.Background(), []string{"demo"}); err != nil {
		t.Fatalf("runCheckoutImpl error: %v", err)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want 1 (up front on expiry)", *reauthCalls)
	}
	if runs != 1 {
		t.Errorf("checkoutRun called %d times, want 1", runs)
	}
}

func TestBoot_NoError_NoReauth(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := bootRun
	bootRun = func(ctx context.Context, d boot.Deps, f boot.Flags) error {
		runs++
		return nil
	}
	t.Cleanup(func() { bootRun = orig })

	if err := runBootImpl(context.Background(), nil); err != nil {
		t.Fatalf("runBootImpl error: %v", err)
	}
	if runs != 1 {
		t.Errorf("bootRun called %d times, want 1", runs)
	}
	if *reauthCalls != 0 {
		t.Errorf("reauth called %d times, want 0", *reauthCalls)
	}
}

func TestBoot_AuthError_ReauthsAndRetriesOnce(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := bootRun
	bootRun = func(ctx context.Context, d boot.Deps, f boot.Flags) error {
		runs++
		if runs == 1 {
			return fmt.Errorf("fetch-env-values failed: 401 Unauthorized")
		}
		return nil
	}
	t.Cleanup(func() { bootRun = orig })

	if err := runBootImpl(context.Background(), nil); err != nil {
		t.Fatalf("runBootImpl error: %v", err)
	}
	if runs != 2 {
		t.Errorf("bootRun called %d times, want 2 (run + retry)", runs)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want 1", *reauthCalls)
	}
}

func TestBoot_PersistentAuthError_ReauthsOnceRunsTwiceReturnsError(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)

	runs := 0
	orig := bootRun
	bootRun = func(ctx context.Context, d boot.Deps, f boot.Flags) error {
		runs++
		return fmt.Errorf("fetch-env-values failed: 401 Unauthorized")
	}
	t.Cleanup(func() { bootRun = orig })

	err := runBootImpl(context.Background(), nil)
	if err == nil {
		t.Fatal("expected the persistent 401 to be returned, got nil")
	}
	if !isAuthError(err) {
		t.Errorf("expected the returned error to still be an auth error, got: %v", err)
	}
	if runs != 2 {
		t.Errorf("bootRun called %d times, want exactly 2 (no infinite re-auth loop)", runs)
	}
	if *reauthCalls != 1 {
		t.Errorf("reauth called %d times, want exactly 1 (single retry)", *reauthCalls)
	}
}

// TestReauth_MintsAndSaves exercises the real reauth seam (with authRun stubbed
// — NO real browser) and verifies it mints, persists, and returns fresh creds.
func TestReauth_MintsAndSaves(t *testing.T) {
	t.Setenv("KEYTO_HOME", t.TempDir())
	t.Setenv("KEYTO_HUB_URL", "https://hub.keytolabs.com")

	authCalls := 0
	origAuth := authRun
	authRun = func(ctx context.Context, opts auth.Options) (*hub.TokenResponse, error) {
		authCalls++
		if opts.OpenURL == nil {
			t.Error("reauth must pass an OpenURL to auth.Run")
		}
		return &hub.TokenResponse{
			Credential: "tok_fresh",
			UserEmail:  "alice@keytogroup.com",
			UserName:   "Alice",
			ExpiresAt:  time.Now().Add(24 * time.Hour),
		}, nil
	}
	t.Cleanup(func() { authRun = origAuth })

	got, err := reauth(context.Background())
	if err != nil {
		t.Fatalf("reauth error: %v", err)
	}
	if authCalls != 1 {
		t.Errorf("authRun called %d times, want 1", authCalls)
	}
	if got == nil || got.Credential != "tok_fresh" {
		t.Fatalf("reauth returned %+v, want credential tok_fresh", got)
	}
	// It must have persisted the minted credential to disk.
	saved, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load after reauth: %v", err)
	}
	if saved.Credential != "tok_fresh" {
		t.Errorf("saved credential = %q, want tok_fresh", saved.Credential)
	}
}
