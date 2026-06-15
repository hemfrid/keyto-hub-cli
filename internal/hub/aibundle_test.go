package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAIBundleMeta(t *testing.T) {
	want := AIBundleMeta{
		Tag:         "v0.2.0",
		PublishedAt: "2026-06-12T00:00:00Z",
		SourceRepo:  "hemfrid/ai-capabilities",
		Manifest: AIBundleManifest{
			Tag:   "v0.2.0",
			Files: []AIBundleManifestFile{{Path: ".claude/agents/x.md", SHA256: "ff"}},
		},
	}
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Credential: "cred-1"}
	got, err := c.AIBundleMeta(context.Background())
	if err != nil {
		t.Fatalf("AIBundleMeta: %v", err)
	}
	if gotPath != "/api/ai-bundle/meta" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer cred-1" {
		t.Errorf("auth = %q", gotAuth)
	}
	if got.Tag != want.Tag || len(got.Manifest.Files) != 1 {
		t.Errorf("meta = %+v", got)
	}
}

func TestAIBundleMetaUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "expired"}
	if _, err := c.AIBundleMeta(context.Background()); err == nil {
		t.Error("expected error on 401")
	}
}

func TestAIBundleTarballUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Credential: "expired"}
	if _, err := c.AIBundleTarball(context.Background(), "v0.2.0"); err == nil {
		t.Error("expected error on 401")
	}
}

func TestAIBundleTarball(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write([]byte("GZIP"))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Credential: "cred-1"}
	buf, err := c.AIBundleTarball(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatalf("AIBundleTarball: %v", err)
	}
	if string(buf) != "GZIP" {
		t.Errorf("body = %q", buf)
	}
	if gotQuery != "tag=v0.2.0" {
		t.Errorf("query = %q", gotQuery)
	}
}
