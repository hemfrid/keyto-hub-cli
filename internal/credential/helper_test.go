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
	hubURL   = "https://hub.keytolabs.com"
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, nil, hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, expired, hubURL)
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

	err := credential.Helper("store", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("erase", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, validCreds(), hubURL)
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

	err := credential.Helper("get", strings.NewReader(in), &out, io.Discard, empty, hubURL)
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

	if err := credential.Helper("get", strings.NewReader(in), &out, &errOut, expired, hubURL); err != nil {
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

	if err := credential.Helper("erase", strings.NewReader(in), &out, &errOut, validCreds(), hubURL); err != nil {
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

	if err := credential.Helper("erase", strings.NewReader(in), &out, &errOut, validCreds(), hubURL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "" || errOut.String() != "" {
		t.Errorf("erase for a non-hub host must be silent; stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

// The credential goes only to the signed-in Hub's own origin: the request's
// protocol must be the Hub URL's scheme and its host (with any port) the Hub
// URL's host. A request for the right host over http would send the token in
// cleartext, so it gets nothing.
func TestHelper_AnswersOnlyTheHubsOrigin(t *testing.T) {
	cases := []struct {
		name, hub string
		attrs     []string
		answers   bool
	}{
		{"https, the Hub host", hubURL, []string{"protocol=https", "host=hub.keytolabs.com"}, true},
		{"the host in another case", hubURL, []string{"protocol=https", "host=HUB.keytolabs.com"}, true},
		{"http, the Hub host (cleartext)", hubURL, []string{"protocol=http", "host=hub.keytolabs.com"}, false},
		{"no protocol", hubURL, []string{"host=hub.keytolabs.com"}, false},
		{"another protocol", hubURL, []string{"protocol=ssh", "host=hub.keytolabs.com"}, false},
		{"the Hub host with a port the Hub URL lacks", hubURL, []string{"protocol=https", "host=hub.keytolabs.com:8443"}, false},
		{"a Hub on a port: same port", "https://hub.keytolabs.com:8443", []string{"protocol=https", "host=hub.keytolabs.com:8443"}, true},
		{"a Hub on a port: no port", "https://hub.keytolabs.com:8443", []string{"protocol=https", "host=hub.keytolabs.com"}, false},
		{"a Hub on a port: another port", "https://hub.keytolabs.com:8443", []string{"protocol=https", "host=hub.keytolabs.com:9443"}, false},
		{"an http Hub (local dev): http", "http://127.0.0.1:3000", []string{"protocol=http", "host=127.0.0.1:3000"}, true},
		{"an http Hub: https is another origin", "http://127.0.0.1:3000", []string{"protocol=https", "host=127.0.0.1:3000"}, false},
		{"not signed in", "", []string{"protocol=https", "host=hub.keytolabs.com"}, false},
		{"an unparseable Hub URL", "://bad", []string{"protocol=https", "host=hub.keytolabs.com"}, false},
		{"http on the https default port", hubURL, []string{"protocol=http", "host=hub.keytolabs.com:443"}, false},
		{"https with the default port written out", hubURL, []string{"protocol=https", "host=hub.keytolabs.com:443"}, true},
		{"a stored Hub URL with user info", "https://alice@hub.keytolabs.com", []string{"protocol=https", "host=hub.keytolabs.com"}, false},
		{"user info in the request host", hubURL, []string{"protocol=https", "host=alice@hub.keytolabs.com"}, false},
	}
	for _, tc := range cases {
		creds := validCreds()
		creds.HubURL = tc.hub
		var out, errOut bytes.Buffer
		if err := credential.Helper("get", strings.NewReader(gitInput(tc.attrs...)), &out, &errOut, creds, tc.hub); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := strings.Contains(out.String(), testCred); got != tc.answers {
			t.Errorf("%s: credential sent = %v, want %v (out %q)", tc.name, got, tc.answers, out.String())
		}
		// erase follows the same rule: the re-login hint only for the Hub's origin.
		var eOut, eErr bytes.Buffer
		if err := credential.Helper("erase", strings.NewReader(gitInput(tc.attrs...)), &eOut, &eErr, creds, tc.hub); err != nil {
			t.Fatalf("%s erase: %v", tc.name, err)
		}
		if got := eErr.Len() > 0; got != tc.answers {
			t.Errorf("%s: erase hint shown = %v, want %v", tc.name, got, tc.answers)
		}
	}
}
