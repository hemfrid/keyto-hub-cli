package envsync

import (
	"context"
	"fmt"
	"io"
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
	maxBackups           = 100
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
	data, ok, err := readRegularFile(filepath.Join(cwd, environmentsFile))
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("env sync: no %s in this repo — a Workers project lists its secrets there; restore it with `git checkout -- %s`", environmentsFile, environmentsFile)
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
		case isPlatformKey(s.Key):
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
	lines, seps := splitDotenvLines(existing)
	start := -1
	for i, line := range lines {
		if isOverridesMarker(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false, nil
	}
	var sb strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if key, ok := dotenvKey(line); ok && isPlatformKey(key) {
			line = fmt.Sprintf("# removed %s: KEYTO_* keys are set by the platform, never in %s", key, DevVarsFile)
			disabled = append(disabled, key)
		}
		sb.WriteString(line)
		sb.WriteString(seps[i])
	}
	section = sb.String()
	if !strings.HasSuffix(section, "\n") {
		section += "\n"
	}
	return section, true, disabled
}

// backupForeignDevVars copies a .dev.vars env sync didn't write (no managed
// header, no overrides section) aside before it is replaced, so hand edits
// aren't lost: to .dev.vars.keyto-backup, then -2, -3 and so on. A backup is
// never overwritten, and content already saved in one isn't saved again.
func backupForeignDevVars(dest, existing, next string, out io.Writer) error {
	if existing == next || strings.HasPrefix(existing, devVarsHeader) {
		return nil
	}
	for n := 1; n <= maxBackups; n++ {
		backup := dest + backupSuffix
		if n > 1 {
			backup += fmt.Sprintf("-%d", n)
		}
		saved, exists, err := readRegularFile(backup)
		if err != nil {
			return err
		}
		if exists {
			if string(saved) == existing {
				fmt.Fprintf(out, "note: %s was not written by env sync; replacing it (this content is already saved at %s)\n", dest, backup)
				return nil
			}
			continue
		}
		if err := writeEnvFile(backup, existing); err != nil {
			return err
		}
		fmt.Fprintf(out, "note: %s was not written by env sync; your copy is saved at %s\n", dest, backup)
		return nil
	}
	return fmt.Errorf("env sync: %s was not written by env sync and there are already %d backups of it (%s*); remove the ones you don't need and re-run", dest, maxBackups, dest+backupSuffix)
}

// wrangler reads .dev.vars with the dotenv package: it turns \r\n and \r into
// \n and matches each assignment with a multiline JavaScript regex,
//
//	^\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)…
//
// where ^ also matches after U+2028/U+2029 and \s includes Unicode spaces.
// The filter below finds keys the same way, so no spelling of a KEYTO_* key
// (a colon, a tab, extra spaces, lower case, a Unicode space or line break)
// gets past it. It over-matches on purpose: a line whose first token is a
// KEYTO_ key is removed even if dotenv would not parse it.
var (
	dotenvLineSep = regexp.MustCompile(`\r\n|\r|\n|\x{2028}|\x{2029}`)
	jsSpace       = `[\s\v\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
	dotenvKeyRe   = regexp.MustCompile(`^` + jsSpace + `*(?:export` + jsSpace + `+)?([\w.-]+)`)
)

// splitDotenvLines splits s at every line break dotenv's ^ recognises and
// returns the lines plus the separator after each ("" after the last), so
// the text can be rebuilt byte for byte.
func splitDotenvLines(s string) (lines, seps []string) {
	prev := 0
	for _, loc := range dotenvLineSep.FindAllStringIndex(s, -1) {
		lines = append(lines, s[prev:loc[0]])
		seps = append(seps, s[loc[0]:loc[1]])
		prev = loc[1]
	}
	return append(lines, s[prev:]), append(seps, "")
}

// dotenvKey returns the leading key token of a dotenv line ("export KEY=v"
// included).
func dotenvKey(line string) (string, bool) {
	m := dotenvKeyRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// isPlatformKey reports whether key is a KEYTO_* key, in any letter case.
func isPlatformKey(key string) bool {
	return len(key) >= len(platformKeyPrefix) && strings.EqualFold(key[:len(platformKeyPrefix)], platformKeyPrefix)
}

// unwritableValue is a key left out of .dev.vars because no spelling of its
// value reads back unchanged; reason never contains the value.
type unwritableValue struct {
	key, reason string
}

// renderDevVars builds the managed part of .dev.vars. KEYTO_DEV_USER=1 is its
// last line; the Hub's values never include a KEYTO_* key. Values are quoted
// for wrangler's dotenv (quoteDevVarsValue), not with the .env rules.
func renderDevVars(keys []string, values map[string]string, missing []string, targetEnv string) (string, []unwritableValue) {
	missingSet := map[string]bool{}
	for _, k := range missing {
		missingSet[k] = true
	}
	var sb strings.Builder
	var unwritable []unwritableValue
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
			quoted, err := quoteDevVarsValue(val)
			if err != nil {
				fmt.Fprintf(&sb, "# NOT WRITTEN: %s (its value can't be written so wrangler reads it back unchanged: %s)\n", k, err)
				unwritable = append(unwritable, unwritableValue{key: k, reason: err.Error()})
				continue
			}
			fmt.Fprintf(&sb, "%s=%s\n", k, quoted)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("# Signs you in as the local dev user (@hemfrid/workers-auth).\n")
	sb.WriteString(devUserLine + "\n")
	return sb.String(), unwritable
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
		fmt.Fprintf(d.Out, "note: %s lists %q; not synced (KEYTO_* keys are set by the platform, and a key must be a plain env name)\n", environmentsFile, k)
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

	content, unwritable := renderDevVars(keys, values, missing, targetEnv)
	for _, u := range unwritable {
		fmt.Fprintf(d.Out, "note: %s left out of %s: its value can't be written so wrangler reads it back unchanged (%s); change it in the Hub (%s) to use it locally\n", u.key, DevVarsFile, u.reason, targetEnv)
	}
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
