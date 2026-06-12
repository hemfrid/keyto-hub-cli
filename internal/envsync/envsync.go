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
	"hash/fnv"
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

var nonIdentRe = regexp.MustCompile(`[^a-z0-9_]`)

// normalizeProjectName converts a project name to a database-safe identifier
// (lower-case, hyphens → underscores, non-alnum-underscore stripped).
func normalizeProjectName(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "-", "_")
	s = nonIdentRe.ReplaceAllString(s, "")
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

// composeNameRe strips characters Docker Compose forbids in a project name
// (must match [a-z0-9_-]; must start with [a-z0-9]).
var composeNameRe = regexp.MustCompile(`[^a-z0-9_-]`)

// normalizeComposeProjectName turns a project name into a valid, project-unique
// Docker Compose project name. This is what gives every locally-started project
// its OWN containers + volumes + network instead of the shared `keyto-app`
// default — so each project's POSTGRES_DB is created on its own fresh volume
// (the shared volume only honors POSTGRES_DB on its first init, which is why a
// second project saw "database does not exist").
func normalizeComposeProjectName(name string) string {
	s := composeNameRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.TrimLeft(s, "-_")
	if s == "" {
		s = "app"
	}
	return s
}

// servicePorts holds the per-project HOST ports the backing services bind. The
// container-internal ports stay standard (5432/3306/6379); only the host side
// is offset per project so two projects' stacks can run at once and a new
// project never collides with another project's already-bound port.
type servicePorts struct {
	Postgres int
	MySQL    int
	Redis    int
}

// portOffset derives a stable 0..3999 offset from the project name. Same
// project → same ports on every sync (so DATABASE_URL stays valid across runs);
// different projects almost always differ. A rare collision surfaces as a
// docker-compose "port already allocated" error, resolvable with a
// POSTGRES_PORT override in .env.local.
func portOffset(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % 4000)
}

// defaultPorts returns the per-project host ports. The bases are chosen to be
// memorable (15432≈pg 5432, 23306≈mysql 3306, 27000≈redis) and to occupy
// non-overlapping unprivileged bands below the ephemeral range.
func defaultPorts(projectName string) servicePorts {
	off := portOffset(projectName)
	return servicePorts{
		Postgres: 15000 + off, // 15000–18999
		MySQL:    23000 + off, // 23000–26999
		Redis:    27000 + off, // 27000–30999
	}
}

// buildContainerValue resolves the local value for a container-hinted key.
// pg/mysql shared params and the per-project host ports are computed by the
// caller. The host URLs (127.0.0.1:<port>) MUST use the same per-project ports
// the compose file binds via ${POSTGRES_PORT}/${REDIS_PORT}/${MYSQL_PORT}.
func buildContainerValue(key, service string, pg postgresParams, my mysqlParams, ports servicePorts) string {
	switch service {
	case "postgres":
		switch key {
		case "DATABASE_URL":
			return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s", pg.User, pg.Password, ports.Postgres, pg.DB)
		case "PGHOST":
			return "127.0.0.1"
		case "PGPORT":
			return fmt.Sprintf("%d", ports.Postgres)
		case "PGUSER":
			return pg.User
		case "PGPASSWORD":
			return pg.Password
		case "PGDATABASE":
			return pg.DB
		default:
			// Unknown postgres key: return a placeholder URL
			return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s", pg.User, pg.Password, ports.Postgres, pg.DB)
		}
	case "redis":
		switch key {
		case "REDIS_URL":
			return fmt.Sprintf("redis://127.0.0.1:%d", ports.Redis)
		case "REDIS_HOST":
			return "127.0.0.1"
		case "REDIS_PORT":
			return fmt.Sprintf("%d", ports.Redis)
		case "REDIS_PASSWORD":
			return ""
		default:
			return fmt.Sprintf("redis://127.0.0.1:%d", ports.Redis)
		}
	case "mysql":
		switch key {
		case "MYSQL_URL":
			return fmt.Sprintf("mysql://%s:%s@127.0.0.1:%d/%s", my.User, my.Password, ports.MySQL, my.Database)
		case "MYSQL_HOST":
			return "127.0.0.1"
		case "MYSQL_PORT":
			return fmt.Sprintf("%d", ports.MySQL)
		case "MYSQL_USER":
			return my.User
		case "MYSQL_PASSWORD":
			return my.Password
		case "MYSQL_DATABASE":
			return my.Database
		default:
			return fmt.Sprintf("mysql://%s:%s@127.0.0.1:%d/%s", my.User, my.Password, ports.MySQL, my.Database)
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

// quoteEnvValue wraps a value in double quotes (escaping where needed) when it
// contains characters that would corrupt a .env line — whitespace, '#', quotes,
// backslash, or newlines. Plain values are returned as-is so docker-compose
// ${VAR} substitution and `next dev` both read them unchanged.
func quoteEnvValue(v string) string {
	if v == "" || !strings.ContainsAny(v, " \t\r\n#\"'\\") {
		return v
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + r.Replace(v) + `"`
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

	// COMPOSE_PROJECT_NAME gives this project its own Docker compose project —
	// unique containers, volumes and network — instead of the shared `keyto-app`
	// default every project would otherwise collapse into.
	sb.WriteString(fmt.Sprintf("COMPOSE_PROJECT_NAME=%s\n", normalizeComposeProjectName(projectName)))

	profiles := inferProfiles(inv.Keys)
	sb.WriteString("COMPOSE_PROFILES=")
	sb.WriteString(strings.Join(profiles, ","))
	sb.WriteString("\n\n")

	pg := defaultPostgresParams(projectName)
	my := defaultMySQLParams(projectName)
	ports := defaultPorts(projectName)

	// Shared postgres params block
	if hasService(inv.Keys, "postgres") {
		sb.WriteString("# Postgres shared credentials\n")
		sb.WriteString(fmt.Sprintf("POSTGRES_USER=%s\n", pg.User))
		sb.WriteString(fmt.Sprintf("POSTGRES_PASSWORD=%s\n", pg.Password))
		sb.WriteString(fmt.Sprintf("POSTGRES_DB=%s\n", pg.DB))
		sb.WriteString(fmt.Sprintf("POSTGRES_PORT=%d\n", ports.Postgres))
		sb.WriteString("\n")
	}

	// Shared MySQL params block
	if hasService(inv.Keys, "mysql") {
		sb.WriteString("# MySQL shared credentials\n")
		sb.WriteString(fmt.Sprintf("MYSQL_USER=%s\n", my.User))
		sb.WriteString(fmt.Sprintf("MYSQL_PASSWORD=%s\n", my.Password))
		sb.WriteString(fmt.Sprintf("MYSQL_ROOT_PASSWORD=%s\n", my.RootPassword))
		sb.WriteString(fmt.Sprintf("MYSQL_DATABASE=%s\n", my.Database))
		sb.WriteString(fmt.Sprintf("MYSQL_PORT=%d\n", ports.MySQL))
		sb.WriteString("\n")
	}

	// Redis/Dragonfly host port (both map ${REDIS_PORT:-6379} in compose).
	if hasService(inv.Keys, "redis") || hasService(inv.Keys, "dragonfly") {
		sb.WriteString("# Redis/Dragonfly host port\n")
		sb.WriteString(fmt.Sprintf("REDIS_PORT=%d\n", ports.Redis))
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
			val := buildContainerValue(k.Key, k.Service, pg, my, ports)
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
				sb.WriteString(fmt.Sprintf("%s=%s\n", k.Key, quoteEnvValue(val)))
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
