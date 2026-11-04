package envsync_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// A cloned repo is untrusted: it can ship .env or .dev.vars as a symlink to
// any file the user can write (~/.ssh/authorized_keys, a shell rc). env sync
// must never follow one, for reading or writing.

const victimContent = "ssh-ed25519 AAAA victim@laptop\n"

// plantVictim writes a file outside the repo and returns its path.
func plantVictim(t *testing.T) string {
	t.Helper()
	victim := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(victim, []byte(victimContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return victim
}

func assertVictimUntouched(t *testing.T, victim string) {
	t.Helper()
	if got := readFile(t, victim); got != victimContent {
		t.Fatalf("the symlink target was modified:\n%s", got)
	}
}

func makeNextJSDir(t *testing.T) string {
	t.Helper()
	dir := makeProjectDir(t, &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"})
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys:          []inventoryKey{{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{"lib/email.ts:4"}}},
	})
	return dir
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "keyto-tmp") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}

func TestSafeWrite_SymlinkedDotEnv_Refused(t *testing.T) {
	dir := makeNextJSDir(t)
	victim := plantVictim(t)
	if err := os.Symlink(victim, filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(map[string]string{"SENDGRID_API_KEY": "sg_secret"}, nil)

	err := envsync.Run(context.Background(), nil, d)
	if err == nil || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal naming .env, got %v", err)
	}
	assertVictimUntouched(t, victim)
	assertNoTempFiles(t, dir)
}

func TestSafeWrite_SymlinkedDevVars_Refused(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	victim := plantVictim(t)
	if err := os.Symlink(victim, filepath.Join(dir, ".dev.vars")); err != nil {
		t.Fatal(err)
	}
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(map[string]string{"OPENAI_API_KEY": "sk-secret"}, nil)

	err := envsync.Run(context.Background(), nil, d)
	if err == nil || !strings.Contains(err.Error(), ".dev.vars") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal naming .dev.vars, got %v", err)
	}
	assertVictimUntouched(t, victim)
	assertNoTempFiles(t, dir)
}

