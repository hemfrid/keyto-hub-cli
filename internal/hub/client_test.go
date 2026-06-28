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

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
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

func TestListProjects_Success(t *testing.T) {
	want := []hub.Project{
		{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", Role: "owner"},
		{Name: "beta-api", Org: "hemfrid", Repo: "beta-api", Role: "member"},
	}

	var gotMethod, gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"projects": want})
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_test123"}
	got, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}

	if gotPath != "/api/cli/projects" {
		t.Errorf("server received path %q, want /api/cli/projects", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("server received method %q, want GET", gotMethod)
	}
	if gotAuth != "Bearer tok_test123" {
		t.Errorf("Authorization header = %q, want Bearer tok_test123", gotAuth)
	}

	if len(got) != len(want) {
		t.Fatalf("got %d projects, want %d", len(got), len(want))
	}
	for i, p := range want {
		if got[i].Name != p.Name || got[i].Org != p.Org || got[i].Repo != p.Repo || got[i].Role != p.Role {
			t.Errorf("projects[%d]: got %+v, want %+v", i, got[i], p)
		}
	}
}

func TestListProjects_Unauthorized_ReturnsError(t *testing.T) {
	const serverBody = "unauthorized"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, serverBody, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	_, err := c.ListProjects(context.Background())
	if err == nil {
		t.Fatal("ListProjects() expected error on 401, got nil")
	}

	// Must not leak the response body verbatim.
	if strings.Contains(err.Error(), serverBody) {
		t.Errorf("error message leaks response body: %v", err)
	}
}

// ---- FetchEnvValues ----

func TestFetchEnvValues_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"env":     "uat",
			"values":  map[string]string{"SENDGRID_API_KEY": "sg_abc123"},
			"missing": []string{"OTHER_KEY"},
		})
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_test"}
	values, missing, err := c.FetchEnvValues(context.Background(), "hemfrid", "acme-web", "uat", []string{"SENDGRID_API_KEY", "OTHER_KEY"})
	if err != nil {
		t.Fatalf("FetchEnvValues() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/cli/projects/hemfrid/acme-web/env/uat/values"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != "Bearer tok_test" {
		t.Errorf("Authorization = %q, want Bearer tok_test", gotAuth)
	}

	// Verify the request body contained the keys array.
	if keysRaw, ok := gotBody["keys"]; !ok {
		t.Error("request body missing 'keys' field")
	} else {
		keys := keysRaw.([]interface{})
		if len(keys) != 2 || keys[0].(string) != "SENDGRID_API_KEY" || keys[1].(string) != "OTHER_KEY" {
			t.Errorf("request body keys = %v, want [SENDGRID_API_KEY OTHER_KEY]", keys)
		}
	}

	if values["SENDGRID_API_KEY"] != "sg_abc123" {
		t.Errorf("values[SENDGRID_API_KEY] = %q, want sg_abc123", values["SENDGRID_API_KEY"])
	}
	if len(missing) != 1 || missing[0] != "OTHER_KEY" {
		t.Errorf("missing = %v, want [OTHER_KEY]", missing)
	}
}

func TestFetchEnvValues_EmptyKeys_NoOp(t *testing.T) {
	// An empty keys array is a valid no-op: server responds 200 with empty values+missing.
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"env":     "uat",
			"values":  map[string]string{},
			"missing": []string{},
		})
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_test"}
	values, missing, err := c.FetchEnvValues(context.Background(), "hemfrid", "acme-web", "uat", []string{})
	if err != nil {
		t.Fatalf("FetchEnvValues() error = %v", err)
	}
	if len(values) != 0 {
		t.Errorf("values not empty: %v", values)
	}
	if len(missing) != 0 {
		t.Errorf("missing not empty: %v", missing)
	}

	// Wire format must be {"keys":[]} — not null — so an empty slice is sent explicitly.
	if keysRaw, ok := gotBody["keys"]; !ok {
		t.Error("request body missing 'keys' field")
	} else {
		keys, ok := keysRaw.([]interface{})
		if !ok || keys == nil {
			t.Errorf("request body 'keys' is not a JSON array: %v", keysRaw)
		} else if len(keys) != 0 {
			t.Errorf("request body keys = %v, want empty array []", keys)
		}
	}
}

