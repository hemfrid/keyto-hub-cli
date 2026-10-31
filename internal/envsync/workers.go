package envsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// A Workers repo (hemfrid/keyto-hub-workers-template) has no Docker services
// and no .keyto/env-inventory.json. Its secrets inventory is .environments,
// and wrangler dev reads local values from .dev.vars.
//
// The dev user rule: .dev.vars carries KEYTO_DEV_USER=1 and NO other KEYTO_*
// key, because @hemfrid/workers-auth refuses the dev user when a platform key
// is present. So every KEYTO_* key is dropped — from .environments, from the
// Hub's answer and from the local overrides.

const (
	// DevVarsFile is the env file a Workers sync writes.
	DevVarsFile          = ".dev.vars"
	environmentsFile     = ".environments"
	localOverridesMarker = "# local overrides below"
	platformKeyPrefix    = "KEYTO_"
	devUserLine          = "KEYTO_DEV_USER=1"
	backupSuffix         = ".keyto-backup"
)

// envKeyRe is a dotenv-safe key: anything else could break the line format.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// unsyncedCategories are the .environments lanes with no value to fetch:
// platform-auto is provided by the platform, skip is tracked but unused.
var unsyncedCategories = map[string]bool{"platform-auto": true, "skip": true}

// environmentsDoc is the part of .environments (lib/introspect in keyto-hub)
// the sync reads.
type environmentsDoc struct {
	SchemaVersion int `yaml:"schemaVersion"`
	Secrets       []struct {
		Key      string `yaml:"key"`
		Category string `yaml:"category"`
	} `yaml:"secrets"`
}

const devVarsHeader = `# ============================================================
# Managed by keyto env sync. Edits above the local overrides line are lost.
# Re-run: keyto env sync
# Local overrides: end this file with the line "` + localOverridesMarker + `"
# and put your own KEY=value lines under it; env sync keeps them.
# KEYTO_* keys are set by the platform and never belong here.
# ============================================================
`

// readEnvironmentsKeys returns the keys to fetch from .environments, in file
// order: every entry except platform-auto and skip, minus KEYTO_* keys
// (returned separately so the caller can say why they were left out).
func readEnvironmentsKeys(cwd string) (keys, dropped []string, err error) {
	data, err := os.ReadFile(filepath.Join(cwd, environmentsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("env sync: no %s in this repo — a Workers project lists its secrets there; restore it with `git checkout -- %s`", environmentsFile, environmentsFile)
		}
		return nil, nil, fmt.Errorf("env sync: read %s: %w", environmentsFile, err)
	}
	var doc environmentsDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("env sync: parse %s: %w", environmentsFile, err)
	}
	if doc.SchemaVersion != 1 {
		return nil, nil, fmt.Errorf("env sync: %s has schemaVersion %d; this keyto understands 1 — run `keyto update`", environmentsFile, doc.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, s := range doc.Secrets {
		if unsyncedCategories[s.Category] || seen[s.Key] {
			continue
		}
		seen[s.Key] = true
		switch {
		case strings.HasPrefix(s.Key, platformKeyPrefix):
			dropped = append(dropped, s.Key)
		case envKeyRe.MatchString(s.Key):
			keys = append(keys, s.Key)
		default:
			dropped = append(dropped, s.Key)
		}
	}
	return keys, dropped, nil
}

// localOverrides returns the local overrides section of an existing
// .dev.vars — from the marker line to the end of the file — with every
// KEYTO_* assignment replaced by a comment. found=false when there is no
// marker. disabled names the KEYTO_* keys it neutralised.
func localOverrides(existing string) (section string, found bool, disabled []string) {
	lines := strings.Split(existing, "\n")
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), localOverridesMarker) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false, nil
	}
	kept := lines[start:]
	for i, line := range kept {
		if key, ok := assignedKey(line); ok && strings.HasPrefix(key, platformKeyPrefix) {
			kept[i] = fmt.Sprintf("# removed %s: KEYTO_* keys are set by the platform, never in %s", key, DevVarsFile)
			disabled = append(disabled, key)
		}
	}
	section = strings.Join(kept, "\n")
	if !strings.HasSuffix(section, "\n") {
		section += "\n"
	}
	return section, true, disabled
}

