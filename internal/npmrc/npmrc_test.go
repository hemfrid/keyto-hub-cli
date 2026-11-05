package npmrc_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/npmrc"
)

const (
	hubURL   = "https://hub.example.com"
	tokenOne = "kt_cli_one_0123456789"
	tokenTwo = "kt_cli_two_9876543210"
)

const (
	laneRegistry = "https://hub.example.com/api/npm/"
	// legacyScopeLine is the user-wide line v0.9.x wrote inside the block.
	legacyScopeLine = "@hemfrid:registry=https://hub.example.com/api/npm/\n"
	// usersOwnScope is a developer's own GitHub Packages mapping, outside the
	// block, which keyto must never touch.
	usersOwnScope = "@hemfrid:registry=https://npm.pkg.github.com\n//npm.pkg.github.com/:_authToken=ghp_users_own\n"
)

// managedBlock is what keyto writes for hubURL and token: the host-scoped
// auth line only, no registry.
func managedBlock(token string) string {
	return npmrc.BeginMarker + "\n" +
		"//hub.example.com/api/npm/:_authToken=" + token + "\n" +
		npmrc.EndMarker + "\n"
}

// legacyBlock is what v0.9.x wrote: the same block with the user-wide
// @hemfrid scope line in it.
func legacyBlock(token string) string {
	return npmrc.BeginMarker + "\n" +
		legacyScopeLine +
		"//hub.example.com/api/npm/:_authToken=" + token + "\n" +
		npmrc.EndMarker + "\n"
}

// ensure runs Ensure and fails the test on an error.
func ensure(t *testing.T, d npmrc.Deps) string {
	t.Helper()
	registry, err := npmrc.Ensure(context.Background(), d)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return registry
}

func assertNoUserWideScope(t *testing.T, content string) {
	t.Helper()
	if strings.Contains(content, legacyScopeLine) {
		t.Fatalf("~/.npmrc maps every @hemfrid package to the Hub:\n%s", content)
	}
}

type probeRec struct{ calls int }

func (p *probeRec) served(ctx context.Context) (bool, string) { p.calls++; return true, "" }

func deps(t *testing.T, path, token string, out *bytes.Buffer, probe *probeRec) npmrc.Deps {
	t.Helper()
	var creds *config.Creds
	if token != "" {
		creds = &config.Creds{Credential: token, HubURL: hubURL, ExpiresAt: time.Now().Add(time.Hour)}
	}
	return npmrc.Deps{Path: path, Creds: creds, Probe: probe.served, Out: out}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v (err %v), want a regular 0600 file", path, info.Mode(), err)
	}
}

func assertNoToken(t *testing.T, s string) {
	t.Helper()
	for _, tok := range []string{tokenOne, tokenTwo} {
		if strings.Contains(s, tok) {
			t.Fatalf("the token was printed: %q", s)
		}
	}
}

func TestEnsure_FreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	out, probe := &bytes.Buffer{}, &probeRec{}
	if got := ensure(t, deps(t, path, tokenOne, out, probe)); got != laneRegistry {
		t.Fatalf("registry = %q, want %q", got, laneRegistry)
	}
	if got := read(t, path); got != managedBlock(tokenOne) {
		t.Fatalf("~/.npmrc =\n%s\nwant\n%s", got, managedBlock(tokenOne))
	}
	assertNoUserWideScope(t, read(t, path))
	assertMode0600(t, path)
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1", probe.calls)
	}
	assertNoToken(t, out.String())
	if !strings.Contains(out.String(), path) {
		t.Fatalf("the user should be told which file changed, got %q", out.String())
	}
}

func TestEnsure_ExistingFileWithOtherScopes_KeptByteForByte(t *testing.T) {
	for name, existing := range map[string]string{
		"trailing newline":         "registry=https://registry.npmjs.org/\n@acme:registry=https://npm.acme.dev/\n//npm.acme.dev/:_authToken=acme-secret\n",
		"no final newline":         "@acme:registry=https://npm.acme.dev/\n; a comment\n//npm.acme.dev/:_authToken=acme-secret",
		"crlf":                     "@acme:registry=https://npm.acme.dev/\r\nsave-exact=true\r\n",
		"users own @hemfrid scope": usersOwnScope,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".npmrc")
			write(t, path, existing, 0o644)
			out := &bytes.Buffer{}
			ensure(t, deps(t, path, tokenOne, out, &probeRec{}))
			got := read(t, path)
			prefix := existing
			if !strings.HasSuffix(prefix, "\n") {
				prefix += "\n"
			}
			if got != prefix+managedBlock(tokenOne) {
				t.Fatalf("~/.npmrc =\n%q\nwant the existing lines untouched, then keyto's block:\n%q", got, prefix+managedBlock(tokenOne))
			}
			assertMode0600(t, path)
			assertNoToken(t, out.String())
		})
	}
}