func TestSafeWrite_SymlinkedDevVarsWithOverridesMarker_NotRead(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	// The target carries a marker, so following the link would copy its
	// content into .dev.vars (a read primitive) as well as overwrite it.
	victim := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(victim, []byte("# local overrides below\nPRIVATE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, ".dev.vars")); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	d := baseDeps(t, dir)
	d.Out = out
	if err := envsync.Run(context.Background(), []string{"--print"}, d); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal even with --print, got %v", err)
	}
	if strings.Contains(out.String(), "PRIVATE=1") {
		t.Fatalf("the symlink target was read:\n%s", out.String())
	}
}

func TestSafeWrite_DirectoryAtDevVars_Refused(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	if err := os.Mkdir(filepath.Join(dir, ".dev.vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := envsync.Run(context.Background(), nil, baseDeps(t, dir))
	if err == nil || !strings.Contains(err.Error(), ".dev.vars") || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("want a not-a-regular-file refusal naming .dev.vars, got %v", err)
	}
}

func TestSafeWrite_DirectoryAtDotEnv_Refused(t *testing.T) {
	dir := makeNextJSDir(t)
	if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := envsync.Run(context.Background(), nil, baseDeps(t, dir))
	if err == nil || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("want a not-a-regular-file refusal naming .env, got %v", err)
	}
}

func TestSafeWrite_ReplacesLooseModeWith0600(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		mk         func(*testing.T) string
	}{
		{"nextjs", ".env", makeNextJSDir},
		{"workers", ".dev.vars", func(t *testing.T) string { return makeWorkersDir(t, workersEnvironments) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.mk(t)
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte("# local overrides below\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := envsync.Run(context.Background(), nil, baseDeps(t, dir)); err != nil {
				t.Fatalf("Run: %v", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v, want a regular 0600 file", tc.file, info.Mode())
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// ---- one-time backup of a .dev.vars env sync didn't write ----

func TestBackup_HandWrittenDevVarsIsKeptOnce(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	path := filepath.Join(dir, ".dev.vars")
	backup := path + ".keyto-backup"
	const handWritten = "API_TOKEN=x\nKEYTO_DEV_USER=1\n"
	if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	d := baseDeps(t, dir)
	d.Out = out
	d.Fetch = staticFetch(map[string]string{"OPENAI_API_KEY": "sk-one"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, backup); got != handWritten {
		t.Fatalf("backup = %q, want the hand-written file", got)
	}
	info, err := os.Lstat(backup)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v (err %v), want 0600", info.Mode(), err)
	}
	if !strings.Contains(out.String(), backup) {
		t.Fatalf("the user should be told where the backup went, got: %s", out.String())
	}

	// A later sync with new Hub values must not replace the backup with
	// env sync's own output.
	d.Fetch = staticFetch(map[string]string{"OPENAI_API_KEY": "sk-two"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := readFile(t, backup); got != handWritten {
		t.Fatalf("the backup was overwritten: %q", got)
	}
}

func TestBackup_LaterHandWrittenDevVarsGetsANumberedBackup(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	path := filepath.Join(dir, ".dev.vars")
	backup := path + ".keyto-backup"
	sync := func(handWritten string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
			t.Fatal(err)
		}
		out := &bytes.Buffer{}
		d := baseDeps(t, dir)
		d.Out = out
		if err := envsync.Run(context.Background(), nil, d); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return out.String()
	}
	const first, second = "API_TOKEN=first\n", "API_TOKEN=second\n"
	sync(first)
	out := sync(second)

	if got := readFile(t, backup); got != first {
		t.Fatalf("the first backup changed: %q", got)
	}
	if got := readFile(t, backup+"-2"); got != second {
		t.Fatalf("second backup = %q, want the second hand-written file", got)
	}
	if info, err := os.Lstat(backup + "-2"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("second backup mode = %v (err %v), want 0600", info.Mode(), err)
	}
	if !strings.Contains(out, backup+"-2") {
		t.Fatalf("the user should be told where the second backup went, got: %s", out)
	}

	// The same hand-written content again is already saved: no third copy.
	out = sync(second)
	if _, err := os.Lstat(backup + "-3"); !os.IsNotExist(err) {
		t.Fatalf("a file identical to an existing backup was backed up again (err %v)", err)
	}
	if !strings.Contains(out, backup+"-2") {
		t.Fatalf("the user should be told the copy is already at %s-2, got: %s", backup, out)
	}
}

func TestBackup_NotTakenForOwnOutputOrWithMarker(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	path := filepath.Join(dir, ".dev.vars")
	backup := path + ".keyto-backup"
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(map[string]string{"OPENAI_API_KEY": "sk-one"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatal(err)
	}
	// env sync's own output, re-synced with different values: no backup.
	d.Fetch = staticFetch(map[string]string{"OPENAI_API_KEY": "sk-two"}, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("no backup expected for env sync's own output (err %v)", err)
	}

	// A file with an overrides section keeps it, so needs no backup.
	if err := os.WriteFile(path, []byte("OLD=1\n# local overrides below\nMINE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("no backup expected when an overrides section exists (err %v)", err)
	}
}

func TestBackup_SymlinkedBackupPath_Refused(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	path := filepath.Join(dir, ".dev.vars")
	if err := os.WriteFile(path, []byte("API_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	victim := plantVictim(t)
	if err := os.Symlink(victim, path+".keyto-backup"); err != nil {
		t.Fatal(err)
	}
	err := envsync.Run(context.Background(), nil, baseDeps(t, dir))
	if err == nil || !strings.Contains(err.Error(), ".keyto-backup") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal naming the backup, got %v", err)
	}
	assertVictimUntouched(t, victim)
	if got := readFile(t, path); got != "API_TOKEN=x\n" {
		t.Fatalf(".dev.vars must be left alone when the backup can't be made, got %q", got)
	}
}

func TestSafeWrite_RemovesStaleTempFilesOnly(t *testing.T) {
	dir := makeWorkersDir(t, workersEnvironments)
	// The writer names its temp files base + ".keyto-tmp-" + 16 hex +
	// ".local"; before that it used "." + base + ".keyto-tmp-" + 16 hex.
	stale := filepath.Join(dir, ".dev.vars.keyto-tmp-0123456789abcdef.local")
	if err := os.WriteFile(stale, []byte("OLD_SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleOldName := filepath.Join(dir, "..dev.vars.keyto-tmp-0123456789abcdef")
	if err := os.WriteFile(staleOldName, []byte("OLD_SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notOurs := filepath.Join(dir, "..dev.vars.keyto-tmp-notes")
	if err := os.WriteFile(notOurs, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	victim := plantVictim(t)
	link := filepath.Join(dir, "..dev.vars.keyto-tmp-fedcba9876543210")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	if err := envsync.Run(context.Background(), nil, baseDeps(t, dir)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, p := range []string{stale, staleOldName} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("a stale temp file was not removed: %s (err %v)", filepath.Base(p), err)
		}
	}
	if _, err := os.Lstat(notOurs); err != nil {
		t.Fatalf("a file not matching the exact temp pattern was removed: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("a symlink matching the pattern must be left alone (err %v)", err)
	}
	assertVictimUntouched(t, victim)
}

// templateIgnores are the env-file patterns in each template's .gitignore
// (hemfrid/keyto-hub-nextjs-template and hemfrid/keyto-hub-workers-template).
var templateIgnores = map[string][]string{
	"nextjs":  {".env*.local", ".env"},
	"workers": {".dev.vars", ".dev.vars.*", "!.dev.vars.example", ".env", ".env.*"},
}

// gitIgnored applies gitignore patterns without a slash to a file name, the
// way git does: the last matching pattern wins and "!" re-includes.
func gitIgnored(t *testing.T, patterns []string, name string) bool {
	t.Helper()
	ignored := false
	for _, p := range patterns {
		negate := strings.HasPrefix(p, "!")
		ok, err := filepath.Match(strings.TrimPrefix(p, "!"), name)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			ignored = !negate
		}
	}
	return ignored
}

// A sync killed between creating its temp file and renaming it leaves a file
// of secrets behind; the templates' .gitignore must already cover its name so
// `git add -A` can't pick it up before the next sync removes it.
func TestSafeWrite_TempFileNamesAreGitIgnoredByTheTemplates(t *testing.T) {
	for template, bases := range map[string][]string{
		"nextjs":  {".env"},
		"workers": {".dev.vars", ".dev.vars.keyto-backup", ".env"},
	} {
		for _, base := range bases {
			name := envsync.TempFileName(base, "0123456789abcdef")
			if !gitIgnored(t, templateIgnores[template], name) {
				t.Errorf("%s template: temp file %q is not git-ignored", template, name)
			}
		}
	}
}
