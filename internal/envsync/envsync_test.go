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

// ---- T15: UAT secret values with special chars are quoted/escaped in .env ----

func TestRun_UATValueQuoting(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)

	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys: []inventoryKey{
			{Key: "WITH_SPACE", LocalSource: "uat", Usages: []string{}},
			{Key: "WITH_HASH", LocalSource: "uat", Usages: []string{}},
			{Key: "WITH_QUOTE", LocalSource: "uat", Usages: []string{}},
			{Key: "WITH_NEWLINE", LocalSource: "uat", Usages: []string{}},
			{Key: "PLAIN", LocalSource: "uat", Usages: []string{}},
		},
	})

	fetchValues := map[string]string{
		"WITH_SPACE":   "hello world",
		"WITH_HASH":    "a#b",
		"WITH_QUOTE":   `a"b`,
		"WITH_NEWLINE": "a\nb",
		"PLAIN":        "sg_abc123",
	}

	outPath := filepath.Join(dir, ".env")
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(fetchValues, []string{})

	err := envsync.Run(context.Background(), []string{"--out", outPath}, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	content := string(data)

	// Split into physical lines to check for stray injected lines.
	lines := strings.Split(content, "\n")

	// Helper: require a specific physical line is present.
	requireLine := func(want string) {
		t.Helper()
		for _, l := range lines {
			if l == want {
				return
			}
		}
		t.Errorf("expected line %q not found in .env:\n%s", want, content)
	}

	requireLine(`WITH_SPACE="hello world"`)
	requireLine(`WITH_HASH="a#b"`)
	requireLine(`WITH_QUOTE="a\"b"`)
	requireLine(`WITH_NEWLINE="a\nb"`) // literal \n, single physical line
	requireLine(`PLAIN=sg_abc123`)     // plain value stays unquoted

	// The newline value must NOT inject a spurious extra physical line.
	// Count the UAT key assignment lines (start with one of our key names) —
	// we expect exactly 5 (one per key, no stray injected lines from the newline value).
	uatKeyPrefixes := []string{"WITH_SPACE=", "WITH_HASH=", "WITH_QUOTE=", "WITH_NEWLINE=", "PLAIN="}
	keyLines := 0
	for _, l := range lines {
		for _, prefix := range uatKeyPrefixes {
			if strings.HasPrefix(l, prefix) {
				keyLines++
				break
			}
		}
	}
	if keyLines != 5 {
		t.Errorf("expected 5 UAT key lines (no stray injected lines), got %d; content:\n%s", keyLines, content)
	}
}