func TestEnsure_RotatesTokenInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	before := "@acme:registry=https://npm.acme.dev/\n"
	after := "save-exact=true\n"
	write(t, path, before+managedBlock(tokenOne)+after, 0o600)

	out, probe := &bytes.Buffer{}, &probeRec{}
	ensure(t, deps(t, path, tokenTwo, out, probe))
	if got := read(t, path); got != before+managedBlock(tokenTwo)+after {
		t.Fatalf("~/.npmrc =\n%s\nwant the block rewritten in place with the new token", got)
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1", probe.calls)
	}
	assertNoToken(t, out.String())
}

func TestEnsure_AlreadyCurrent_NoWriteNoProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	content := "save-exact=true\n" + managedBlock(tokenOne)
	write(t, path, content, 0o600)
	before, _ := os.Stat(path)

	out, probe := &bytes.Buffer{}, &probeRec{}
	if got := ensure(t, deps(t, path, tokenOne, out, probe)); got != laneRegistry {
		t.Fatalf("registry = %q, want %q (the line is in place)", got, laneRegistry)
	}
	after, _ := os.Stat(path)
	if read(t, path) != content || !os.SameFile(before, after) {
		t.Fatal("an up-to-date ~/.npmrc must not be rewritten")
	}
	if probe.calls != 0 {
		t.Fatalf("probe calls = %d, want 0 (nothing to change)", probe.calls)
	}
}

func TestEnsure_SymlinkedNpmrc_Refused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "dotfiles-npmrc")
	const targetContent = "@acme:registry=https://npm.acme.dev/\n"
	write(t, target, targetContent, 0o644)
	path := filepath.Join(dir, ".npmrc")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	out, probe := &bytes.Buffer{}, &probeRec{}
	registry, err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, out, probe))
	if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), path) {
		t.Fatalf("want a symlink refusal naming %s, got %v", path, err)
	}
	if registry != "" {
		t.Fatalf("registry = %q, want none (nothing written)", registry)
	}
	// The tip says what to add by hand, without the token itself, and never
	// the user-wide scope line.
	if !strings.Contains(err.Error(), "//hub.example.com/api/npm/:_authToken=") {
		t.Fatalf("the refusal should give the line to add by hand, got %v", err)
	}
	if strings.Contains(err.Error(), "@hemfrid:registry") {
		t.Fatalf("the refusal must not suggest a user-wide @hemfrid scope, got %v", err)
	}
	assertNoToken(t, err.Error()+out.String())
	if got := read(t, target); got != targetContent {
		t.Fatalf("the link target was modified: %q", got)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link itself must stay (err %v)", err)
	}
	if probe.calls != 0 {
		t.Fatalf("probe calls = %d, want 0", probe.calls)
	}
}

func TestEnsure_NoCredential_SoftTipAndNoWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	_, err := npmrc.Ensure(context.Background(), deps(t, path, "", &bytes.Buffer{}, &probeRec{}))
	if err == nil || !strings.Contains(err.Error(), "keyto auth") {
		t.Fatalf("want a tip pointing at keyto auth, got %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written without a credential (err %v)", err)
	}
}

func TestEnsure_ExpiredCredential_SoftTip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	d := deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{})
	d.Creds.ExpiresAt = time.Now().Add(-time.Hour)
	_, err := npmrc.Ensure(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "keyto auth") {
		t.Fatalf("want an expired-sign-in tip, got %v", err)
	}
	assertNoToken(t, err.Error())
}

func TestEnsure_HubWithoutTheLane_LeavesFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, "save-exact=true\n", 0o644)
	out := &bytes.Buffer{}
	d := deps(t, path, tokenOne, out, &probeRec{})
	d.Probe = func(ctx context.Context) (bool, string) { return false, "HTTP 404" }
	registry, err := npmrc.Ensure(context.Background(), d)
	if err != nil {
		t.Fatalf("a Hub without the lane is not an error, got %v", err)
	}
	if registry != "" {
		t.Fatalf("registry = %q, want none: the install must not be pointed at a Hub without the lane", registry)
	}
	if got := read(t, path); got != "save-exact=true\n" {
		t.Fatalf("~/.npmrc changed: %q", got)
	}
	if !strings.Contains(out.String(), "HTTP 404") || !strings.Contains(out.String(), "unchanged") {
		t.Fatalf("the user should be told why nothing changed, got %q", out.String())
	}
}

