package hub_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

func statusClient(t *testing.T, status int) *hub.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	}))
	t.Cleanup(srv.Close)
	return &hub.Client{BaseURL: srv.URL, Credential: "tok"}
}

// Every Hub call that can be refused, as the commands reach it.
func refusedCalls() map[string]func(c *hub.Client) error {
	ctx := context.Background()
	return map[string]func(c *hub.Client) error{
		"ExchangeToken": func(c *hub.Client) error {
			_, err := c.ExchangeToken(ctx, "code", "verifier", "http://127.0.0.1/cb")
			return err
		},
		"ListProjects": func(c *hub.Client) error { _, err := c.ListProjects(ctx); return err },
		"FetchEnvValues": func(c *hub.Client) error {
			_, _, err := c.FetchEnvValues(ctx, "o", "r", "uat", []string{"API_KEY"})
			return err
		},
		"SetEnvValues": func(c *hub.Client) error {
			_, err := c.SetEnvValues(ctx, "o", "r", "uat", map[string]string{"API_KEY": "v"})
			return err
		},
		"PostDiagnostics": func(c *hub.Client) error { return c.PostDiagnostics(ctx, map[string]string{"a": "b"}) },
		"AIBundleMeta":    func(c *hub.Client) error { _, err := c.AIBundleMeta(ctx); return err },
		"AIBundleTarball": func(c *hub.Client) error { _, err := c.AIBundleTarball(ctx, "v1.0.0"); return err },
	}
}

func TestIsUnauthorized_TrueForA401FromEveryHubCall(t *testing.T) {
	for name, call := range refusedCalls() {
		t.Run(name, func(t *testing.T) {
			err := call(statusClient(t, http.StatusUnauthorized))
			if err == nil {
				t.Fatal("expected an error on 401")
			}
			if !hub.IsUnauthorized(err) {
				t.Errorf("IsUnauthorized(%v) = false, want true", err)
			}
			// The commands wrap it on the way up.
			wrapped := fmt.Errorf("env sync: %w", fmt.Errorf("env sync: fetch values from Hub: %w", err))
			if !hub.IsUnauthorized(wrapped) {
				t.Errorf("IsUnauthorized(wrapped %v) = false, want true", wrapped)
			}
		})
	}
}

func TestIsUnauthorized_FalseForEveryOtherStatus(t *testing.T) {
	for _, status := range []int{400, 403, 404, 409, 500, 503} {
		for name, call := range refusedCalls() {
			t.Run(fmt.Sprintf("%s %d", name, status), func(t *testing.T) {
				err := call(statusClient(t, status))
				if err == nil {
					t.Fatalf("expected an error on %d", status)
				}
				if hub.IsUnauthorized(err) {
					t.Errorf("IsUnauthorized(%v) = true, want false", err)
				}
			})
		}
	}
}

func TestIsUnauthorized_NeverMatchesOnText(t *testing.T) {
	cases := []error{
		nil,
		errors.New("list-projects failed: 401 Unauthorized"), // the text alone is not a status
		errors.New(`project "app-401" not found or you are not a member`),
		errors.New("clone https://hub.example/git/hemfrid/app-401.git: exit status 128"),
		errors.New("exit status 1: npm error code E401 Unauthorized"),
		errors.New("request rejected: unauthorized"),
		&hub.DebugError{Status: 500, Message: "upstream said 401 unauthorized"},
	}
	for _, err := range cases {
		if hub.IsUnauthorized(err) {
			t.Errorf("IsUnauthorized(%v) = true, want false", err)
		}
	}
}

func TestIsUnauthorized_TrueForTheTypedErrorsThatCarry401(t *testing.T) {
	cases := []error{
		&hub.StatusError{Op: "list-projects", StatusCode: 401},
		&hub.SetEnvValuesError{StatusCode: 401, Status: "401 Unauthorized"},
		&hub.DebugError{Status: 401},
	}
	for _, err := range cases {
		if !hub.IsUnauthorized(err) {
			t.Errorf("IsUnauthorized(%T) = false, want true", err)
		}
	}
}

func TestStatusError_KeepsTheMessagesTheCLIPrintsToday(t *testing.T) {
	cases := map[string]error{
		"list-projects failed: 401 Unauthorized":     &hub.StatusError{Op: "list-projects", StatusCode: 401},
		"fetch-env-values failed: 403 Forbidden":     &hub.StatusError{Op: "fetch-env-values", StatusCode: 403},
		"token exchange failed: 418 I'm a teapot":    &hub.StatusError{Op: "token exchange", StatusCode: 418},
		"ai-bundle: unauthorized — run 'keyto auth'": &hub.StatusError{Op: "ai-bundle download", StatusCode: 401, Message: "ai-bundle: unauthorized — run 'keyto auth'"},
	}
	for want, err := range cases {
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	}
}
