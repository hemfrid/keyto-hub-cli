# Local-dev Compose — CLI Slice Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `keyto env sync` (and `keyto dev`) to the Go CLI so developers can write a fully-populated, gitignored `.env` for local docker-compose dev by reading the committed env-inventory + fetching UAT secrets from the Hub.

**Architecture:** A new `internal/envsync` package is wired into `cmd/keyto/main.go` via `case "env"` in `dispatch()`, following the identical DI pattern as `internal/start`. The package reads `.keyto/project.json` and `.keyto/env-inventory.json` from the working directory, partitions keys by `localSource` hint, builds container-side URLs locally, batches `uat`-hinted keys to a new `hub.Client.FetchEnvValues` POST, and writes a managed `.env` at 0600. No YAML parser dependency is introduced — profiles are inferred from the `container`-service names present in the inventory (the YAML-dep fallback path described by the spec), matching the spec's allowed behaviour when `project.yaml` is absent or unparseable.

**Tech Stack:** Go 1.25, `net/http/httptest` for hub-client tests, `t.TempDir()` + flat-function injection for envsync tests.

**Spec:** /Users/danielhirvonen/github/keyto/keyto-hub-nextjs-template/docs/superpowers/specs/2026-06-11-local-dev-docker-compose-design.md

**Target repo:** /Users/danielhirvonen/github/keyto/keyto-hub-cli

**Scope note:** slice 2 of 3; depends on inventory schema (real, template slice) + values-endpoint contract (spec §2.5/§3.4); the Hub endpoint is a separate slice.

---

## File Structure

| File | Responsibility | Action |
|---|---|---|
| `internal/hub/client.go` | Add `FetchEnvValues` method + private request/response types | Modify |
| `internal/hub/client_test.go` | Tests for `FetchEnvValues` (httptest.Server) | Modify |
| `internal/envsync/envsync.go` | `Run(ctx, args, Deps)` — flag parsing, algorithm, `.env` writer | Create |
| `internal/envsync/envsync_test.go` | All envsync tests (table-driven, fake deps, golden `.env`) | Create |
| `cmd/keyto/main.go` | `case "env"` + `case "dev"` in dispatch; `runEnvSync`; `runDev`; updated `printUsage` | Modify |
| `cmd/keyto/main_test.go` | Dispatch routing tests for `env` and `dev` | Modify |

---

## Chunk 1: `hub.Client.FetchEnvValues`

Add the new Hub client method in isolation, fully tested with `httptest.Server`, before writing any consumer code.

### Task 1: `FetchEnvValues` — Hub client method

**Files:**
- Modify: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/hub/client.go`
- Modify: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/hub/client_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/hub/client_test.go`:

```go
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
		keys, _ := keysRaw.([]interface{})
		if len(keys) != 2 {
			t.Errorf("request body keys len = %d, want 2", len(keys))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
```

- [ ] **Step 2: Run tests to confirm FAIL**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./internal/hub/... -run TestFetchEnvValues -v
```
Expected output: `FAIL` — `c.FetchEnvValues undefined (type *hub.Client has no field or method FetchEnvValues)`.

- [ ] **Step 3: Implement `FetchEnvValues` in `client.go`**

Append to `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/hub/client.go` (before the final `}`; add after `ListProjects`):

```go
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
//   POST /api/cli/projects/{org}/{repo}/env/{env}/values
//   Request:  { "keys": ["KEY_A", ...] }
//   Response: { "env": "uat", "values": { "KEY_A": "..." }, "missing": ["KEY_B"] }
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
```

- [ ] **Step 4: Run tests to confirm PASS**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./internal/hub/... -v
```
Expected: all `TestFetchEnvValues_*` and pre-existing tests PASS.

- [ ] **Step 5: Verify the full suite still passes**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./...
```
Expected: `ok` for every package, no failures.

- [ ] **Step 6: Commit**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
git add internal/hub/client.go internal/hub/client_test.go
git commit -m "feat(hub): add FetchEnvValues — POST env values endpoint client"
```

---

## Chunk 2: `internal/envsync` package — core algorithm

This chunk builds the heart of the feature: inventory parsing, partition logic, container-URL building, profile inference, and `.env` rendering. All logic is tested against a fake `HubFetcher` function injection — no real HTTP.

### Task 2: Inventory types + parsing

**Files:**
- Create: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/envsync/envsync.go`
- Create: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/envsync/envsync_test.go`

- [ ] **Step 1: Write the failing tests for inventory parsing and partition**

Create `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/envsync/envsync_test.go`:

```go
package envsync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// ---- helpers ----

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
}

func makeProjectDir(t *testing.T, m *project.Marker) string {
	t.Helper()
	dir := t.TempDir()
	if err := project.Write(dir, m); err != nil {
		t.Fatalf("project.Write: %v", err)
	}
	return dir
}

func makeCreds() *config.Creds {
	return &config.Creds{
		Credential: "tok_test",
		HubURL:     "https://hub.example.com",
		UserEmail:  "alice@keytogroup.com",
		UserName:   "Alice",
	}
}

func noopFetch(_ context.Context, _, _, _ string, _ []string) (map[string]string, []string, error) {
	return map[string]string{}, []string{}, nil
}

func staticFetch(values map[string]string, missing []string) envsync.HubFetcher {
	return func(_ context.Context, _, _, _ string, _ []string) (map[string]string, []string, error) {
		return values, missing, nil
	}
}

func errorFetch(err error) envsync.HubFetcher {
	return func(_ context.Context, _, _, _ string, _ []string) (map[string]string, []string, error) {
		return nil, nil, err
	}
}

// inventoryFor is a convenience builder for a minimal .keyto/env-inventory.json.
type inventoryKey struct {
	Key         string   `json:"key"`
	LocalSource string   `json:"localSource"`
	Service     string   `json:"service,omitempty"`
	Usages      []string `json:"usages"`
}

type inventory struct {
	SchemaVersion int            `json:"schemaVersion"`
	Keys          []inventoryKey `json:"keys"`
}

// baseDeps builds a Deps with the given dir pre-wired and all optional fields
// defaulted to safe no-ops / in-memory buffers.
func baseDeps(t *testing.T, dir string) envsync.Deps {
	t.Helper()
	return envsync.Deps{
		Creds:    makeCreds(),
		Cwd:      dir,
		Fetch:    noopFetch,
		Out:      &bytes.Buffer{},
	}
}

// ---- T1: missing project marker ----

func TestRun_NoProjectMarker_Error(t *testing.T) {
	dir := t.TempDir() // no .keyto/project.json
	d := baseDeps(t, dir)
	err := envsync.Run(context.Background(), []string{}, d)
	if err == nil {
		t.Fatal("expected error for missing project marker")
	}
	if !strings.Contains(err.Error(), "keyto start") {
		t.Errorf("error should mention 'keyto start', got: %v", err)
	}
}

// ---- T2: missing inventory ----

func TestRun_NoInventory_Error(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	// No .keyto/env-inventory.json
	d := baseDeps(t, dir)
	err := envsync.Run(context.Background(), []string{}, d)
	if err == nil {
		t.Fatal("expected error for missing inventory")
	}
	if !strings.Contains(err.Error(), "scan:env") {
		t.Errorf("error should mention 'scan:env', got: %v", err)
	}
}

// ---- T3: nil creds ----

func TestRun_NilCreds_Error(t *testing.T) {
	dir := t.TempDir()
	d := baseDeps(t, dir)
	d.Creds = nil
	err := envsync.Run(context.Background(), []string{}, d)
	if err == nil {
		t.Fatal("expected error for nil creds")
	}
	if !strings.Contains(err.Error(), "keyto auth") {
		t.Errorf("error should mention 'keyto auth', got: %v", err)
	}
}

// ---- T4: prod refused without --allow-prod ----

func TestRun_ProdWithoutFlag_Error(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys:          []inventoryKey{{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}}},
	})
	d := baseDeps(t, dir)
	err := envsync.Run(context.Background(), []string{"--env", "prod"}, d)
	if err == nil {
		t.Fatal("expected error for prod without --allow-prod")
	}
	if !strings.Contains(err.Error(), "--allow-prod") {
		t.Errorf("error should mention --allow-prod, got: %v", err)
	}
}

// ---- T5: golden .env across all three hint types ----

func TestRun_GoldenEnvFile_AllHintTypes(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)

	inv := inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "DATABASE_URL", LocalSource: "container", Service: "postgres", Usages: []string{"lib/db.ts:16"}},
			{Key: "REDIS_URL", LocalSource: "container", Service: "redis", Usages: []string{"lib/cache.ts:3"}},
			{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{"lib/email.ts:4"}},
			{Key: "DEV_USER_EMAIL", LocalSource: "placeholder", Usages: []string{"lib/auth.ts:37"}},
		},
	}
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inv)

	outPath := filepath.Join(dir, ".env")
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(map[string]string{"SENDGRID_API_KEY": "sg_real_value"}, []string{})

	err := envsync.Run(context.Background(), []string{"--out", outPath}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	content := string(data)

	// File must have the managed header.
	if !strings.Contains(content, "keyto env sync") {
		t.Error(".env missing managed-header banner")
	}

	// Postgres shared credentials and URL.
	if !strings.Contains(content, "POSTGRES_USER=") {
		t.Error(".env missing POSTGRES_USER")
	}
	if !strings.Contains(content, "POSTGRES_PASSWORD=") {
		t.Error(".env missing POSTGRES_PASSWORD")
	}
	if !strings.Contains(content, "POSTGRES_DB=acme_web") {
		t.Errorf(".env missing POSTGRES_DB=acme_web; content:\n%s", content)
	}
	if !strings.Contains(content, "DATABASE_URL=postgres://") {
		t.Error(".env missing DATABASE_URL postgres URL")
	}
	if !strings.Contains(content, "@localhost:5432/") {
		t.Error("DATABASE_URL should point at localhost:5432")
	}

	// Redis URL.
	if !strings.Contains(content, "REDIS_URL=redis://localhost:6379") {
		t.Errorf(".env missing REDIS_URL=redis://localhost:6379; content:\n%s", content)
	}

	// UAT key with real value.
	if !strings.Contains(content, "SENDGRID_API_KEY=sg_real_value") {
		t.Errorf(".env missing SENDGRID_API_KEY=sg_real_value; content:\n%s", content)
	}

	// Placeholder key is a commented stub.
	if !strings.Contains(content, "# DEV_USER_EMAIL=") {
		t.Errorf(".env missing commented DEV_USER_EMAIL placeholder; content:\n%s", content)
	}

	// COMPOSE_PROFILES contains postgres and redis (inferred from container services).
	if !strings.Contains(content, "COMPOSE_PROFILES=") {
		t.Error(".env missing COMPOSE_PROFILES")
	}
	if !strings.Contains(content, "postgres") || !strings.Contains(content, "redis") {
		t.Errorf("COMPOSE_PROFILES should include postgres and redis; content:\n%s", content)
	}

	// Verify permissions.
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf(".env permissions = %04o, want 0600", perm)
	}
}

// ---- T6: missing UAT key → MISSING comment, run continues ----

func TestRun_MissingUATKey_WritesComment(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}},
		},
	})

	outPath := filepath.Join(dir, ".env")
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(map[string]string{}, []string{"SENDGRID_API_KEY"}) // Hub says missing

	err := envsync.Run(context.Background(), []string{"--out", outPath}, d)
	if err != nil {
		t.Fatalf("missing key must be non-fatal, got error: %v", err)
	}

	data, _ := os.ReadFile(outPath)
	if !strings.Contains(string(data), "# MISSING: SENDGRID_API_KEY") {
		t.Errorf(".env should contain MISSING comment; got:\n%s", data)
	}
}

// ---- T7: --print writes to Out, not to file ----

func TestRun_PrintFlag_WritesToOutNotFile(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "REDIS_URL", LocalSource: "container", Service: "redis", Usages: []string{}},
		},
	})

	var out bytes.Buffer
	d := baseDeps(t, dir)
	d.Out = &out

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// .env file must NOT have been created.
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Error("--print must not write a file; .env was created")
	}

	// Output must go to Out.
	if !strings.Contains(out.String(), "REDIS_URL=redis://localhost:6379") {
		t.Errorf("--print output missing REDIS_URL; got:\n%s", out.String())
	}
}

// ---- T8: container-URL building — postgres ----

func TestContainerURLs_Postgres(t *testing.T) {
	m := &project.Marker{Name: "my-project", Org: "org", Repo: "my-project", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "DATABASE_URL", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "PGHOST", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "PGPORT", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "PGUSER", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "PGPASSWORD", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "PGDATABASE", LocalSource: "container", Service: "postgres", Usages: []string{}},
		},
	})

	var out bytes.Buffer
	d := baseDeps(t, dir)
	d.Out = &out

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content := out.String()

	// PGDATABASE should be the normalized project name.
	if !strings.Contains(content, "PGDATABASE=my_project") {
		t.Errorf("PGDATABASE should be my_project; got:\n%s", content)
	}
	if !strings.Contains(content, "PGHOST=localhost") {
		t.Errorf("PGHOST should be localhost; got:\n%s", content)
	}
	if !strings.Contains(content, "PGPORT=5432") {
		t.Errorf("PGPORT should be 5432; got:\n%s", content)
	}
}

// ---- T9: container-URL building — mysql ----

func TestContainerURLs_MySQL(t *testing.T) {
	m := &project.Marker{Name: "shop", Org: "org", Repo: "shop", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "MYSQL_URL", LocalSource: "container", Service: "mysql", Usages: []string{}},
		},
	})

	var out bytes.Buffer
	d := baseDeps(t, dir)
	d.Out = &out

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content := out.String()

	if !strings.Contains(content, "MYSQL_URL=mysql://") {
		t.Errorf("missing MYSQL_URL; got:\n%s", content)
	}
	if !strings.Contains(content, "@localhost:3306/") {
		t.Errorf("MYSQL_URL should point at localhost:3306; got:\n%s", content)
	}
	if !strings.Contains(content, "MYSQL_DATABASE=shop") {
		t.Errorf("MYSQL_DATABASE should be shop; got:\n%s", content)
	}
}

// ---- T10: Fetch is NOT called when no uat keys ----

func TestRun_NoUATKeys_FetchNotCalled(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "REDIS_URL", LocalSource: "container", Service: "redis", Usages: []string{}},
		},
	})

	fetchCalled := false
	d := baseDeps(t, dir)
	d.Fetch = func(_ context.Context, _, _, _ string, _ []string) (map[string]string, []string, error) {
		fetchCalled = true
		return map[string]string{}, []string{}, nil
	}

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchCalled {
		t.Error("Fetch must not be called when there are no uat-hinted keys")
	}
}

// ---- T11: Fetch error propagates ----

func TestRun_FetchError_Propagates(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}},
		},
	})

	fetchErr := fmt.Errorf("network failure")
	d := baseDeps(t, dir)
	d.Fetch = errorFetch(fetchErr)

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err == nil {
		t.Fatal("expected error from Fetch, got nil")
	}
}

// ---- T12: profile derivation — services inferred from container keys ----

func TestProfileDerivation_InferredFromContainerServices(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "DATABASE_URL", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "REDIS_URL", LocalSource: "container", Service: "redis", Usages: []string{}},
		},
	})

	var out bytes.Buffer
	d := baseDeps(t, dir)
	d.Out = &out

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must contain COMPOSE_PROFILES with exactly postgres,redis (sorted).
	// The app/migrate profiles must NOT be present in COMPOSE_PROFILES.
	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "COMPOSE_PROFILES=") {
			val := strings.TrimPrefix(line, "COMPOSE_PROFILES=")
			parts := strings.Split(val, ",")
			profileSet := map[string]bool{}
			for _, p := range parts {
				profileSet[strings.TrimSpace(p)] = true
			}
			if !profileSet["postgres"] || !profileSet["redis"] {
				t.Errorf("COMPOSE_PROFILES missing expected profiles; got %q", val)
			}
			if profileSet["app"] || profileSet["migrate"] {
				t.Errorf("COMPOSE_PROFILES must not contain app/migrate; got %q", val)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("COMPOSE_PROFILES line not found; output:\n%s", out.String())
	}
}

// ---- T13: --allow-prod enables prod ----

func TestRun_ProdWithAllowProd_Succeeds(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}},
		},
	})

	var fetchedEnv string
	d := baseDeps(t, dir)
	d.Fetch = func(_ context.Context, _, _, env string, _ []string) (map[string]string, []string, error) {
		fetchedEnv = env
		return map[string]string{"SENDGRID_API_KEY": "prod_value"}, []string{}, nil
	}

	err := envsync.Run(context.Background(), []string{"--env", "prod", "--allow-prod", "--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error with --allow-prod: %v", err)
	}
	if fetchedEnv != "prod" {
		t.Errorf("Fetch was called with env=%q, want prod", fetchedEnv)
	}
}

// ---- T14: Fetch receives only uat-hinted keys (least-privilege) ----

func TestRun_FetchReceivesOnlyUATKeys(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "DATABASE_URL", LocalSource: "container", Service: "postgres", Usages: []string{}},
			{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}},
			{Key: "DEV_USER_EMAIL", LocalSource: "placeholder", Usages: []string{}},
		},
	})

	var fetchedKeys []string
	d := baseDeps(t, dir)
	d.Fetch = func(_ context.Context, _, _, _ string, keys []string) (map[string]string, []string, error) {
		fetchedKeys = keys
		return map[string]string{"SENDGRID_API_KEY": "val"}, []string{}, nil
	}

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fetchedKeys) != 1 || fetchedKeys[0] != "SENDGRID_API_KEY" {
		t.Errorf("Fetch received keys %v, want [SENDGRID_API_KEY]", fetchedKeys)
	}
}
```