func TestEnsure_UnterminatedBlock_Refused(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	content := "save-exact=true\n" + npmrc.BeginMarker + "\n@hemfrid:registry=https://x/\nmine=1\n"
	write(t, path, content, 0o600)
	_, err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{}))
	if err == nil || !strings.Contains(err.Error(), npmrc.EndMarker) {
		t.Fatalf("want a refusal naming the missing end marker, got %v", err)
	}
	if read(t, path) != content {
		t.Fatal("a file with an unterminated block must be left alone")
	}
}

func TestEnsure_DuplicateBlocksCollapse(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, managedBlock(tokenOne)+"a=1\n"+managedBlock(tokenOne), 0o600)
	ensure(t, deps(t, path, tokenTwo, &bytes.Buffer{}, &probeRec{}))
	if got := read(t, path); got != managedBlock(tokenTwo)+"a=1\n" {
		t.Fatalf("~/.npmrc =\n%s\nwant one block, at the first one's place", got)
	}
}

func TestEnsure_LaterLineOverridingTheLane_Noted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, managedBlock(tokenOne)+"@hemfrid:registry = https://npm.pkg.github.com/\n//hub.example.com/api/npm/:_authToken=handwritten\n", 0o600)
	out := &bytes.Buffer{}
	ensure(t, deps(t, path, tokenTwo, out, &probeRec{}))
	// The block is lines 1-3. Line 4 is the user's own scope line: theirs to
	// keep, not a shadow of anything keyto writes. Line 5 overrides keyto's
	// token.
	if !strings.Contains(out.String(), "line 5 of") {
		t.Fatalf("the note should name line 5 only, got %q", out.String())
	}
	if strings.Contains(out.String(), "handwritten") || strings.Contains(out.String(), "npm.pkg.github.com") {
		t.Fatalf("the note must not print the lines themselves, got %q", out.String())
	}
}

func TestEnsure_HubOriginAndPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	d := deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{})
	d.Creds.HubURL = "http://localhost:3000/"
	if got := ensure(t, d); got != "http://localhost:3000/api/npm/" {
		t.Fatalf("registry = %q", got)
	}
	got := read(t, path)
	if want := "\n//localhost:3000/api/npm/:_authToken=" + tokenOne + "\n"; !strings.Contains(got, want) {
		t.Fatalf("~/.npmrc lacks %q:\n%s", want, got)
	}
	if strings.Contains(got, "@hemfrid:registry") {
		t.Fatalf("~/.npmrc must not map the scope:\n%s", got)
	}
}

func TestEnsure_TokenThatCouldInjectLines_Refused(t *testing.T) {
	for _, tok := range []string{"a\n@evil:registry=https://evil/", "a b", "a\rb", "a\tb"} {
		path := filepath.Join(t.TempDir(), ".npmrc")
		d := deps(t, path, tok, &bytes.Buffer{}, &probeRec{})
		if _, err := npmrc.Ensure(context.Background(), d); err == nil {
			t.Errorf("token %q: want a refusal", tok)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("token %q: ~/.npmrc was written", tok)
		}
	}
}

func TestUserConfigPath(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := npmrc.UserConfigPath(getenv, "/home/u"); got != filepath.Join("/home/u", ".npmrc") {
		t.Fatalf("default = %q", got)
	}
	env["NPM_CONFIG_USERCONFIG"] = "/etc/upper"
	if got := npmrc.UserConfigPath(getenv, "/home/u"); got != "/etc/upper" {
		t.Fatalf("NPM_CONFIG_USERCONFIG = %q", got)
	}
	env["npm_config_userconfig"] = "/etc/lower"
	if got := npmrc.UserConfigPath(getenv, "/home/u"); got != "/etc/lower" {
		t.Fatalf("npm_config_userconfig should win, got %q", got)
	}
}

// ---- the user-wide @hemfrid scope line v0.9.x wrote (Sean's review on #38) ----

func TestEnsure_LegacyScopeLine_RemovedAndOtherLinesKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	before := "save-exact=true\n" + usersOwnScope
	after := "; trailing comment\n"
	write(t, path, before+legacyBlock(tokenOne)+after, 0o600)

	out, probe := &bytes.Buffer{}, &probeRec{}
	if got := ensure(t, deps(t, path, tokenOne, out, probe)); got != laneRegistry {
		t.Fatalf("registry = %q, want %q", got, laneRegistry)
	}
	if got := read(t, path); got != before+managedBlock(tokenOne)+after {
		t.Fatalf("~/.npmrc =\n%s\nwant only keyto's scope line gone:\n%s", got, before+managedBlock(tokenOne)+after)
	}
	if probe.calls != 0 {
		t.Fatalf("probe calls = %d, want 0: the credential line didn't change", probe.calls)
	}
	if !strings.Contains(out.String(), "removed the user-wide @hemfrid:registry line") {
		t.Fatalf("the user should be told the scope line went, got %q", out.String())
	}
	assertMode0600(t, path)
	assertNoToken(t, out.String())
}

