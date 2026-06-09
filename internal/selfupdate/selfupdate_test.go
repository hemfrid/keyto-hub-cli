package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.1", "v0.1.2", true},
		{"v0.1.2", "v0.1.2", false},
		{"v0.1.2", "v0.1.1", false},
		{"v0.1.9", "v0.2.0", true},
		{"v0.9.9", "v1.0.0", true},
		{"1.0.0", "1.0.1", true},       // leading "v" optional
		{"v0.1.0", "v0.1.1-rc1", true}, // pre-release suffix ignored on latest
		{"dev", "v9.9.9", false},       // unparseable current -> never nag
		{"v0.1.0", "garbage", false},   // unparseable latest -> never nag
		{"v0.1", "v0.1.1", false},      // malformed current -> never nag
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestLatestTag_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("expected a User-Agent header (GitHub requires one)")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","name":"v0.2.0"}`))
	}))
	defer srv.Close()

	tag, err := LatestTag(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v0.2.0" {
		t.Errorf("tag = %q, want v0.2.0", tag)
	}
}

func TestLatestTag_Non200_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := LatestTag(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
}

func TestNotice_ContainsVersionsAndInstall(t *testing.T) {
	n := Notice("v0.1.1", "v0.1.2")
	for _, want := range []string{"v0.1.1", "v0.1.2", "install.sh"} {
		if !strings.Contains(n, want) {
			t.Errorf("Notice missing %q:\n%s", want, n)
		}
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	if !needsRefresh(time.Time{}, now) {
		t.Error("zero checkedAt should need refresh")
	}
	if needsRefresh(now.Add(-1*time.Hour), now) {
		t.Error("a 1h-old check should NOT need refresh")
	}
	if !needsRefresh(now.Add(-25*time.Hour), now) {
		t.Error("a 25h-old check SHOULD need refresh")
	}
}

func TestCacheRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update-check.json")
	want := cache{CheckedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC), Latest: "v0.1.2"}

	if err := saveCache(path, want); err != nil {
		t.Fatalf("saveCache: %v", err)
	}
	got, err := loadCache(path)
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if got.Latest != want.Latest || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("roundtrip = %+v, want %+v", got, want)
	}
}