- [ ] **Step 2: Run tests to confirm FAIL**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./internal/envsync/... 2>&1
```
Expected: `cannot find package "github.com/hemfrid/keyto-hub-cli/internal/envsync"` — package not yet created.

- [ ] **Step 3: Create `envsync.go` with all the implementation**

Create `/Users/danielhirvonen/github/keyto/keyto-hub-cli/internal/envsync/envsync.go`:

```go
// Package envsync implements the `keyto env sync` command.
//
// It reads the project's committed env-inventory (.keyto/env-inventory.json),
// partitions keys by localSource hint, builds container-side URLs locally,
// fetches UAT-hinted values from the Hub in one batched call, and writes a
// managed .env file (0600) for local docker-compose dev.
package envsync

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// HubFetcher is the function type for fetching UAT/prod values from the Hub.
// It matches hub.Client.FetchEnvValues so the real client can be passed directly.
type HubFetcher func(
	ctx context.Context,
	org, repo, env string,
	keys []string,
) (values map[string]string, missing []string, err error)

// Deps holds all injectable dependencies for Run.
type Deps struct {
	// Creds holds the authenticated user's credentials. nil means not authed.
	Creds *config.Creds

	// Cwd is the working directory containing .keyto/ (injected so tests don't depend on os.Getwd).
	Cwd string

	// Fetch calls the Hub values endpoint. Required when there are uat-hinted keys.
	Fetch HubFetcher

	// Out is the writer for --print output and status messages.
	Out io.Writer
}

