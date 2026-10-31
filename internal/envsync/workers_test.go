package envsync_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// workersEnvironments is a .environments fixture in the shape the Hub's
// renderEnvironments writes and the Workers template ships.
const workersEnvironments = `# .environments: the project's secrets inventory. Keys and lanes only, never values.

schemaVersion: 1
detectedAt: 2026-10-28T00:00:00Z
detectedFrom:
  - template-baseline
stack: workers
secrets:
  - key: OPENAI_API_KEY
    category: operator-required
  - key: PUBLIC_FLAG
    category: build-time
  - key: R2_ACCESS_KEY_ID
    category: platform-auto
    source: kv-tf:acme-workers-platform
  - key: IGNORED_THING
    category: skip
  - key: KEYTO_AUTH_SESSION_KEY
    category: operator-required
`

// makeWorkersDir builds a checked-out Workers repo: the keyto marker, a
// wrangler.json and the given .environments ("" = none).
func makeWorkersDir(t *testing.T, environments string) string {
	t.Helper()
	dir := makeProjectDir(t, &project.Marker{Name: "acme-workers", Org: "hemfrid", Repo: "acme-workers", HubURL: "https://hub.example.com"})
	if err := os.WriteFile(filepath.Join(dir, "wrangler.json"), []byte(`{"name":"acme-workers"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if environments != "" {
		if err := os.WriteFile(filepath.Join(dir, ".environments"), []byte(environments), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// recordingFetch returns the given values and records the keys it was asked for.
func recordingFetch(asked *[]string, values map[string]string, missing []string) envsync.HubFetcher {
	return func(_ context.Context, _, _, _ string, keys []string) (map[string]string, []string, error) {
		*asked = append(*asked, keys...)
		return values, missing, nil
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// assignedKeys returns the keys of every non-comment KEY=value line.
func assignedKeys(content string) []string {
	var keys []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, _, ok := strings.Cut(line, "="); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

func lastNonEmptyLine(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestWorkers_WritesDevVars0600_NotDotEnv(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	var asked []string
	d.Fetch = recordingFetch(&asked, map[string]string{"OPENAI_API_KEY": "sk-test", "PUBLIC_FLAG": "on"}, nil)

	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".dev.vars"))
	if err != nil {
		t.Fatalf(".dev.vars not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".dev.vars mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("a Workers sync must not write .env (stat err = %v)", err)
	}
}

func TestWorkers_KeysComeFromEnvironments(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	var asked []string
	d.Fetch = recordingFetch(&asked, map[string]string{"OPENAI_API_KEY": "sk-test", "PUBLIC_FLAG": "on"}, nil)

	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sort.Strings(asked)
	if strings.Join(asked, ",") != "OPENAI_API_KEY,PUBLIC_FLAG" {
		t.Fatalf("fetched keys = %v, want operator-required + build-time only (no platform-auto, skip or KEYTO_*)", asked)
	}
	content := readFile(t, filepath.Join(dir, ".dev.vars"))
	if envVal(content, "OPENAI_API_KEY") != "sk-test" || envVal(content, "PUBLIC_FLAG") != "on" {
		t.Fatalf("fetched values missing from .dev.vars:\n%s", content)
	}
	if strings.Contains(content, "R2_ACCESS_KEY_ID=") || strings.Contains(content, "IGNORED_THING=") {
		t.Fatalf("platform-auto / skip keys must not be written:\n%s", content)
	}
}

func TestWorkers_DropsEveryKeytoKeyFromTheHub(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	var asked []string
	d.Fetch = recordingFetch(&asked, map[string]string{
		"OPENAI_API_KEY":         "sk-test",
		"KEYTO_AUTH_CLIENT_ID":   "should-not-appear",
		"KEYTO_AUTH_SESSION_KEY": "should-not-appear",
	}, nil)

	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := readFile(t, filepath.Join(dir, ".dev.vars"))
	if strings.Contains(content, "should-not-appear") {
		t.Fatalf("a KEYTO_* value from the Hub reached .dev.vars:\n%s", content)
	}
	for _, k := range assignedKeys(content) {
		if strings.HasPrefix(k, "KEYTO_") && k != "KEYTO_DEV_USER" {
			t.Fatalf(".dev.vars assigns %s; only KEYTO_DEV_USER is allowed:\n%s", k, content)
		}
	}
}

func TestWorkers_LastLineIsDevUser(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	var asked []string
	d.Fetch = recordingFetch(&asked, map[string]string{"OPENAI_API_KEY": "sk-test"}, []string{"PUBLIC_FLAG"})

	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := readFile(t, filepath.Join(dir, ".dev.vars"))
	if got := lastNonEmptyLine(content); got != "KEYTO_DEV_USER=1" {
		t.Fatalf("last line = %q, want KEYTO_DEV_USER=1\n%s", got, content)
	}
	if n := strings.Count(content, "KEYTO_DEV_USER="); n != 1 {
		t.Fatalf("KEYTO_DEV_USER appears %d times, want 1", n)
	}
	if !strings.Contains(content, "# MISSING: PUBLIC_FLAG") {
		t.Fatalf("a key the Hub has no value for should be marked MISSING:\n%s", content)
	}
}

func TestWorkers_LocalOverridesSurviveASecondSync(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	var asked []string
	d.Fetch = recordingFetch(&asked, map[string]string{"OPENAI_API_KEY": "sk-first"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	path := filepath.Join(dir, ".dev.vars")
	first := readFile(t, path)
	overrides := "# local overrides below\nMY_LOCAL_FLAG=yes\n# a note to self\nOPENAI_API_KEY=sk-mine\n"
	if err := os.WriteFile(path, []byte(first+"\n"+overrides), 0o600); err != nil {
		t.Fatal(err)
	}

	d.Fetch = recordingFetch(&asked, map[string]string{"OPENAI_API_KEY": "sk-second"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	second := readFile(t, path)
	if !strings.HasSuffix(second, overrides) {
		t.Fatalf("the local overrides section was not kept verbatim at the end:\n%s", second)
	}
	if !strings.Contains(second, "OPENAI_API_KEY=sk-second") {
		t.Fatalf("the managed section was not refreshed:\n%s", second)
	}
	if strings.Contains(second, "sk-first") {
		t.Fatalf("the old managed value survived:\n%s", second)
	}
	if n := strings.Count(second, "\n# local overrides below\n"); n != 1 {
		t.Fatalf("overrides marker appears %d times, want 1:\n%s", n, second)
	}

	// A third sync is stable: nothing grows.
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if third := readFile(t, path); third != second {
		t.Fatalf("a re-sync changed the file:\n--- second\n%s\n--- third\n%s", second, third)
	}
}

func TestWorkers_KeytoKeysInLocalOverridesAreDisabled(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	path := filepath.Join(dir, ".dev.vars")
	if err := os.WriteFile(path, []byte("# local overrides below\nKEYTO_AUTH_CLIENT_ID=abc\nexport KEYTO_PUBLIC_URL=http://x\nKEYTO_DEV_USER=0\nOK_KEY=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	d.Out = out
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := readFile(t, path)
	for _, k := range assignedKeys(content) {
		if strings.HasPrefix(k, "KEYTO_") && k != "KEYTO_DEV_USER" {
			t.Fatalf(".dev.vars still assigns %s:\n%s", k, content)
		}
		if strings.HasPrefix(strings.TrimPrefix(k, "export "), "KEYTO_") && k != "KEYTO_DEV_USER" {
			t.Fatalf(".dev.vars still assigns %s:\n%s", k, content)
		}
	}
	if strings.Contains(content, "\nKEYTO_DEV_USER=0") {
		t.Fatalf("an override must not switch the dev user off:\n%s", content)
	}
	if !strings.Contains(content, "\nOK_KEY=1\n") {
		t.Fatalf("a non-KEYTO override was lost:\n%s", content)
	}
	if !strings.Contains(out.String(), "KEYTO_AUTH_CLIENT_ID") {
		t.Fatalf("the user should be told which override was disabled, got: %s", out.String())
	}
}

func TestWorkers_DoesNotReadEnvInventory(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	// A corrupt inventory proves the Workers path never opens it.
	if err := os.WriteFile(filepath.Join(dir, ".keyto", "env-inventory.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := baseDeps(t, dir)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v (the Workers path must not read .keyto/env-inventory.json)", err)
	}
}

func TestWorkers_MissingEnvironments_ErrorNamesIt(t *testing.T) {
	dir := makeWorkersDir(t, "")
	d := baseDeps(t, dir)
	err := envsync.Run(context.Background(), nil, d)
	if err == nil {
		t.Fatal("expected an error for a missing .environments")
	}
	if !strings.Contains(err.Error(), ".environments") || strings.Contains(err.Error(), "scan:env") {
		t.Fatalf("error should name .environments and not the scan:env hint, got: %v", err)
	}
}

func TestWorkers_EmptySecrets_NoFetch_DevUserOnly(t *testing.T) {
	dir := makeWorkersDir(t, "schemaVersion: 1\nstack: workers\nsecrets: []\n")
	d := baseDeps(t, dir)
	d.Fetch = func(context.Context, string, string, string, []string) (map[string]string, []string, error) {
		t.Fatal("Fetch must not be called when .environments lists no keys")
		return nil, nil, nil
	}
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := readFile(t, filepath.Join(dir, ".dev.vars"))
	if keys := assignedKeys(content); strings.Join(keys, ",") != "KEYTO_DEV_USER" {
		t.Fatalf("assigned keys = %v, want only KEYTO_DEV_USER", keys)
	}
}

func TestWorkers_PrintWritesToOut(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	d := baseDeps(t, dir)
	out := &bytes.Buffer{}
	d.Out = out
	if err := envsync.Run(context.Background(), []string{"--print"}, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lastNonEmptyLine(out.String()) != "KEYTO_DEV_USER=1" {
		t.Fatalf("--print output should be the .dev.vars content, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".dev.vars")); !os.IsNotExist(err) {
		t.Fatal("--print must not write .dev.vars")
	}
}

func TestWorkers_BadSchemaVersion_Errors(t *testing.T) {
	dir := makeWorkersDir(t, "schemaVersion: 2\nsecrets: []\n")
	d := baseDeps(t, dir)
	if err := envsync.Run(context.Background(), nil, d); err == nil || !strings.Contains(err.Error(), ".environments") {
		t.Fatalf("want a .environments schemaVersion error, got %v", err)
	}
}