func TestEnsure_LegacyScopeLine_RemovedWithTokenRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, usersOwnScope+legacyBlock(tokenOne), 0o600)
	out, probe := &bytes.Buffer{}, &probeRec{}
	ensure(t, deps(t, path, tokenTwo, out, probe))
	if got := read(t, path); got != usersOwnScope+managedBlock(tokenTwo) {
		t.Fatalf("~/.npmrc =\n%s\nwant the new token and no keyto scope line", got)
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (the token changed)", probe.calls)
	}
	if !strings.Contains(out.String(), "removed the user-wide") {
		t.Fatalf("the user should be told the scope line went, got %q", out.String())
	}
}

func TestEnsure_LegacyScopeLine_RemovedEvenWithoutACredential(t *testing.T) {
	for name, mutate := range map[string]func(d *npmrc.Deps){
		"signed out": func(d *npmrc.Deps) { d.Creds = nil },
		"expired":    func(d *npmrc.Deps) { d.Creds.ExpiresAt = time.Now().Add(-time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".npmrc")
			write(t, path, usersOwnScope+legacyBlock(tokenOne), 0o600)
			out, probe := &bytes.Buffer{}, &probeRec{}
			d := deps(t, path, tokenOne, out, probe)
			mutate(&d)
			registry, err := npmrc.Ensure(context.Background(), d)
			if err == nil || !strings.Contains(err.Error(), "keyto auth") {
				t.Fatalf("want the keyto auth tip, got %v", err)
			}
			if registry != "" {
				t.Fatalf("registry = %q, want none", registry)
			}
			// The old credential line stays: only the scope line was harmful.
			if got := read(t, path); got != usersOwnScope+managedBlock(tokenOne) {
				t.Fatalf("~/.npmrc =\n%s\nwant only keyto's scope line gone", got)
			}
			if probe.calls != 0 {
				t.Fatalf("probe calls = %d, want 0", probe.calls)
			}
		})
	}
}

func TestEnsure_LegacyScopeLine_RemovedWhenHubDoesNotServeTheLane(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, legacyBlock(tokenOne), 0o600)
	out := &bytes.Buffer{}
	d := deps(t, path, tokenTwo, out, &probeRec{})
	d.Probe = func(ctx context.Context) (bool, string) { return false, "HTTP 404" }
	if got := ensure(t, d); got != "" {
		t.Fatalf("registry = %q, want none", got)
	}
	if got := read(t, path); got != managedBlock(tokenOne) {
		t.Fatalf("~/.npmrc =\n%s\nwant the scope line gone and the old token kept", got)
	}
}

func TestEnsure_LegacyCleanup_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, usersOwnScope+legacyBlock(tokenOne), 0o600)
	ensure(t, deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{}))
	first := read(t, path)
	firstInfo, _ := os.Stat(path)

	out, probe := &bytes.Buffer{}, &probeRec{}
	ensure(t, deps(t, path, tokenOne, out, probe))
	secondInfo, _ := os.Stat(path)
	if read(t, path) != first || !os.SameFile(firstInfo, secondInfo) {
		t.Fatal("a second run after the cleanup must not rewrite ~/.npmrc")
	}
	if probe.calls != 0 || strings.Contains(out.String(), "removed") {
		t.Fatalf("a second run has nothing to do (probe calls %d, out %q)", probe.calls, out.String())
	}
}

func TestEnsure_UsersOwnScopeLine_NeverTouched(t *testing.T) {
	// No credential, a current block, and the user's own scope line before and
	// after it: nothing of the user's moves.
	path := filepath.Join(t.TempDir(), ".npmrc")
	content := usersOwnScope + managedBlock(tokenOne) + "@hemfrid:registry=https://somewhere.else/\n"
	write(t, path, content, 0o600)
	before, _ := os.Stat(path)
	if _, err := npmrc.Ensure(context.Background(), deps(t, path, "", &bytes.Buffer{}, &probeRec{})); err == nil {
		t.Fatal("want the keyto auth tip")
	}
	ensure(t, deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{}))
	after, _ := os.Stat(path)
	if read(t, path) != content || !os.SameFile(before, after) {
		t.Fatalf("the user's own @hemfrid lines must stay, unwritten:\n%s", read(t, path))
	}
}

func TestScopeEnv(t *testing.T) {
	if got := npmrc.ScopeEnv(laneRegistry); got != "npm_config_@hemfrid:registry="+laneRegistry {
		t.Fatalf("ScopeEnv = %q", got)
	}
}
