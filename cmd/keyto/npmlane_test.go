package main

import (
	"bytes"
	"context"
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
	"github.com/hemfrid/keyto-hub-cli/internal/npmrc"
)

// laneHub is a fake Hub whose /api/npm/ route answers like keyto-hub#825.
func laneHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the lane probe sent a credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureNpmLane_LoadsStoredCredsAndWritesUserNpmrc(t *testing.T) {
	srv := laneHub(t)
	t.Setenv("KEYTO_HOME", t.TempDir())
	npmrcPath := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("npm_config_userconfig", npmrcPath)
	if err := config.Save(&config.Creds{Credential: "kt_stored", HubURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	registry, err := ensureNpmLane(context.Background(), nil, out)
	if err != nil {
		t.Fatalf("ensureNpmLane: %v", err)
	}
	if registry != srv.URL+"/api/npm/" {
		t.Fatalf("registry = %q, want the lane", registry)
	}
	got, err := os.ReadFile(npmrcPath)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if !strings.Contains(string(got), "//"+host+"/api/npm/:_authToken=kt_stored\n") {
		t.Fatalf("npmrc lacks the lane auth line:\n%s", got)
	}
	if strings.Contains(string(got), "@hemfrid:registry") {
		t.Fatalf("npmrc must not map @hemfrid user-wide:\n%s", got)
	}
	if strings.Contains(out.String(), "kt_stored") {
		t.Fatalf("the token was printed: %s", out.String())
	}
}

func TestEnsureNpmLane_PrefersThreadedCreds(t *testing.T) {
	srv := laneHub(t)
	t.Setenv("KEYTO_HOME", t.TempDir()) // nothing stored
	npmrcPath := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("npm_config_userconfig", npmrcPath)

	fresh := &config.Creds{Credential: "kt_fresh", HubURL: srv.URL}
	if _, err := ensureNpmLane(context.Background(), fresh, &bytes.Buffer{}); err != nil {
		t.Fatalf("ensureNpmLane: %v", err)
	}
	if got, _ := os.ReadFile(npmrcPath); !strings.Contains(string(got), ":_authToken=kt_fresh\n") {
		t.Fatalf("npmrc should carry the re-auth credential:\n%s", got)
	}
}

func TestEnsureNpmLane_NotSignedIn_Tip(t *testing.T) {
	t.Setenv("KEYTO_HOME", t.TempDir())
	npmrcPath := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("npm_config_userconfig", npmrcPath)
	_, err := ensureNpmLane(context.Background(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "keyto auth") {
		t.Fatalf("want a keyto auth tip, got %v", err)
	}
	if _, statErr := os.Lstat(npmrcPath); !os.IsNotExist(statErr) {
		t.Fatalf("nothing may be written when signed out (err %v)", statErr)
	}
}

func TestNpmLaneEnv(t *testing.T) {
	if got := npmLaneEnv(""); got != nil {
		t.Fatalf("no lane must add no env, got %v", got)
	}
	got := npmLaneEnv("https://hub.example.com/api/npm/")
	if len(got) != 1 || got[0] != "npm_config_@hemfrid:registry=https://hub.example.com/api/npm/" {
		t.Fatalf("npmLaneEnv = %v", got)
	}
}

// TestBootDeps_InstallGetsTheScopeOnlyThroughItsEnv runs the real NpmLane and
// Install closures against a fake Hub and a fake npm on PATH: the scope reaches
// the npm process through its environment, and the user's npm config (an old
// v0.9.x block in it) ends up with the credential line and no scope line.
func TestBootDeps_InstallGetsTheScopeOnlyThroughItsEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake npm is a shell script")
	}
	srv := laneHub(t)
	t.Setenv("KEYTO_HOME", t.TempDir())
	if err := config.Save(&config.Creds{Credential: "kt_stored", HubURL: srv.URL, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	npmrcPath := filepath.Join(t.TempDir(), "npmrc")
	userOwn := "@hemfrid:registry=https://npm.pkg.github.com\n"
	legacy := npmrc.BeginMarker + "\n@hemfrid:registry=" + srv.URL + "/api/npm/\n//" + host + "/api/npm/:_authToken=kt_stored\n" + npmrc.EndMarker + "\n"
	if err := os.WriteFile(npmrcPath, []byte(userOwn+legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("npm_config_userconfig", npmrcPath)

	bin := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "npm-env")
	fakeNpm := "#!/bin/sh\nenv > '" + envOut + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(fakeNpm), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := bootDeps(context.Background(), t.TempDir(), boot.Flags{}, nil)
	if err := d.NpmLane(context.Background()); err != nil {
		t.Fatalf("NpmLane: %v", err)
	}
	if err := d.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if want := "npm_config_@hemfrid:registry=" + srv.URL + "/api/npm/\n"; !strings.Contains(string(env), want) {
		t.Fatalf("npm's env lacks %q:\n%s", want, env)
	}
	got, _ := os.ReadFile(npmrcPath)
	wantNpmrc := userOwn + npmrc.BeginMarker + "\n//" + host + "/api/npm/:_authToken=kt_stored\n" + npmrc.EndMarker + "\n"
	if string(got) != wantNpmrc {
		t.Fatalf("npmrc =\n%s\nwant\n%s", got, wantNpmrc)
	}
}
