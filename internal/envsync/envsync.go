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