// inventoryKey mirrors the JSON shape of a key entry in .keyto/env-inventory.json.
type inventoryKey struct {
	Key         string   `json:"key"`
	LocalSource string   `json:"localSource"` // "container" | "uat" | "placeholder"
	Service     string   `json:"service,omitempty"`
	Usages      []string `json:"usages"`
}

// envInventory mirrors the root of .keyto/env-inventory.json.
type envInventory struct {
	SchemaVersion int            `json:"schemaVersion"`
	Keys          []inventoryKey `json:"keys"`
}

// postgresParams holds the shared Postgres credentials written into .env.
type postgresParams struct {
	User     string
	Password string
	DB       string
}

// mysqlParams holds the shared MySQL credentials written into .env.
type mysqlParams struct {
	User         string
	Password     string
	RootPassword string
	Database     string
}

// normalizeProjectName converts a project name to a database-safe identifier
// (lower-case, hyphens → underscores, non-alnum-underscore stripped).
func normalizeProjectName(name string) string {
	re := regexp.MustCompile(`[^a-z0-9_]`)
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "-", "_")
	s = re.ReplaceAllString(s, "")
	if s == "" {
		s = "app"
	}
	return s
}

// defaultPostgresParams returns deterministic defaults for the Postgres shared credentials.
func defaultPostgresParams(projectName string) postgresParams {
	db := normalizeProjectName(projectName)
	return postgresParams{
		User:     "postgres",
		Password: "postgres",
		DB:       db,
	}
}

// defaultMySQLParams returns deterministic defaults for the MySQL shared credentials.
func defaultMySQLParams(projectName string) mysqlParams {
	db := normalizeProjectName(projectName)
	return mysqlParams{
		User:         "app",
		Password:     "mysql",
		RootPassword: "root",
		Database:     db,
	}
}

// buildContainerValue resolves the local value for a container-hinted key.
// pg/mysql shared params are expected to already be computed by the caller.
func buildContainerValue(key, service string, pg postgresParams, my mysqlParams) string {
	switch service {
	case "postgres":
		switch key {
		case "DATABASE_URL":
			return fmt.Sprintf("postgres://%s:%s@localhost:5432/%s", pg.User, pg.Password, pg.DB)
		case "PGHOST":
			return "localhost"
		case "PGPORT":
			return "5432"
		case "PGUSER":
			return pg.User
		case "PGPASSWORD":
			return pg.Password
		case "PGDATABASE":
			return pg.DB
		default:
			// Unknown postgres key: return a placeholder URL
			return fmt.Sprintf("postgres://%s:%s@localhost:5432/%s", pg.User, pg.Password, pg.DB)
		}
	case "redis":
		switch key {
		case "REDIS_URL":
			return "redis://localhost:6379"
		case "REDIS_HOST":
			return "localhost"
		case "REDIS_PORT":
			return "6379"
		case "REDIS_PASSWORD":
			return ""
		default:
			return "redis://localhost:6379"
		}
	case "mysql":
		switch key {
		case "MYSQL_URL":
			return fmt.Sprintf("mysql://%s:%s@localhost:3306/%s", my.User, my.Password, my.Database)
		case "MYSQL_HOST":
			return "localhost"
		case "MYSQL_USER":
			return my.User
		case "MYSQL_PASSWORD":
			return my.Password
		case "MYSQL_DATABASE":
			return my.Database
		default:
			return fmt.Sprintf("mysql://%s:%s@localhost:3306/%s", my.User, my.Password, my.Database)
		}
	default:
		return ""
	}
}

