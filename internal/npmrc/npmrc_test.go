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

// managedBlock is what keyto writes for hubURL and token.
func managedBlock(token string) string {
	return npmrc.BeginMarker + "\n" +
		"@hemfrid:registry=https://hub.example.com/api/npm/\n" +
		"//hub.example.com/api/npm/:_authToken=" + token + "\n" +
		npmrc.EndMarker + "\n"
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
	if err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, out, probe)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := read(t, path); got != managedBlock(tokenOne) {
		t.Fatalf("~/.npmrc =\n%s\nwant\n%s", got, managedBlock(tokenOne))
	}
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
		"trailing newline": "registry=https://registry.npmjs.org/\n@acme:registry=https://npm.acme.dev/\n//npm.acme.dev/:_authToken=acme-secret\n",
		"no final newline": "@acme:registry=https://npm.acme.dev/\n; a comment\n//npm.acme.dev/:_authToken=acme-secret",
		"crlf":             "@acme:registry=https://npm.acme.dev/\r\nsave-exact=true\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".npmrc")
			write(t, path, existing, 0o644)
			out := &bytes.Buffer{}
			if err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, out, &probeRec{})); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
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
	if err := npmrc.Ensure(context.Background(), deps(t, path, tokenTwo, out, probe)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
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
	if err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, out, probe)); err != nil {
		t.Fatalf("Ensure: %v", err)
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
	err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, out, probe))
	if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), path) {
		t.Fatalf("want a symlink refusal naming %s, got %v", path, err)
	}
	// The tip says what to add by hand, without the token itself.
	if !strings.Contains(err.Error(), "@hemfrid:registry=https://hub.example.com/api/npm/") {
		t.Fatalf("the refusal should give the lines to add by hand, got %v", err)
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
	err := npmrc.Ensure(context.Background(), deps(t, path, "", &bytes.Buffer{}, &probeRec{}))
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
	err := npmrc.Ensure(context.Background(), d)
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
	if err := npmrc.Ensure(context.Background(), d); err != nil {
		t.Fatalf("a Hub without the lane is not an error, got %v", err)
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
	err := npmrc.Ensure(context.Background(), deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{}))
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
	if err := npmrc.Ensure(context.Background(), deps(t, path, tokenTwo, &bytes.Buffer{}, &probeRec{})); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := read(t, path); got != managedBlock(tokenTwo)+"a=1\n" {
		t.Fatalf("~/.npmrc =\n%s\nwant one block, at the first one's place", got)
	}
}

func TestEnsure_LaterLineOverridingTheLane_Noted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	write(t, path, managedBlock(tokenOne)+"@hemfrid:registry = https://npm.pkg.github.com/\n//hub.example.com/api/npm/:_authToken=handwritten\n", 0o600)
	out := &bytes.Buffer{}
	if err := npmrc.Ensure(context.Background(), deps(t, path, tokenTwo, out, &probeRec{})); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.Contains(out.String(), "5, 6") {
		t.Fatalf("the note should name lines 5 and 6, got %q", out.String())
	}
	if strings.Contains(out.String(), "handwritten") || strings.Contains(out.String(), "npm.pkg.github.com") {
		t.Fatalf("the note must not print the lines themselves, got %q", out.String())
	}
}

func TestEnsure_HubOriginAndPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".npmrc")
	d := deps(t, path, tokenOne, &bytes.Buffer{}, &probeRec{})
	d.Creds.HubURL = "http://localhost:3000/"
	if err := npmrc.Ensure(context.Background(), d); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := read(t, path)
	for _, want := range []string{
		"\n@hemfrid:registry=http://localhost:3000/api/npm/\n",
		"\n//localhost:3000/api/npm/:_authToken=" + tokenOne + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("~/.npmrc lacks %q:\n%s", want, got)
		}
	}
}

func TestEnsure_TokenThatCouldInjectLines_Refused(t *testing.T) {
	for _, tok := range []string{"a\n@evil:registry=https://evil/", "a b", "a\rb", "a\tb"} {
		path := filepath.Join(t.TempDir(), ".npmrc")
		d := deps(t, path, tok, &bytes.Buffer{}, &probeRec{})
		if err := npmrc.Ensure(context.Background(), d); err == nil {
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