func TestFetchEnvValues_Unauthorized_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	_, _, err := c.FetchEnvValues(context.Background(), "hemfrid", "acme-web", "uat", []string{"KEY"})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	// Must surface a recognizable status; must not leak the body.
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should reference 401, got: %v", err)
	}
	if strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error must not leak response body: %v", err)
	}
}

func TestFetchEnvValues_Forbidden_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	_, _, err := c.FetchEnvValues(context.Background(), "hemfrid", "acme-web", "uat", []string{"KEY"})
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should reference 403, got: %v", err)
	}
}

func TestFetchEnvValues_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &hub.Client{BaseURL: srv.URL}
	_, _, err := c.FetchEnvValues(ctx, "hemfrid", "acme-web", "uat", []string{"KEY"})
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

// ---- SetEnvValues ----

func TestSetEnvValues_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody struct {
		Values map[string]string `json:"values"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"env": "uat", "updated": []string{"API_KEY"}})
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_xyz"}
	updated, err := c.SetEnvValues(context.Background(), "hemfrid", "acme-web", "uat",
		map[string]string{"API_KEY": "sk-123"})
	if err != nil {
		t.Fatalf("SetEnvValues() error = %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	wantPath := "/api/cli/projects/hemfrid/acme-web/env/uat/values"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != "Bearer tok_xyz" {
		t.Errorf("Authorization = %q, want Bearer tok_xyz", gotAuth)
	}
	if gotBody.Values["API_KEY"] != "sk-123" {
		t.Errorf("request body values = %v", gotBody.Values)
	}
	if len(updated) != 1 || updated[0] != "API_KEY" {
		t.Errorf("updated = %v, want [API_KEY]", updated)
	}
}

func TestSetEnvValues_Non2xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("secret detail that must not leak"))
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok"}
	_, err := c.SetEnvValues(context.Background(), "o", "r", "prod", map[string]string{"K": "V"})
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should reference 403, got: %v", err)
	}
	if strings.Contains(err.Error(), "secret detail") {
		t.Errorf("error must not leak response body: %v", err)
	}
}

// ---- PostDiagnostics ----

func TestPostDiagnostics_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer srv.Close()

	payload := map[string]interface{}{
		"schema_version": 1,
		"cli_version":    "v0.4.0",
		"os":             "darwin",
		"checks":         []interface{}{},
	}

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_diag"}
	if err := c.PostDiagnostics(context.Background(), payload); err != nil {
		t.Fatalf("PostDiagnostics() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/cli/diagnostics" {
		t.Errorf("path = %q, want /api/cli/diagnostics", gotPath)
	}
	if gotAuth != "Bearer tok_diag" {
		t.Errorf("Authorization = %q, want Bearer tok_diag", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if v, _ := gotBody["schema_version"].(float64); v != 1 {
		t.Errorf("body schema_version = %v, want 1", gotBody["schema_version"])
	}
}

func TestPostDiagnostics_Unauthorized_ReturnsError(t *testing.T) {
	const serverBody = "unauthorized"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, serverBody, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_diag"}
	err := c.PostDiagnostics(context.Background(), map[string]interface{}{"schema_version": 1})
	if err == nil {
		t.Fatal("PostDiagnostics() expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should reference 401, got: %v", err)
	}
	// Must not leak the response body verbatim.
	if strings.Contains(err.Error(), serverBody) {
		t.Errorf("error message leaks response body: %v", err)
	}
}

func TestPostDiagnostics_Unprocessable_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid payload", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_diag"}
	err := c.PostDiagnostics(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("PostDiagnostics() expected error on 422, got nil")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error should reference 422, got: %v", err)
	}
}

func TestPostDiagnostics_NoCredential_OmitsAuthHeader(t *testing.T) {
	var hadAuth bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL} // no credential
	if err := c.PostDiagnostics(context.Background(), map[string]interface{}{"schema_version": 1}); err != nil {
		t.Fatalf("PostDiagnostics() error = %v", err)
	}
	if hadAuth {
		t.Error("Authorization header should be omitted when no credential is set")
	}
}

func TestPostDiagnostics_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &hub.Client{BaseURL: srv.URL, Credential: "tok_diag"}
	if err := c.PostDiagnostics(ctx, map[string]interface{}{}); err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}
