package hub_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

func TestDebug_SendsBearerAndQuery(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pod":"web-1","container":"app","previous":true,"lines":["a","b"],"truncated":false}`))
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL, Credential: "cred-test"}
	var logs hub.DebugLogs
	err := c.Debug(context.Background(), "demo app", "logs", url.Values{"env": {"prod"}, "previous": {"true"}}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/projects/demo%20app/debug/logs" && gotPath != "/api/projects/demo app/debug/logs" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "env=prod&previous=true" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer cred-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if logs.Pod != "web-1" || len(logs.Lines) != 2 || !logs.Previous {
		t.Fatalf("decoded = %+v", logs)
	}
}

func TestDebug_TypedErrorFromJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"debug_not_enabled","message":"not yet"}`))
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	err := c.Debug(context.Background(), "demo", "pods", nil, &[]hub.DebugPod{})
	var de *hub.DebugError
	if !errors.As(err, &de) {
		t.Fatalf("want *DebugError, got %v", err)
	}
	if de.Status != 409 || de.Code != "debug_not_enabled" || de.Message != "not yet" {
		t.Fatalf("got %+v", de)
	}
}

func TestDebug_NonJSONErrorDoesNotLeakBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>internal stack trace</html>"))
	}))
	defer srv.Close()

	c := &hub.Client{BaseURL: srv.URL}
	err := c.Debug(context.Background(), "demo", "pods", nil, &[]hub.DebugPod{})
	var de *hub.DebugError
	if !errors.As(err, &de) || de.Status != 502 || de.Message != "" {
		t.Fatalf("got %+v", err)
	}
	if strings.Contains(err.Error(), "stack trace") {
		t.Fatalf("error leaked body: %v", err)
	}
}

func TestDebug_ErrorMessageSanitisedAtDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"e\u001b]0;x\u0007","message":"bo\u001b]52;c;eA==\u0007om\nline"}`))
	}))
	defer srv.Close()
	c := &hub.Client{BaseURL: srv.URL}
	err := c.Debug(context.Background(), "demo", "pods", nil, &[]hub.DebugPod{})
	var de *hub.DebugError
	if !errors.As(err, &de) || de.Message != "boom line" || de.Code != "e" || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("got %+v / %q", de, err)
	}
}
