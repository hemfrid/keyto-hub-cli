// Package hub provides a client for the Keyto Hub CLI API.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/termsafe"
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
		return nil, &StatusError{Op: "token exchange", StatusCode: resp.StatusCode}
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
		return nil, &StatusError{Op: "list-projects", StatusCode: resp.StatusCode}
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

	endpoint := c.BaseURL + envValuesPath(org, repo, env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
		return nil, nil, &StatusError{Op: "fetch-env-values", StatusCode: resp.StatusCode}
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

// setEnvValuesRequest / setEnvValuesResponse are the bodies for the env write
// endpoint. Binding contract (proposed, mirrors FetchEnvValues):
//
//	PUT /api/cli/projects/{org}/{repo}/env/{env}/values
//	Request:  { "values": { "KEY": "VALUE", ... } }
//	Response: { "env": "uat", "updated": ["KEY", ...] }
type setEnvValuesRequest struct {
	Values map[string]string `json:"values"`
}

type setEnvValuesResponse struct {
	Env     string   `json:"env"`
	Updated []string `json:"updated"`
}

// SetEnvValuesError is a refused env write. The Hub's error body for this
// endpoint is JSON: { error, code?, detail?, updated? }. It names keys, never
// values. Only those decoded fields are kept, never the raw body.
type SetEnvValuesError struct {
	StatusCode int
	Status     string   // e.g. "409 Conflict": the code and Go's own text for it, never the server's
	Code       string   // machine code, e.g. "secret_recently_deleted"; else the body's "error"
	Detail     string   // the Hub's explanation, one line
	Updated    []string // keys the Hub saved before it stopped
}

func (e *SetEnvValuesError) Error() string {
	msg := "set-env-values failed: " + e.Status
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if len(e.Updated) > 0 {
		msg += " [already saved: " + strings.Join(e.Updated, ", ") + "]"
	}
	return msg
}

const (
	setEnvErrBodyLimit   = 64 << 10 // bytes of an error body read
	setEnvErrCodeLimit   = 64
	setEnvErrDetailLimit = 500
	setEnvErrKeysLimit   = 20
	setEnvMinValueLen    = 4 // shorter values are NOT filtered: too common in prose to match on
)

var envKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,62}$`)

// oneLine makes Hub-supplied text safe to print (termsafe.Sanitize: no escape
// sequences, no newlines, no bidi overrides), collapses runs of spaces, and
// cuts it at max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(termsafe.Sanitize(s, false)), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "..."
	}
	return s
}

// newSetEnvValuesError builds the error for a non-2xx answer. A body that is
// not the Hub's JSON shape leaves only the status. A text field that contains
// one of the submitted values is dropped: values are never shown.
func newSetEnvValuesError(resp *http.Response, values map[string]string) *SetEnvValuesError {
	// The reason phrase in resp.Status is the server's (or a proxy's) own text,
	// so the status line is rebuilt from the code alone.
	e := &SetEnvValuesError{
		StatusCode: resp.StatusCode,
		Status:     termsafe.StatusLine(resp.StatusCode),
	}
	var body struct {
		Error   string   `json:"error"`
		Code    string   `json:"code"`
		Detail  string   `json:"detail"`
		Updated []string `json:"updated"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, setEnvErrBodyLimit))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return e
	}
	// A field is checked twice: as received (so a multi-line value echoed
	// verbatim is caught), and as it would print (so a value split by a
	// character the sanitizer removes, such as a zero-width space, is caught).
	// Values shorter than setEnvMinValueLen are not filtered: they match
	// ordinary words too often, and would hide most details.
	printed := func(s string) string { return oneLine(s, len(s)) }
	hasValue := func(s string) bool {
		p := printed(s)
		for _, v := range values {
			if len(v) < setEnvMinValueLen {
				continue
			}
			if strings.Contains(s, v) {
				return true
			}
			if pv := printed(v); len(pv) >= setEnvMinValueLen && strings.Contains(p, pv) {
				return true
			}
		}
		return false
	}
	// A saved key is listed only if it is not itself a submitted value (a
	// value can be key-shaped). Exact match, whatever its length.
	isValue := func(k string) bool {
		for _, v := range values {
			if k == v {
				return true
			}
		}
		return false
	}
	code := body.Code
	if code == "" {
		code = body.Error
	}
	if !hasValue(code) {
		e.Code = oneLine(code, setEnvErrCodeLimit)
	}
	if !hasValue(body.Detail) {
		e.Detail = oneLine(body.Detail, setEnvErrDetailLimit)
	}
	for _, k := range body.Updated {
		if len(e.Updated) < setEnvErrKeysLimit && envKeyRe.MatchString(k) && !isValue(k) {
			e.Updated = append(e.Updated, k)
		}
	}
	return e
}

