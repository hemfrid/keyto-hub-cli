package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/boot"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

const laneRegistry = "https://hub.keytolabs.com/api/npm/"

// npm's own output for a refused token on the Hub's registry.
const npmE401OnTheLane = `npm error code E401
npm error 401 Unauthorized - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fworkers-manifest - unauthorized
npm error A complete log of this run can be found in: /home/u/.npm/_logs/2026-11-15T10_00_00_000Z-debug-0.log`

func TestNpmLaneRefusedToken(t *testing.T) {
	cases := []struct {
		name, output, registry string
		want                   bool
	}{
		{"E401 on the Hub's registry", npmE401OnTheLane, laneRegistry, true},
		{"the registry given without its trailing slash", npmE401OnTheLane, "https://hub.keytolabs.com/api/npm", true},
		{"older npm's prefix (npm ERR!)", strings.ReplaceAll(npmE401OnTheLane, "npm error", "npm ERR!"), laneRegistry, true},
		{
			"npm 11.12.1's real output against a local registry answering 401 (captured 2026-11-15)",
			"npm error code E401\nnpm error 401 Unauthorized - GET http://127.0.0.1:65026/api/npm/@hemfrid%2fapp-401 - unauthorized\nnpm error A complete log of this run can be found in: /tmp/x/_logs/debug-0.log",
			"http://127.0.0.1:65026/api/npm/", true,
		},
		{"no lane registry in use (a Next.js project)", npmE401OnTheLane, "", false},
		{
			"E401 on another registry",
			"npm error code E401\nnpm error 401 Unauthorized - GET https://npm.pkg.github.com/@hemfrid%2fworkers-manifest - unauthenticated",
			laneRegistry, false,
		},
		{
			"another host that only starts with the Hub's URL",
			"npm error code E401\nnpm error 401 Unauthorized - GET https://hub.keytolabs.com.evil.example/api/npm/@hemfrid%2fx - no",
			laneRegistry, false,
		},
		{
			"a 404 on the Hub's registry for a package named app-401",
			"npm error code E404\nnpm error 404 Not Found - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fapp-401 - not found",
			laneRegistry, false,
		},
		{
			"E403 on the Hub's registry",
			"npm error code E403\nnpm error 403 Forbidden - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fx - forbidden",
			laneRegistry, false,
		},
		{
			"E401 as text in a script's own output, not npm's error lines",
			"> postinstall\ncode E401\n401 Unauthorized - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fx - unauthorized",
			laneRegistry, false,
		},
		{
			"the code line and the URL line belong to different errors",
			"npm error code E401\nnpm error 401 Unauthorized - GET https://registry.npmjs.org/private-pkg - no\nnpm warn deprecated see https://hub.keytolabs.com/api/npm/@hemfrid%2fx",
			laneRegistry, false,
		},
		{
			"a path that only starts with the registry's path",
			"npm error code E401\nnpm error 401 Unauthorized - GET https://hub.keytolabs.com/api/npm-mirror/@hemfrid%2fx - no",
			"https://hub.keytolabs.com/api/npm", false,
		},
		{
			"a 401 line on the Hub's registry without npm's E401 code",
			"npm error code ECONNRESET\nnpm error 401 Unauthorized - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fx - retried",
			laneRegistry, false,
		},
		{
			"E401 appears, but not as npm's code line (a package named pkg-E401, a script's output)",
			"npm error code E404\n> postinstall E401\nnpm error 401 Unauthorized - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fpkg-E401 - x",
			laneRegistry, false,
		},
		{
			"npm 11.12.1's coloured lines (--color=always, byte form captured 2026-11-15)",
			"\x1b[1mnpm\x1b[22m \x1b[31merror\x1b[39m \x1b[94mcode\x1b[39m E401\n\x1b[1mnpm\x1b[22m \x1b[31merror\x1b[39m 401 Unauthorized - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fx - unauthorized",
			laneRegistry, true,
		},
		{"empty output", "", laneRegistry, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := npmLaneRefusedToken(c.output, c.registry); got != c.want {
				t.Errorf("npmLaneRefusedToken = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNpmInstallError_ARefusedLaneTokenIsAHintNotAProvenHub401(t *testing.T) {
	exit := errors.New("exit status 1")
	err := npmInstallError(exit, npmE401OnTheLane, laneRegistry)
	if !isNpmLaneRefused(err) {
		t.Errorf("want the lane hint, got: %v", err)
	}
	// npm's output can be written by any package in the tree (npm re-emits a
	// dependency's postinstall output as "npm error" lines), so on its own it
	// never counts as a 401 from the Hub.
	if hub.IsUnauthorized(err) || isAuthError(err) {
		t.Errorf("the hint alone must not be an auth error: %v", err)
	}
	if !errors.Is(err, exit) {
		t.Errorf("the npm exit error should stay in the chain: %v", err)
	}
	if strings.Contains(err.Error(), "@hemfrid%2fworkers-manifest") {
		t.Errorf("npm's output is already on the terminal; the error should not repeat it: %v", err)
	}
	if !isNpmLaneRefused(fmt.Errorf("install: %w", err)) {
		t.Error("the hint should survive wrapping")
	}
}

func TestNpmLaneHint_MessageDoesNotRepeatBootsPrefix(t *testing.T) {
	// boot.Run prints this as "npm install: <message>".
	err := npmInstallError(errors.New("exit status 1"), npmE401OnTheLane, laneRegistry)
	if got, want := err.Error(), "the Hub's npm registry refused the stored credential (E401): exit status 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNpmInstallArgs(t *testing.T) {
	cases := []struct {
		name     string
		sub      string
		registry string
		colour   bool
		want     []string
	}{
		{"no lane: npm's own arguments only", "ci", "", false, []string{"ci"}},
		{"no lane, on a terminal: still nothing added (npm keeps the terminal)", "install", "", true, []string{"install"}},
		{"lane, not a terminal", "ci", laneRegistry, false, []string{"ci", "--@hemfrid:registry=" + laneRegistry}},
		{"lane, on a terminal: colour is asked for, since npm now writes to a pipe", "ci", laneRegistry, true, []string{"ci", "--@hemfrid:registry=" + laneRegistry, "--color=always"}},
	}
	for _, c := range cases {
		got := npmInstallArgs(c.sub, c.registry, c.colour)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNpmInstallError_AnythingElseIsTheNpmErrorUnchanged(t *testing.T) {
	exit := errors.New("exit status 1")
	for _, out := range []string{
		"npm error code E404\nnpm error 404 Not Found - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fapp-401 - not found",
		"npm error code E401\nnpm error 401 Unauthorized - GET https://npm.pkg.github.com/@hemfrid%2fx - no",
		"",
	} {
		err := npmInstallError(exit, out, laneRegistry)
		if err != exit {
			t.Errorf("want the npm error unchanged, got: %v", err)
		}
		if isAuthError(err) || isNpmLaneRefused(err) {
			t.Errorf("must not be an auth error or a lane hint: %v", err)
		}
	}
	if npmInstallError(nil, npmE401OnTheLane, laneRegistry) != nil {
		t.Error("a successful install has no error")
	}
}

func TestTailBuffer_KeepsOnlyTheLastBytes(t *testing.T) {
	b := &tailBuffer{max: 10}
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("abcde"))
	if got := b.String(); got != "56789abcde" {
		t.Errorf("tail = %q, want %q", got, "56789abcde")
	}
	n, err := b.Write([]byte(strings.Repeat("x", 100)))
	if n != 100 || err != nil {
		t.Errorf("Write must report the whole slice as written, got %d, %v", n, err)
	}
	if got := b.String(); got != strings.Repeat("x", 10) {
		t.Errorf("tail = %q", got)
	}
}

// fakeNpm puts an `npm` on PATH that prints output to stderr and exits 1.
func fakeNpm(t *testing.T, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake npm is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >&2 <<'OUT'\n" + output + "\nOUT\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNpmInstall_ARefusedLaneTokenComesBackAsTheLaneHint(t *testing.T) {
	fakeNpm(t, npmE401OnTheLane)
	err := npmInstall(context.Background(), t.TempDir(), laneRegistry)
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if !isNpmLaneRefused(err) {
		t.Errorf("want the lane hint, got: %v", err)
	}
}

func TestNpmInstall_APackageNamedApp401NotFoundIsNotAnAuthError(t *testing.T) {
	fakeNpm(t, "npm error code E404\nnpm error 404 Not Found - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fapp-401 - not found")
	err := npmInstall(context.Background(), t.TempDir(), laneRegistry)
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if isAuthError(err) || isNpmLaneRefused(err) {
		t.Errorf("must not be an auth error or a lane hint: %v", err)
	}
}

// ---- the probe: npm's E401 is a hint; only the Hub's own answer is proof ----

// laneHintBoot makes boot fail with the npm lane hint on every run, and
// counts the runs.
func laneHintBoot(t *testing.T) *int {
	t.Helper()
	runs := 0
	orig := bootRun
	bootRun = func(ctx context.Context, d boot.Deps, f boot.Flags) error {
		runs++
		return fmt.Errorf("install: %w", npmInstallError(errors.New("exit status 1"), npmE401OnTheLane, laneRegistry))
	}
	t.Cleanup(func() { bootRun = orig })
	return &runs
}

// stubProbe replaces the Hub probe and counts its calls.
func stubProbe(t *testing.T, answer error) *int {
	t.Helper()
	calls := 0
	orig := probeHubCredential
	probeHubCredential = func(ctx context.Context) error {
		calls++
		return answer
	}
	t.Cleanup(func() { probeHubCredential = orig })
	return &calls
}

func TestBoot_NpmLaneHint_HubConfirmsThe401_ReauthsOnceAndRetries(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)
	runs := laneHintBoot(t)
	probes := stubProbe(t, &hub.StatusError{Op: "list-projects", StatusCode: 401})

	if err := runBootImpl(context.Background(), nil); err == nil {
		t.Fatal("expected the persistent error to be returned")
	}
	if *probes != 1 || *reauthCalls != 1 || *runs != 2 {
		t.Errorf("probes=%d reauth=%d runs=%d, want 1, 1, 2", *probes, *reauthCalls, *runs)
	}
}

func TestBoot_NpmLaneHint_HubAcceptsTheCredential_NoReauth(t *testing.T) {
	// A dependency's postinstall can make npm print the same lines. The Hub
	// accepts the stored credential, so nothing is signed in again.
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)
	runs := laneHintBoot(t)
	probes := stubProbe(t, nil)

	err := runBootImpl(context.Background(), nil)
	if err == nil || !isNpmLaneRefused(err) {
		t.Fatalf("expected npm's error to be returned, got: %v", err)
	}
	if *probes != 1 || *reauthCalls != 0 || *runs != 1 {
		t.Errorf("probes=%d reauth=%d runs=%d, want 1, 0, 1", *probes, *reauthCalls, *runs)
	}
}

func TestBoot_NpmLaneHint_ProbeFailsAnotherWay_NoReauth(t *testing.T) {
	for _, answer := range []error{
		errors.New("dial tcp: connection refused"),
		&hub.StatusError{Op: "list-projects", StatusCode: 503},
		errors.New("list-projects failed: 401 Unauthorized"), // text is not a status
	} {
		reauthCalls := reauthStubs(t, mintedCreds())
		validCreds(t)
		runs := laneHintBoot(t)
		stubProbe(t, answer)

		if err := runBootImpl(context.Background(), nil); err == nil {
			t.Fatal("expected npm's error to be returned")
		}
		if *reauthCalls != 0 || *runs != 1 {
			t.Errorf("probe answer %v: reauth=%d runs=%d, want 0 and 1", answer, *reauthCalls, *runs)
		}
	}
}

func TestBoot_AHub401FromEnvSync_NeedsNoProbe(t *testing.T) {
	reauthCalls := reauthStubs(t, mintedCreds())
	validCreds(t)
	probes := stubProbe(t, nil)
	orig := bootRun
	bootRun = func(ctx context.Context, d boot.Deps, f boot.Flags) error {
		return fmt.Errorf("env sync: %w", &hub.StatusError{Op: "fetch-env-values", StatusCode: 401})
	}
	t.Cleanup(func() { bootRun = orig })

	_ = runBootImpl(context.Background(), nil)
	if *probes != 0 || *reauthCalls != 1 {
		t.Errorf("probes=%d reauth=%d, want 0 and 1", *probes, *reauthCalls)
	}
}

func TestProbeHubCredential_AsksTheHubWithTheStoredCredential(t *testing.T) {
	for _, status := range []int{200, 401, 503} {
		var gotAuth, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"projects":[]}`))
		}))
		t.Setenv("KEYTO_HOME", t.TempDir())
		if err := config.Save(&config.Creds{Credential: "tok_stored", HubURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		err := probeHubCredential(context.Background())
		srv.Close()
		if gotAuth != "Bearer tok_stored" {
			t.Errorf("status %d: Authorization = %q (path %q)", status, gotAuth, gotPath)
		}
		if got, want := hub.IsUnauthorized(err), status == 401; got != want {
			t.Errorf("status %d: IsUnauthorized(%v) = %v, want %v", status, err, got, want)
		}
	}
}

func TestProbeHubCredential_NoStoredCredential_IsNotA401(t *testing.T) {
	t.Setenv("KEYTO_HOME", t.TempDir())
	err := probeHubCredential(context.Background())
	if err == nil || hub.IsUnauthorized(err) {
		t.Errorf("want a non-401 error, got: %v", err)
	}
}
