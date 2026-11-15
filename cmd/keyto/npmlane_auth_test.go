package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestNpmInstallError_ARefusedLaneTokenIsATyped401(t *testing.T) {
	exit := errors.New("exit status 1")
	err := npmInstallError(exit, npmE401OnTheLane, laneRegistry)
	if !hub.IsUnauthorized(err) || !isAuthError(err) {
		t.Errorf("a refused lane token should be an auth error, got: %v", err)
	}
	if !errors.Is(err, exit) {
		t.Errorf("the npm exit error should stay in the chain: %v", err)
	}
	if strings.Contains(err.Error(), "@hemfrid%2fworkers-manifest") {
		t.Errorf("npm's output is already on the terminal; the error should not repeat it: %v", err)
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
		if isAuthError(err) {
			t.Errorf("must not be an auth error: %v", err)
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

func TestNpmInstall_ARefusedLaneTokenComesBackAsAnAuthError(t *testing.T) {
	fakeNpm(t, npmE401OnTheLane)
	err := npmInstall(context.Background(), t.TempDir(), laneRegistry)
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if !isAuthError(err) {
		t.Errorf("want an auth error, got: %v", err)
	}
}

func TestNpmInstall_APackageNamedApp401NotFoundIsNotAnAuthError(t *testing.T) {
	fakeNpm(t, "npm error code E404\nnpm error 404 Not Found - GET https://hub.keytolabs.com/api/npm/@hemfrid%2fapp-401 - not found")
	err := npmInstall(context.Background(), t.TempDir(), laneRegistry)
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if isAuthError(err) {
		t.Errorf("must not be an auth error: %v", err)
	}
}
