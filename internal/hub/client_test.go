package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-cli/internal/hub"
)

func TestExchangeToken_Success(t *testing.T) {
	want := hub.TokenResponse{
		Credential: "tok_abc123",
		ExpiresAt:  time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		UserEmail:  "alice@example.com",
		UserName:   "alice",
	}

	var gotMethod, gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	got, err := c.ExchangeToken(context.Background(), "code123", "verifier456", "http://localhost:9876/callback")
	if err != nil {
		t.Fatalf("ExchangeToken() error = %v", err)
	}

	// Assert path and method.
	if gotPath != "/api/cli/token" {
		t.Errorf("server received path %q, want /api/cli/token", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("server received method %q, want POST", gotMethod)
	}

	// Assert request body fields.
	if gotBody["code"] != "code123" {
		t.Errorf("body[code] = %q, want code123", gotBody["code"])
	}
	if gotBody["code_verifier"] != "verifier456" {
		t.Errorf("body[code_verifier] = %q, want verifier456", gotBody["code_verifier"])
	}
	if gotBody["redirect_uri"] != "http://localhost:9876/callback" {
		t.Errorf("body[redirect_uri] = %q, want http://localhost:9876/callback", gotBody["redirect_uri"])
	}

	// Assert parsed response.
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
}

func TestExchangeToken_NonOK_ReturnsError(t *testing.T) {
	const serverBodyText = "you shall not pass"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, serverBodyText, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	_, err := c.ExchangeToken(context.Background(), "code", "verifier", "http://localhost/cb")
	if err == nil {
		t.Fatal("ExchangeToken() expected error on 400, got nil")
	}

	// Must not leak the response body verbatim.
	if strings.Contains(err.Error(), serverBodyText) {
		t.Errorf("error message leaks response body: %v", err)
	}
}

func TestExchangeToken_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := &hub.Client{BaseURL: srv.URL}
	_, err := c.ExchangeToken(ctx, "code", "verifier", "http://localhost/cb")
	if err == nil {
		t.Fatal("ExchangeToken() expected error on cancelled context, got nil")
	}
}
