package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
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
	if err := ensureNpmLane(context.Background(), nil, out); err != nil {
		t.Fatalf("ensureNpmLane: %v", err)
	}
	got, err := os.ReadFile(npmrcPath)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if !strings.Contains(string(got), "//"+host+"/api/npm/:_authToken=kt_stored\n") {
		t.Fatalf("npmrc lacks the lane auth line:\n%s", got)
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
	if err := ensureNpmLane(context.Background(), fresh, &bytes.Buffer{}); err != nil {
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
	err := ensureNpmLane(context.Background(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "keyto auth") {
		t.Fatalf("want a keyto auth tip, got %v", err)
	}
	if _, statErr := os.Lstat(npmrcPath); !os.IsNotExist(statErr) {
		t.Fatalf("nothing may be written when signed out (err %v)", statErr)
	}
}