// backupForeignDevVars keeps a one-time copy of a .dev.vars env sync didn't
// write (no managed header, no overrides section) before it is replaced, so
// hand edits aren't lost. An existing backup is never overwritten: a later
// sync would only copy env sync's own output over the user's original.
func backupForeignDevVars(dest, existing, next string, out io.Writer) error {
	if existing == next || strings.HasPrefix(existing, devVarsHeader) {
		return nil
	}
	backup := dest + backupSuffix
	exists, err := checkRegularOrAbsent(backup)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintf(out, "note: %s was not written by env sync; replacing it (an earlier backup is kept at %s)\n", dest, backup)
		return nil
	}
	if err := writeEnvFile(backup, existing); err != nil {
		return err
	}
	fmt.Fprintf(out, "note: %s was not written by env sync; your copy is saved at %s\n", dest, backup)
	return nil
}

// assignedKey returns the key a dotenv line assigns ("export KEY=v" included).
func assignedKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
	key, _, ok := strings.Cut(t, "=")
	return strings.TrimSpace(key), ok
}

// renderDevVars builds the managed part of .dev.vars. KEYTO_DEV_USER=1 is its
// last line; the Hub's values never include a KEYTO_* key.
func renderDevVars(keys []string, values map[string]string, missing []string, targetEnv string) string {
	missingSet := map[string]bool{}
	for _, k := range missing {
		missingSet[k] = true
	}
	var sb strings.Builder
	sb.WriteString(devVarsHeader)
	sb.WriteString("\n")
	if len(keys) > 0 {
		fmt.Fprintf(&sb, "# Secrets from %s (fetched via Hub)\n", targetEnv)
		for _, k := range keys {
			val, ok := values[k]
			if missingSet[k] || !ok {
				fmt.Fprintf(&sb, "# MISSING: %s (not set in %s)\n", k, targetEnv)
				continue
			}
			fmt.Fprintf(&sb, "%s=%s\n", k, quoteEnvValue(val))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("# Signs you in as the local dev user (@hemfrid/workers-auth).\n")
	sb.WriteString(devUserLine + "\n")
	return sb.String()
}

// runWorkers is `keyto env sync` for a Workers repo: .dev.vars from
// .environments, the same batched Hub fetch, the dev user appended last and
// the local overrides section kept.
func runWorkers(ctx context.Context, d Deps, marker *project.Marker, targetEnv, outPath string, printMode bool) error {
	keys, dropped, err := readEnvironmentsKeys(d.Cwd)
	if err != nil {
		return err
	}
	for _, k := range dropped {
		fmt.Fprintf(d.Out, "note: %s lists %s; not synced (KEYTO_* keys are set by the platform, and a key must be a plain env name)\n", environmentsFile, k)
	}

	values := map[string]string{}
	var missing []string
	if len(keys) > 0 {
		values, missing, err = d.Fetch(ctx, marker.Org, marker.Repo, targetEnv, keys)
		if err != nil {
			return fmt.Errorf("env sync: fetch values from Hub: %w", err)
		}
	}

	dest := outPath
	if dest == "" {
		dest = filepath.Join(d.Cwd, DevVarsFile)
	}
	// Read the current file without following a symlink: a cloned repo can
	// ship .dev.vars as a link to any file, and its content would be copied
	// into the overrides section.
	data, exists, err := readRegularFile(dest)
	if err != nil {
		return err
	}
	existing := string(data)
	overrides, hasOverrides, disabled := localOverrides(existing)
	for _, k := range disabled {
		fmt.Fprintf(d.Out, "note: removed %s from the %s local overrides — any KEYTO_* key there turns the local dev user off\n", k, DevVarsFile)
	}

	content := renderDevVars(keys, values, missing, targetEnv)
	if overrides != "" {
		content += "\n" + overrides
	}

	if printMode {
		_, err := fmt.Fprint(d.Out, content)
		return err
	}
	if exists && !hasOverrides {
		if err := backupForeignDevVars(dest, existing, content, d.Out); err != nil {
			return err
		}
	}
	if err := writeEnvFile(dest, content); err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "wrote %s\n", dest)
	return nil
}
