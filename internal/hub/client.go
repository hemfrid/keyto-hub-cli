// Package hub provides a client for the Keyto Hub CLI API.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Project represents a Keyto project that the authenticated user is a member of.
type Project struct {
	Name string `json:"name"`
	Org  string `json:"org"`
	Repo string `json:"repo"`
	Role string `json:"role"`
}

// Client is a thin HTTP client for the Hub's CLI endpoints.
// Credential is optional; when set it is sent as a Bearer token on every request.
type Client struct {
	BaseURL    string
	Credential string
	HTTP       *http.Client
}

// TokenResponse is the JSON payload returned by POST /api/cli/token.
type TokenResponse struct {
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expires_at"`
	UserEmail  string    `json:"user_email"`
	UserName   string    `json:"user_name"`
}

// tokenRequest is the JSON body sent to the token endpoint.
type tokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

// httpClient returns c.HTTP if set, otherwise http.DefaultClient.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// ExchangeToken POSTs the authorization code and PKCE verifier to the Hub's
// token endpoint and returns the parsed credential on success.
// On a non-200 response it returns an error that includes the HTTP status but
// does NOT include the raw response body (to avoid leaking error details).
func (c *Client) ExchangeToken(ctx context.Context, code, codeVerifier, redirectURI string) (*TokenResponse, error) {
	payload := tokenRequest{
		Code:         code,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/cli/token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", resp.Status)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	return &tr, nil
}

// listProjectsResponse is the JSON body returned by GET /api/cli/projects.
type listProjectsResponse struct {
	Projects []Project `json:"projects"`
}

// ListProjects fetches the list of projects the authenticated user is a member of.
// It sends a Bearer token in the Authorization header and expects a 200 response
// containing {projects: [...]}. Non-200 responses return an error without leaking
// the response body.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/cli/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("build list-projects request: %w", err)
	}
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("list-projects request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list-projects failed: %s", resp.Status)
	}

	var lr listProjectsResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("decode list-projects response: %w", err)
	}

	return lr.Projects, nil
}

// fetchEnvValuesRequest is the JSON body sent to POST /api/cli/projects/{org}/{repo}/env/{env}/values.
type fetchEnvValuesRequest struct {
	Keys []string `json:"keys"`
}

// fetchEnvValuesResponse is the JSON body returned by the values endpoint.
type fetchEnvValuesResponse struct {
	Env     string            `json:"env"`
	Values  map[string]string `json:"values"`
	Missing []string          `json:"missing"`
}

// FetchEnvValues POSTs a set of key names to the Hub and returns the resolved
// values and any keys the Hub could not resolve.
//
// Binding contract (spec §2.5 / §3.4):
//
//	POST /api/cli/projects/{org}/{repo}/env/{env}/values
//	Request:  { "keys": ["KEY_A", ...] }
//	Response: { "env": "uat", "values": { "KEY_A": "..." }, "missing": ["KEY_B"] }
//
// An empty keys slice is a valid no-op — the server returns 200 with empty
// values and missing. Non-200 returns an error with the HTTP status but never
// the raw body (to avoid leaking error details).
func (c *Client) FetchEnvValues(
	ctx context.Context,
	org, repo, env string,
	keys []string,
) (values map[string]string, missing []string, err error) {
	payload := fetchEnvValuesRequest{Keys: keys}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch-env-values: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/cli/projects/%s/%s/env/%s/values", c.BaseURL, org, repo, env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("fetch-env-values: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch-env-values: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch-env-values failed: %s", resp.Status)
	}

	var fr fetchEnvValuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return nil, nil, fmt.Errorf("fetch-env-values: decode response: %w", err)
	}

	if fr.Values == nil {
		fr.Values = map[string]string{}
	}
	if fr.Missing == nil {
		fr.Missing = []string{}
	}
	return fr.Values, fr.Missing, nil
}