// inferProfiles returns the set of backing-store compose profile names inferred
// from the container-hinted services present in the inventory.
// The app/migrate profiles are never included — they are opted in at invocation.
func inferProfiles(keys []inventoryKey) []string {
	seen := map[string]bool{}
	for _, k := range keys {
		if k.LocalSource == "container" && k.Service != "" {
			seen[k.Service] = true
		}
	}
	profiles := make([]string, 0, len(seen))
	for p := range seen {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	return profiles
}

// hasService reports whether the inventory contains at least one container key
// for the named service (used to decide which shared-param blocks to write).
func hasService(keys []inventoryKey, service string) bool {
	for _, k := range keys {
		if k.LocalSource == "container" && k.Service == service {
			return true
		}
	}
	return false
}

const managedHeader = `# ============================================================
# Managed by keyto env sync — DO NOT EDIT MANUALLY.
# Re-run: keyto env sync
# Personal overrides: .env.local (higher precedence, never clobbered).
# ============================================================
`

// renderEnv builds the full .env content from the resolved values.
//
// Layout:
//  1. Managed header
//  2. COMPOSE_PROFILES
//  3. Shared postgres params (if postgres in profiles)
//  4. Shared mysql params (if mysql in profiles)
//  5. Resolved container keys
//  6. Resolved uat keys (with MISSING comments for unresolved ones)
//  7. Placeholder key stubs
func renderEnv(
	inv envInventory,
	projectName string,
	uatValues map[string]string,
	uatMissing []string,
	targetEnv string,
) string {
	var sb strings.Builder

	sb.WriteString(managedHeader)
	sb.WriteString("\n")

	profiles := inferProfiles(inv.Keys)
	sb.WriteString("COMPOSE_PROFILES=")
	sb.WriteString(strings.Join(profiles, ","))
	sb.WriteString("\n\n")

	pg := defaultPostgresParams(projectName)
	my := defaultMySQLParams(projectName)

	// Shared postgres params block
	if hasService(inv.Keys, "postgres") {
		sb.WriteString("# Postgres shared credentials\n")
		sb.WriteString(fmt.Sprintf("POSTGRES_USER=%s\n", pg.User))
		sb.WriteString(fmt.Sprintf("POSTGRES_PASSWORD=%s\n", pg.Password))
		sb.WriteString(fmt.Sprintf("POSTGRES_DB=%s\n", pg.DB))
		sb.WriteString("\n")
	}

	// Shared MySQL params block
	if hasService(inv.Keys, "mysql") {
		sb.WriteString("# MySQL shared credentials\n")
		sb.WriteString(fmt.Sprintf("MYSQL_USER=%s\n", my.User))
		sb.WriteString(fmt.Sprintf("MYSQL_PASSWORD=%s\n", my.Password))
		sb.WriteString(fmt.Sprintf("MYSQL_ROOT_PASSWORD=%s\n", my.RootPassword))
		sb.WriteString(fmt.Sprintf("MYSQL_DATABASE=%s\n", my.Database))
		sb.WriteString("\n")
	}

	missingSet := map[string]bool{}
	for _, k := range uatMissing {
		missingSet[k] = true
	}

	// Container keys
	var containerKeys []inventoryKey
	for _, k := range inv.Keys {
		if k.LocalSource == "container" {
			containerKeys = append(containerKeys, k)
		}
	}
	if len(containerKeys) > 0 {
		sb.WriteString("# Container-backed services (local URLs)\n")
		for _, k := range containerKeys {
			val := buildContainerValue(k.Key, k.Service, pg, my)
			sb.WriteString(fmt.Sprintf("%s=%s\n", k.Key, val))
		}
		sb.WriteString("\n")
	}

	// UAT keys
	var uatKeys []inventoryKey
	for _, k := range inv.Keys {
		if k.LocalSource == "uat" {
			uatKeys = append(uatKeys, k)
		}
	}
	if len(uatKeys) > 0 {
		sb.WriteString(fmt.Sprintf("# Secrets from %s (fetched via Hub)\n", targetEnv))
		for _, k := range uatKeys {
			if missingSet[k.Key] {
				sb.WriteString(fmt.Sprintf("# MISSING: %s (not set in %s)\n", k.Key, targetEnv))
			} else if val, ok := uatValues[k.Key]; ok {
				sb.WriteString(fmt.Sprintf("%s=%s\n", k.Key, val))
			} else {
				// Key was not in missing and not in values — treat as missing.
				sb.WriteString(fmt.Sprintf("# MISSING: %s (not set in %s)\n", k.Key, targetEnv))
			}
		}
		sb.WriteString("\n")
	}

	// Placeholder keys
	var placeholderKeys []inventoryKey
	for _, k := range inv.Keys {
		if k.LocalSource == "placeholder" {
			placeholderKeys = append(placeholderKeys, k)
		}
	}
	if len(placeholderKeys) > 0 {
		sb.WriteString("# Local-only / platform credentials (never synced — set manually in .env.local)\n")
		for _, k := range placeholderKeys {
			sb.WriteString(fmt.Sprintf("# %s=\n", k.Key))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// writeEnvFile writes content to path with permissions 0600 + explicit Chmod.
func writeEnvFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// Run implements `keyto env sync [flags]`.
//
// Flags:
//
//	--env uat|prod   target environment (default: uat)
//	--out <path>     output path (default: <cwd>/.env)
//	--print          write to Deps.Out instead of a file
//	--allow-prod     required to use --env prod
func Run(ctx context.Context, args []string, d Deps) error {
	// Flag parsing
	fs := flag.NewFlagSet("env sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetEnv := fs.String("env", "uat", "target environment (uat|prod)")
	outPath := fs.String("out", "", "output path (default: <cwd>/.env)")
	printMode := fs.Bool("print", false, "write to stdout instead of a file")
	allowProd := fs.Bool("allow-prod", false, "required to use --env prod")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("env sync: parse flags: %w", err)
	}

	// Auth check first.
	if d.Creds == nil {
		return fmt.Errorf("not authenticated — run `keyto auth`")
	}

	// Prod gate.
	if *targetEnv == "prod" && !*allowProd {
		return fmt.Errorf("env sync: --env prod requires --allow-prod (production secret reveal; use UAT for local dev)")
	}

	// Read project marker.
	marker, err := project.Read(d.Cwd)
	if err != nil {
		return fmt.Errorf("env sync: read project marker: %w", err)
	}
	if marker == nil {
		return fmt.Errorf("env sync: no .keyto/project.json found — run `keyto start` first")
	}

	// Read inventory.
	invPath := filepath.Join(d.Cwd, ".keyto", "env-inventory.json")
	invData, err := os.ReadFile(invPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("env sync: no .keyto/env-inventory.json — run `npm run scan:env` first")
		}
		return fmt.Errorf("env sync: read inventory: %w", err)
	}
	var inv envInventory
	if err := json.Unmarshal(invData, &inv); err != nil {
		return fmt.Errorf("env sync: parse inventory: %w", err)
	}

	// Partition by localSource.
	var uatKeys []string
	for _, k := range inv.Keys {
		if k.LocalSource == "uat" {
			uatKeys = append(uatKeys, k.Key)
		}
	}

	// Batch Hub fetch for uat-hinted keys (skip if empty).
	var uatValues map[string]string
	var uatMissing []string
	if len(uatKeys) > 0 {
		uatValues, uatMissing, err = d.Fetch(ctx, marker.Org, marker.Repo, *targetEnv, uatKeys)
		if err != nil {
			return fmt.Errorf("env sync: fetch values from Hub: %w", err)
		}
	} else {
		uatValues = map[string]string{}
		uatMissing = []string{}
	}

	// Render the .env content.
	content := renderEnv(inv, marker.Name, uatValues, uatMissing, *targetEnv)

	// Output.
	if *printMode {
		_, err := fmt.Fprint(d.Out, content)
		return err
	}

	dest := *outPath
	if dest == "" {
		dest = filepath.Join(d.Cwd, ".env")
	}
	if err := writeEnvFile(dest, content); err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "wrote %s\n", dest)
	return nil
}
```

- [ ] **Step 4: Run tests to confirm PASS**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./internal/envsync/... -v
```
Expected: all 14 tests PASS. If any fail, fix the implementation (not the tests) — the tests encode the spec's requirements.

- [ ] **Step 5: Verify no regressions**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./...
```
Expected: `ok` for all packages.

- [ ] **Step 6: Commit**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
git add internal/envsync/envsync.go internal/envsync/envsync_test.go
git commit -m "feat(envsync): core algorithm — inventory parsing, container URLs, .env writer"
```

---

## Chunk 3: Wire `env` and `dev` into `cmd/keyto/main.go`

Connect the new package to the CLI dispatcher and add the `keyto dev` convenience wrapper.

### Task 3: Dispatch wiring + `runEnvSync` + `runDev`

**Files:**
- Modify: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main.go`
- Modify: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main_test.go`

- [ ] **Step 1: Write failing dispatch tests**

Before appending the new tests, update the import block at the top of
`/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main_test.go`
so it is the **single merged import block** for the whole file. The file
currently imports `"strings"`, `"testing"`, and `"time"`. The new tests
also need `"context"`. Replace the existing import block with:

```go
import (
	"context"
	"strings"
	"testing"
	"time"
)
```

Then append the following test functions to `main_test.go`:

```go
// TestDispatch_EnvSubcommand_Unknown ensures `keyto env unknown` returns an error.
func TestDispatch_EnvSubcommand_Unknown(t *testing.T) {
	// `keyto env` with a subcommand that is not "sync" should fail.
	// Note: `keyto env sync` requires real auth + cwd setup, so we test
	// the dispatch routing only at the unknown-subcommand level.
	err := dispatch([]string{"env", "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown env subcommand, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention 'unknown', got: %v", err)
	}
}

// TestDispatch_DevRoutesToRunDev ensures `keyto dev` routes to the runDev
// package variable (similar to the runUpdate pattern), without running Docker.
func TestDispatch_DevRoutesToRunDev(t *testing.T) {
	called := false
	origRunDev := runDev
	runDev = func(ctx context.Context, args []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runDev = origRunDev })

	if err := dispatch([]string{"dev"}); err != nil {
		t.Fatalf("dispatch(dev) returned error: %v", err)
	}
	if !called {
		t.Fatal("dispatch did not route 'dev' to runDev")
	}
}
```

- [ ] **Step 2: Run tests to confirm FAIL**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./cmd/keyto/... -run "TestDispatch_Env|TestDispatch_Dev" -v
```
Expected: FAIL — `dispatch([]string{"env", "unknown"})` returns `"unknown command: env"` (no env case yet); `runDev` undefined.

- [ ] **Step 3: Wire the dispatch and implement `runEnvSync` / `runDev` in `main.go`**

Add the import for `envsync` and `exec` (already imported), then apply the following changes to `/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main.go`:

**3a. Add `envsync` import** — add to the import block:
```go
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
```

**3b. Add `case "env":` and `case "dev":` to the `switch` in `dispatch()`** — insert before `default:`:
```go
	case "env":
		return runEnvDispatch(context.Background(), args[1:])
	case "dev":
		return runDev(context.Background(), args[1:])
```

**3c. Add `runDev` as a package variable** (mirrors the `runUpdate` pattern for testability) — add after `var runUpdate = func() error {`:
```go
// runDev is a package var so dispatch routing can be tested without performing
// a real env sync or docker compose up.
var runDev = func(ctx context.Context, args []string) error {
	return runDevImpl(ctx, args)
}
```

**3d. Add `runEnvDispatch`** — add after `runStart`:
```go
// runEnvDispatch routes `keyto env <subcommand>` to the appropriate handler.
// Currently only "sync" is supported.
func runEnvDispatch(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "sync" {
		return runEnvSync(ctx, args[1:])
	}
	return fmt.Errorf("unknown env subcommand: %s — try `keyto env sync`", args[0])
}
```

**3e. Add `runEnvSync`** — add after `runEnvDispatch`:
```go
// runEnvSync implements `keyto env sync [flags]`.
// It loads creds, builds the real Deps, and delegates to envsync.Run.
func runEnvSync(ctx context.Context, args []string) error {
	creds, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotAuthed) {
			creds = nil // envsync.Run returns the helpful "run keyto auth" error
		} else {
			return fmt.Errorf("env sync: load config: %w", err)
		}
	}

	if creds != nil && creds.Expired() {
		return fmt.Errorf("your sign-in has expired — run `keyto auth` to sign in again")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("env sync: get working directory: %w", err)
	}

	var fetcher envsync.HubFetcher
	if creds != nil {
		hubClient := &hub.Client{
			BaseURL:    creds.HubURL,
			Credential: creds.Credential,
		}
		fetcher = hubClient.FetchEnvValues
	}

	d := envsync.Deps{
		Creds:  creds,
		Cwd:    cwd,
		Fetch:  fetcher,
		Out:    os.Stderr,
	}

	return envsync.Run(ctx, args, d)
}
```

**3f. Add `runDevImpl`** — add after `runEnvSync`:
```go
// runDevImpl implements `keyto dev`: env sync then docker compose up.
// The sync runs on the host first (it needs ~/.keyto/credentials and browser
// auth, so it cannot run inside a container). The documented two-step
// baseline is: keyto env sync && docker compose up
func runDevImpl(ctx context.Context, args []string) error {
	fmt.Fprintln(os.Stderr, "keyto dev: syncing env…")
	if err := runEnvSync(ctx, []string{}); err != nil {
		return fmt.Errorf("keyto dev: env sync: %w", err)
	}

	fmt.Fprintln(os.Stderr, "keyto dev: starting docker compose…")
	composeArgs := append([]string{"compose", "up"}, args...)
	cmd := exec.CommandContext(ctx, "docker", composeArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keyto dev: docker compose up: %w", err)
	}
	return nil
}
```

**3g. Update `printUsage()`** — add the two new commands to the Commands block:
```go
	fmt.Println("  env sync    Sync UAT secrets into .env for local docker-compose dev")
	fmt.Println("              Flags: --env uat|prod  --out <file>  --print  --allow-prod")
	fmt.Println("  dev         env sync + docker compose up (requires docker)")
```

- [ ] **Step 4: Run tests to confirm dispatch tests PASS**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./cmd/keyto/... -v
```
Expected: all tests including `TestDispatch_EnvSubcommand_Unknown` and `TestDispatch_DevRoutesToRunDev` PASS.

- [ ] **Step 5: Build the binary to confirm compilation**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go build ./cmd/keyto/...
```
Expected: exits 0, binary `keyto` produced in repo root (or `./keyto`). No compile errors.

- [ ] **Step 6: Smoke-test help output**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
./keyto help 2>&1 | grep -E "env sync|dev"
```
Expected: lines mentioning `env sync` and `dev` appear in the help output.

- [ ] **Step 7: Verify the full suite**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./...
```
Expected: `ok` for all packages.

- [ ] **Step 8: Commit**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
git add cmd/keyto/main.go cmd/keyto/main_test.go
git commit -m "feat(cmd): wire keyto env sync and keyto dev into dispatch"
```

---

## Chunk 4: Integration smoke-test (optional, network-free)

A light integration test that exercises the full path through `runEnvSync` against a local `httptest.Server` — no real Hub required. This is the closest thing to an end-to-end test without a running cluster.

### Task 4: `runEnvSync` integration test via httptest

**Files:**
- Modify: `/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main_test.go`

- [ ] **Step 1: Write the integration test**

First, update the **single** import block at the top of
`/Users/danielhirvonen/github/keyto/keyto-hub-cli/cmd/keyto/main_test.go`
to the complete merged form required by this chunk. Go does NOT allow two
separate `import (...)` blocks in the same file — merge everything into
one. After Chunk 3, the import block already contains `"context"`,
`"strings"`, `"testing"`, and `"time"`. This chunk's test also needs
`"encoding/json"`, `"net/http"`, `"net/http/httptest"`, and
`"path/filepath"`. Replace the import block with:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)
```

Then append the following test function to `main_test.go`:

```go
// TestRunEnvSync_Integration exercises the full envsync.Run path end-to-end
// against a local httptest.Server that stands in for the Hub values endpoint.
// It uses Deps.Cwd injection (no os.Chdir — process-global mutation) and wires
// the fake Hub via a staticFetch-style HubFetcher backed by httptest.NewServer.
func TestRunEnvSync_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}

	// Stand up a fake Hub values endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"env":     "uat",
			"values":  map[string]string{"SENDGRID_API_KEY": "sg_integration_value"},
			"missing": []string{},
		})
	}))
	defer srv.Close()

	// Build a fully isolated project directory in a temp dir.
	projectDir := t.TempDir()

	// Write project marker.
	keytoDir := filepath.Join(projectDir, ".keyto")
	if err := os.MkdirAll(keytoDir, 0o755); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	marker := map[string]string{"name": "acme-web", "org": "hemfrid", "repo": "acme-web", "hub_url": srv.URL}
	markerData, _ := json.MarshalIndent(marker, "", "  ")
	if err := os.WriteFile(filepath.Join(keytoDir, "project.json"), markerData, 0o644); err != nil {
		t.Fatalf("write project.json: %v", err)
	}

	// Write inventory.
	inv := map[string]interface{}{
		"schemaVersion": 1,
		"keys": []map[string]interface{}{
			{"key": "DATABASE_URL", "localSource": "container", "service": "postgres", "usages": []string{}},
			{"key": "SENDGRID_API_KEY", "localSource": "uat", "usages": []string{}},
			{"key": "DEV_USER_EMAIL", "localSource": "placeholder", "usages": []string{}},
		},
	}
	invData, _ := json.MarshalIndent(inv, "", "  ")
	if err := os.WriteFile(filepath.Join(keytoDir, "env-inventory.json"), invData, 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	// Wire the fake Hub via a HubFetcher that delegates to the httptest server.
	// This exercises the same code path as the real hub.Client.FetchEnvValues
	// without touching any process-global state.
	fakeFetcher := func(ctx context.Context, org, repo, env string, keys []string) (map[string]string, []string, error) {
		hubClient := &hub.Client{BaseURL: srv.URL, Credential: "tok_integration"}
		return hubClient.FetchEnvValues(ctx, org, repo, env, keys)
	}

	outPath := filepath.Join(projectDir, ".env.test")
	creds := &config.Creds{
		Credential: "tok_integration",
		HubURL:     srv.URL,
		UserEmail:  "alice@keytogroup.com",
		UserName:   "Alice",
	}

	d := envsync.Deps{
		Creds: creds,
		Cwd:   projectDir, // inject project dir — no os.Chdir
		Fetch: fakeFetcher,
		Out:   &strings.Builder{},
	}

	if err := envsync.Run(context.Background(), []string{"--out", outPath}, d); err != nil {
		t.Fatalf("envsync.Run integration error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .env.test: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "SENDGRID_API_KEY=sg_integration_value") {
		t.Errorf("integration: .env missing SENDGRID_API_KEY; got:\n%s", content)
	}
	if !strings.Contains(content, "DATABASE_URL=postgres://") {
		t.Errorf("integration: .env missing DATABASE_URL; got:\n%s", content)
	}
	if !strings.Contains(content, "# DEV_USER_EMAIL=") {
		t.Errorf("integration: .env missing placeholder comment; got:\n%s", content)
	}
}
```

Note: `os` is not needed in this chunk's test (no `os.Chdir`); the
`os.MkdirAll` and `os.WriteFile` calls above use the `os` package — add
`"os"` to the import block if it is not already present. The final merged
import block for `main_test.go` after both Chunk 3 and Chunk 4 are applied
must be exactly:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run the integration test**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./cmd/keyto/... -v -run TestRunEnvSync_Integration
```
Expected: `--- PASS: TestRunEnvSync_Integration`.

- [ ] **Step 3: Verify full suite still green**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
go test ./...
```
Expected: `ok` for all packages.

- [ ] **Step 4: Commit**

```bash
cd /Users/danielhirvonen/github/keyto/keyto-hub-cli
git add cmd/keyto/main_test.go
git commit -m "test(cmd): integration test for runEnvSync via httptest.Server"
```

---

## Done criteria

All of the following must be true before the slice is considered complete:

- [ ] `go test ./...` passes with zero failures in the `keyto-hub-cli` repo.
- [ ] `go build ./cmd/keyto/...` exits 0 with no compiler errors or warnings.
- [ ] `./keyto help` output includes `env sync` and `dev` with flag descriptions.
- [ ] `./keyto env unknown` returns a non-zero exit and an error message containing "unknown".
- [ ] `./keyto env sync --help` (or `--print` with no project in cwd) returns a clear error about running `keyto start`.
- [ ] All 14 `internal/envsync` tests pass, covering: missing marker, missing inventory, nil creds, prod gating, golden `.env` output, missing-key comment, `--print`, container URL building (postgres + mysql), no-fetch when no UAT keys, fetch error propagation, profile derivation, `--allow-prod`, least-privilege fetch.
- [ ] All 5 new `internal/hub` tests for `FetchEnvValues` pass (success, empty-keys no-op, 401, 403, context cancellation).
- [ ] `.env` written by the command has mode `0600` (verified by `TestRun_GoldenEnvFile_AllHintTypes`).
- [ ] `COMPOSE_PROFILES` in the written `.env` contains only backing-store profiles (postgres/redis/mysql) — never `app` or `migrate`.
- [ ] The binding contract is honoured: `POST /api/cli/projects/{org}/{repo}/env/{env}/values` with `{"keys":[...]}` body; response `{"env","values","missing"}` decoded correctly.

### Notes for the implementer

**YAML-dep fallback (confirmed, no gopkg.in/yaml.v3 added):** `go.mod` has no YAML dependency (`golang.org/x/sys` and `golang.org/x/term` only). The plan uses the spec's allowed fallback: profiles are inferred from the `service` field of `container`-hinted inventory entries. This is cheaper than adding a dependency and is fully spec-compliant. If a future iteration wants to cross-check against `project.yaml` for stricter correctness, add `gopkg.in/yaml.v3` and a `scanProjectYAML` helper — but that adds no new functionality for local dev (the inventory already captures which services the app actually uses).

**`runDev` OS exec note:** `runDevImpl` shells out to `docker` on PATH. If `docker` is not installed, the error is surfaced clearly from `exec.CommandContext`. The function is a package variable (`runDev`) so dispatch tests can stub it without running Docker.

**Idempotency note:** because `renderEnv` always produces the full file from scratch, every `keyto env sync` invocation fully replaces `.env`. There is no append/merge. Personal overrides belong in `.env.local`, which docker-compose loads with higher precedence and which Next.js picks up at runtime.

**Non-fatal missing keys:** when the Hub returns a key in `missing`, the `renderEnv` function writes `# MISSING: KEY (not set in <env>)` — a commented line that keeps the file syntactically valid for `docker compose up` while alerting the developer. The overall run exits 0.