// SetEnvValues upserts env vars in the given environment via the Hub and returns
// the keys the Hub reports as updated. Non-2xx returns a *SetEnvValuesError with
// the HTTP status and the Hub's code, detail and already-saved keys, but never
// the raw body. Values are never logged.
func (c *Client) SetEnvValues(
	ctx context.Context,
	org, repo, env string,
	values map[string]string,
) (updated []string, err error) {
	body, err := json.Marshal(setEnvValuesRequest{Values: values})
	if err != nil {
		return nil, fmt.Errorf("set-env-values: marshal request: %w", err)
	}

	endpoint := c.BaseURL + envValuesPath(org, repo, env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("set-env-values: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("set-env-values: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newSetEnvValuesError(resp, values)
	}

	var sr setEnvValuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("set-env-values: decode response: %w", err)
	}
	if sr.Updated == nil {
		sr.Updated = []string{}
	}
	return sr.Updated, nil
}

// PostDiagnostics uploads a doctor report to the Hub's diagnostics ingest so it
// surfaces in /admin/diagnostics.
//
// Contract:
//
//	POST /api/cli/diagnostics
//	Authorization: Bearer <credential>
//	Request: { schema_version:1, cli_version, os, os_version?, arch?,
//	           checks:[{name,status,fix_type,fix,detail}] }
//	Response: 200 {ok:true} | 401 | 422 | 429
//
// payload is marshalled as-is (the doctor report plus its schema_version
// envelope). A non-2xx response returns an error that includes the HTTP status
// but never the raw body (to avoid leaking error details). Callers treat the
// upload as best-effort — see the doctor command's non-fatal handling.
func (c *Client) PostDiagnostics(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("diagnostics: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/cli/diagnostics", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("diagnostics: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("diagnostics: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Op: "diagnostics post", StatusCode: resp.StatusCode}
	}
	return nil
}

// ── AI bundle (keyto ai init/update/status) ─────────────────────

type AIBundleManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type AIBundleManifest struct {
	Tag              string                 `json:"tag"`
	SourceSHA        string                 `json:"source_sha"`
	ManifestCommit   string                 `json:"manifest_commit"`
	FrameworkVersion string                 `json:"framework_version"`
	Files            []AIBundleManifestFile `json:"files"`
}

type AIBundleMeta struct {
	Tag         string           `json:"tag"`
	PublishedAt string           `json:"publishedAt"`
	SourceRepo  string           `json:"sourceRepo"`
	Manifest    AIBundleManifest `json:"manifest"`
}

// AIBundleMeta fetches the latest general-bundle release metadata (tag +
// file manifest) from the Hub relay.
func (c *Client) AIBundleMeta(ctx context.Context) (*AIBundleMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/ai-bundle/meta", nil)
	if err != nil {
		return nil, fmt.Errorf("build ai-bundle meta request: %w", err)
	}
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai-bundle meta request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &StatusError{Op: "ai-bundle meta", StatusCode: resp.StatusCode, Message: "ai-bundle meta: unauthorized — run 'keyto auth'"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "ai-bundle meta", StatusCode: resp.StatusCode}
	}
	var m AIBundleMeta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode ai-bundle meta: %w", err)
	}
	return &m, nil
}

// AIBundleTarball downloads the bundle tarball for a specific release tag.
func (c *Client) AIBundleTarball(ctx context.Context, tag string) ([]byte, error) {
	u := c.BaseURL + "/api/ai-bundle?tag=" + url.QueryEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build ai-bundle request: %w", err)
	}
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai-bundle request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &StatusError{Op: "ai-bundle download", StatusCode: resp.StatusCode, Message: "ai-bundle: unauthorized — run 'keyto auth'"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "ai-bundle download", StatusCode: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

// envValuesPath is the env-values route for a project. org, repo and env are
// each escaped as one path segment, so a slash, "?" or "#" inside one of them
// cannot change the route.
func envValuesPath(org, repo, env string) string {
	return "/api/cli/projects/" + url.PathEscape(org) + "/" + url.PathEscape(repo) + "/env/" + url.PathEscape(env) + "/values"
}
