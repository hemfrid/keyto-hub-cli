package credential_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/credential"
)

const (
	hubHost  = "hub.keytolabs.com"
	testCred = "tok_abc123"
)

func validCreds() *config.Creds {
	return &config.Creds{
		Credential: testCred,
		HubURL:     "https://hub.keytolabs.com",
		ExpiresAt:  time.Now().Add(1 * time.Hour),
	}
}

func gitInput(kvpairs ...string) string {
	var sb strings.Builder
	for _, kv := range kvpairs {
		sb.WriteString(kv)
		sb.WriteByte('\n')
	}
	sb.WriteByte('\n') // trailing blank line (git protocol)
	return sb.String()
}

// get + matching host + valid creds → correct output
func TestHelper_Get_MatchingHost_ValidCreds(t *testing.T) {
	in := gitInput("protocol=https", "host="+hubHost)
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "username=keyto\n") {
		t.Errorf("output missing 'username=keyto\\n'; got: %q", got)
	}
	if !strings.Contains(got, "password="+testCred+"\n") {
		t.Errorf("output missing 'password=%s\\n'; got: %q", testCred, got)
	}
	// Must end with a blank line (git protocol)
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("output does not end with blank line; got: %q", got)
	}
}

// get + different host → empty output (credential must NOT leak)
func TestHelper_Get_DifferentHost(t *testing.T) {
	in := gitInput("protocol=https", "host=other.example.com")
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	if got != "" {
		t.Errorf("expected empty output for non-hub host, got: %q", got)
	}
	if strings.Contains(got, testCred) {
		t.Errorf("credential must not appear in output for non-hub host; got: %q", got)
	}
}

// get + nil creds → empty output
func TestHelper_Get_NilCreds(t *testing.T) {
	in := gitInput("protocol=https", "host="+hubHost)
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, nil, hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.String() != "" {
		t.Errorf("expected empty output for nil creds, got: %q", out.String())
	}
}

// get + expired creds → empty output
func TestHelper_Get_ExpiredCreds(t *testing.T) {
	expired := &config.Creds{
		Credential: testCred,
		HubURL:     "https://hub.keytolabs.com",
		ExpiresAt:  time.Now().Add(-1 * time.Hour),
	}
	in := gitInput("protocol=https", "host="+hubHost)
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, expired, hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	if got != "" {
		t.Errorf("expected empty output for expired creds, got: %q", got)
	}
	if strings.Contains(got, testCred) {
		t.Errorf("credential must not appear in output for expired creds; got: %q", got)
	}
}

// store → no output, no error
func TestHelper_Store(t *testing.T) {
	in := gitInput("protocol=https", "host="+hubHost, "username=keyto", "password="+testCred)
	var out bytes.Buffer

	err := credential.Helper("store", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" {
		t.Errorf("store must produce no output, got: %q", out.String())
	}
}

// erase → no output, no error
func TestHelper_Erase(t *testing.T) {
	in := gitInput("protocol=https", "host="+hubHost, "username=keyto", "password="+testCred)
	var out bytes.Buffer

	err := credential.Helper("erase", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" {
		t.Errorf("erase must produce no output, got: %q", out.String())
	}
}

// Parsing: handles extra keys (path, protocol) and trailing blank line
func TestHelper_Get_ExtraKeys(t *testing.T) {
	// Input has extra keys that git may send
	in := "protocol=https\nhost=" + hubHost + "\npath=/some/repo.git\n\n"
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "username=keyto\n") {
		t.Errorf("expected username line; got: %q", got)
	}
	if !strings.Contains(got, "password="+testCred+"\n") {
		t.Errorf("expected password line; got: %q", got)
	}
}

// Parsing: tolerant of missing trailing newline on stdin
func TestHelper_Get_NoTrailingBlankLine(t *testing.T) {
	// Input without trailing blank line — should still work
	in := "protocol=https\nhost=" + hubHost
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "password="+testCred+"\n") {
		t.Errorf("expected password line; got: %q", got)
	}
}

// get + creds with empty Credential string → empty output (unusable)
func TestHelper_Get_EmptyCredential(t *testing.T) {
	empty := &config.Creds{
		Credential: "",
		HubURL:     "https://hub.keytolabs.com",
		ExpiresAt:  time.Now().Add(1 * time.Hour),
	}
	in := gitInput("protocol=https", "host="+hubHost)
	var out bytes.Buffer

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, empty, hubHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" {
		t.Errorf("expected empty output for empty credential, got: %q", out.String())
	}
}

// get + expired creds → friendly re-login hint on errOut, no credential on stdout.
func TestHelper_Get_ExpiredCreds_PrintsReloginHint(t *testing.T) {
	expired := &config.Creds{
		Credential: testCred,
		HubURL:     "https://hub.keytolabs.com",
		ExpiresAt:  time.Now().Add(-1 * time.Hour),
	}
	in := gitInput("protocol=https", "host="+hubHost)
	var out, errOut bytes.Buffer

	if err := credential.Helper("get", strings.NewReader(in), &out, &errOut, expired, hubHost); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" {
		t.Errorf("expired creds must not emit a credential; got stdout: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "keyto auth") {
		t.Errorf("expected a re-login hint mentioning 'keyto auth' on stderr; got: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), testCred) {
		t.Errorf("hint must not leak the credential; got: %q", errOut.String())
	}
}

// erase + matching host → friendly re-login hint (git calls erase after the Hub
// rejects a revoked/expired credential with a 401).
func TestHelper_Erase_MatchingHost_PrintsReloginHint(t *testing.T) {
	in := gitInput("protocol=https", "host="+hubHost)
	var out, errOut bytes.Buffer

	if err := credential.Helper("erase", strings.NewReader(in), &out, &errOut, validCreds(), hubHost); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" {
		t.Errorf("erase must produce no stdout; got: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "keyto auth") {
		t.Errorf("expected a re-login hint on stderr; got: %q", errOut.String())
	}
}

// erase + non-hub host → silent (no hint for unrelated hosts).
func TestHelper_Erase_DifferentHost_Silent(t *testing.T) {
	in := gitInput("protocol=https", "host=other.example.com")
	var out, errOut bytes.Buffer

	if err := credential.Helper("erase", strings.NewReader(in), &out, &errOut, validCreds(), hubHost); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" || errOut.String() != "" {
		t.Errorf("erase for a non-hub host must be silent; stdout=%q stderr=%q", out.String(), errOut.String())
	}
}
