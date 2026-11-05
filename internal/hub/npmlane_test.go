package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNpmLaneServed(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		want        bool
		wantDetail  string
	}{
		{"lane deployed: 401 JSON", http.StatusUnauthorized, "application/json", true, ""},
		{"no lane: Next.js 404 page", http.StatusNotFound, "text/html; charset=utf-8", false, "HTTP 404"},
		{"401 that isn't the route's JSON", http.StatusUnauthorized, "text/html", false, "HTTP 401"},
		{"server error", http.StatusBadGateway, "application/json", false, "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			}))
			defer srv.Close()

			c := &Client{BaseURL: srv.URL, Credential: "kt_secret"}
			served, detail := c.NpmLaneServed(context.Background())
			if served != tc.want || !strings.Contains(detail, tc.wantDetail) {
				t.Fatalf("NpmLaneServed = %v, %q; want %v, %q", served, detail, tc.want, tc.wantDetail)
			}
			if gotPath != "/api/npm/@hemfrid/workers-auth" {
				t.Errorf("probed %q, want an allow-listed lane package", gotPath)
			}
			if gotAuth != "" {
				t.Errorf("the probe must not send the credential, sent %q", gotAuth)
			}
		})
	}
}

func TestNpmLaneServed_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	served, detail := (&Client{BaseURL: url}).NpmLaneServed(context.Background())
	if served || detail == "" || strings.Contains(detail, "kt_") {
		t.Fatalf("unreachable Hub: got %v, %q", served, detail)
	}
}
