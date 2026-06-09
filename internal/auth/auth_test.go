package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hemfrid/keyto-cli/internal/auth"
	"github.com/hemfrid/keyto-cli/internal/hub"
)

// fakeTokenServer returns an httptest.Server that serves a fixed TokenResponse
// on POST /api/cli/token and records the request body.
func fakeTokenServer(t *testing.T, tr hub.TokenResponse) (*httptest.Server, *map[string]string) {
	t.Helper()
	body := &map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/token" {
			http.NotFound(w, r)
			return
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tr)
	}))
	return srv, body
}

// TestRun_HappyPath simulates a full loopback login:
//  1. Run binds a loopback port and builds an auth URL.
//  2. The injected OpenURL function (instead of a real browser) parses the
//     auth URL, verifies the required query params, then performs the Hub's
//     role by GETting the callback URL with the correct code+state.
//  3. A fake httptest server stands in for the Hub's /api/cli/token endpoint.
//  4. Run must return a TokenResponse matching what the fake server returns.
func TestRun_HappyPath(t *testing.T) {
	want := hub.TokenResponse{
		Credential: "tok_happy",
		ExpiresAt:  time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		UserEmail:  "alice@example.com",
		UserName:   "alice",
	}

	hubSrv, body := fakeTokenServer(t, want)
	defer hubSrv.Close()

	openURL := func(rawURL string) error {
		// Parse the auth URL that Run built.
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("OpenURL: bad URL %q: %w", rawURL, err)
		}
		q := u.Query()

		// Assert required parameters are present.
		if q.Get("redirect_uri") == "" {
			return fmt.Errorf("OpenURL: missing redirect_uri in %q", rawURL)
		}
		if q.Get("state") == "" {
			return fmt.Errorf("OpenURL: missing state in %q", rawURL)
		}
		if q.Get("code_challenge") == "" {
			return fmt.Errorf("OpenURL: missing code_challenge in %q", rawURL)
		}
		if q.Get("code_challenge_method") != "S256" {
			return fmt.Errorf("OpenURL: code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
		}

		// Assert redirect_uri points to the loopback.
		ruri, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return fmt.Errorf("OpenURL: bad redirect_uri: %w", err)
		}
		if ruri.Hostname() != "127.0.0.1" {
			return fmt.Errorf("OpenURL: redirect_uri host = %q, want 127.0.0.1", ruri.Hostname())
		}
		if ruri.Path != "/callback" {
			return fmt.Errorf("OpenURL: redirect_uri path = %q, want /callback", ruri.Path)
		}

		// Simulate the Hub: GET the callback with ?code=THE_CODE&state=<same state>
		callbackURL := q.Get("redirect_uri") + "?code=test_code_abc&state=" + url.QueryEscape(q.Get("state"))
		resp, err := http.Get(callbackURL) //nolint:noctx
		if err != nil {
			return fmt.Errorf("OpenURL: callback GET failed: %w", err)
		}
		resp.Body.Close()
		return nil
	}

	got, err := auth.Run(context.Background(), auth.Options{
		HubURL:  hubSrv.URL,
		OpenURL: openURL,
		HTTP:    hubSrv.Client(),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got.Credential != want.Credential {
		t.Errorf("Credential = %q, want %q", got.Credential, want.Credential)
	}
	if got.UserEmail != want.UserEmail {
		t.Errorf("UserEmail = %q, want %q", got.UserEmail, want.UserEmail)
	}
	if got.UserName != want.UserName {
		t.Errorf("UserName = %q, want %q", got.UserName, want.UserName)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}

	// Verify the token endpoint received the code and verifier (non-empty).
	if (*body)["code"] != "test_code_abc" {
		t.Errorf("token endpoint: code = %q, want test_code_abc", (*body)["code"])
	}
	if (*body)["code_verifier"] == "" {
		t.Error("token endpoint: code_verifier is empty")
	}
	if (*body)["redirect_uri"] == "" {
		t.Error("token endpoint: redirect_uri is empty")
	}
}

// TestRun_MismatchedState verifies that Run returns an error (and does NOT
// proceed to token exchange) when the callback arrives with the wrong state.
func TestRun_MismatchedState(t *testing.T) {
	// This token server should NOT be called; if it is, the test passes trivially
	// but we track it to make the violation visible.
	tokenCalled := false
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/token" {
			tokenCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer hubSrv.Close()

	openURL := func(rawURL string) error {
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("OpenURL: bad URL %q: %w", rawURL, err)
		}
		q := u.Query()
		redirectURI := q.Get("redirect_uri")
		// Send the WRONG state.
		callbackURL := redirectURI + "?code=legit_code&state=WRONG_STATE"
		resp, err := http.Get(callbackURL) //nolint:noctx
		if err != nil {
			return fmt.Errorf("OpenURL: callback GET failed: %w", err)
		}
		resp.Body.Close()
		return nil
	}

	_, err := auth.Run(context.Background(), auth.Options{
		HubURL:  hubSrv.URL,
		OpenURL: openURL,
		HTTP:    hubSrv.Client(),
		Timeout: 10 * time.Second,
	})

	if err == nil {
		t.Fatal("Run() expected error on state mismatch, got nil")
	}
	if tokenCalled {
		t.Error("Run() called the token endpoint despite mismatched state — should have aborted")
	}
}

// TestRun_OAuthError verifies that Run returns an error (and does NOT call the
// token endpoint) when the callback arrives with ?error=access_denied and no
// code — simulating an authorization denial by the user or provider.
func TestRun_OAuthError(t *testing.T) {
	tokenCalled := false
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/token" {
			tokenCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer hubSrv.Close()

	openURL := func(rawURL string) error {
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("OpenURL: bad URL %q: %w", rawURL, err)
		}
		q := u.Query()
		redirectURI := q.Get("redirect_uri")
		// Simulate a denial: correct state, error param, no code.
		callbackURL := redirectURI + "?error=access_denied&state=" + url.QueryEscape(q.Get("state"))
		resp, err := http.Get(callbackURL) //nolint:noctx
		if err != nil {
			return fmt.Errorf("OpenURL: callback GET failed: %w", err)
		}
		resp.Body.Close()
		return nil
	}

	_, err := auth.Run(context.Background(), auth.Options{
		HubURL:  hubSrv.URL,
		OpenURL: openURL,
		HTTP:    hubSrv.Client(),
		Timeout: 10 * time.Second,
	})

	if err == nil {
		t.Fatal("Run() expected error on OAuth denial, got nil")
	}
	if tokenCalled {
		t.Error("Run() called the token endpoint despite OAuth error — should have aborted")
	}
}

// TestRun_Timeout verifies that Run returns an error when OpenURL does nothing
// (simulating an unresponsive browser) and the Timeout fires.
func TestRun_Timeout(t *testing.T) {
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer hubSrv.Close()

	// OpenURL does nothing — no callback will arrive.
	openURL := func(_ string) error { return nil }

	start := time.Now()
	_, err := auth.Run(context.Background(), auth.Options{
		HubURL:  hubSrv.URL,
		OpenURL: openURL,
		HTTP:    hubSrv.Client(),
		Timeout: 150 * time.Millisecond, // very short for tests
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run() expected timeout error, got nil")
	}
	// Sanity: it should have finished in under 3 seconds.
	if elapsed > 3*time.Second {
		t.Errorf("Run() took %v, expected to finish quickly on timeout", elapsed)
	}
}
