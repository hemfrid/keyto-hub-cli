package envsync_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
)

// wrangler reads .dev.vars with dotenv 16.3.1 (dotenvParse, dotenv_test.go).
// Every value env sync writes there must come back from that parser exactly
// as the Hub sent it.

// trickyDevVarsValues are Hub values that the .env quoting used to change on
// the way to wrangler, plus the rest of dotenv's edge cases.
var trickyDevVarsValues = map[string]string{
	"JSON_KEY":       `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----\n","client_email":"x@y.iam"}`,
	"WIN_PATH":       `C:\new\temp`,
	"TAB":            "a\tb",
	"NEWLINES":       "line one\nline two\n",
	"CRLF":           "one\r\ntwo",
	"CR_ONLY":        "one\rtwo",
	"HASH":           "abc#def # not a comment",
	"EQUALS":         "a=b==c",
	"SPACES":         "  padded  ",
	"UNICODE":        "naïve café 日本 🎉",
	"UNICODE_SPACES": "\u00a0x\u3000",
	"LINE_SEP":       "x\u2028y\u2029z",
	"EMPTY":          "",
	"SINGLE_QUOTE":   "it's",
	"DOUBLE_QUOTE":   `say "hi"`,
	"BACKTICK":       "run `ls`",
	"TWO_QUOTES":     `it's "quoted"`,
	"LITERAL_BS_N":   `literal \n and \r stay`,
	"TRAILING_BS":    `ends with \`,
	"BS_NEWLINE":     "a\\\nb",
	"QUOTED_START":   `'starts with a quote`,
	"WRAPPED":        `"wrapped"`,
	"ESCAPED_QUOTES": `\"x\"`,
	"INJECTION":      "x\nKEYTO_DEV_USER=0\nEVIL=1",
	"MARKERISH":      "# local overrides below, but not the line",
	"PRETTY_JSON":    "{\n  \"a\": \"b\\nc\"\n}",
}

// unwritableDevVarsValues have no dotenv spelling that round-trips: each
// needs ', ` and " (or a literal \n) at once, ends in a backslash next to
// characters only quotes can hold, or carries the overrides marker line.
var unwritableDevVarsValues = map[string]string{
	"ALL_QUOTES":   "it's \"x\" `y`",
	"BS_N_QUOTES":  "it's `y` \\n",
	"CR_AND_QUOTE": "a\"\rb",
	"HASH_BS":      `a # b\`,
	"MARKER_LINE":  "x\n# local overrides below\nEVIL=1",
}

func environmentsFor(keys []string) string {
	var sb strings.Builder
	sb.WriteString("schemaVersion: 1\nsecrets:\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "  - key: %s\n    category: operator-required\n", k)
	}
	return sb.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestWorkers_DevVarsValuesRoundTripThroughWranglersDotenv(t *testing.T) {
	keys := sortedKeys(trickyDevVarsValues)
	dir := makeWorkersDir(t, environmentsFor(keys))
	d := baseDeps(t, dir)
	d.Fetch = staticFetch(trickyDevVarsValues, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	path := filepath.Join(dir, ".dev.vars")

	// Add a local overrides section: it has to parse after the values too.
	first := readFile(t, path) + "\n# local overrides below\nMINE=1\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	content := readFile(t, path)
	if content != first {
		t.Fatalf("a re-sync changed the file:\n--- before\n%s\n--- after\n%s", first, content)
	}

	want := map[string]string{"KEYTO_DEV_USER": "1", "MINE": "1"}
	for k, v := range trickyDevVarsValues {
		want[k] = v
	}
	if got := dotenvParse(content); !reflect.DeepEqual(got, want) {
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("%s: wrangler reads %q, the Hub sent %q", k, got[k], want[k])
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("wrangler reads an extra key %s=%q", k, got[k])
			}
		}
		t.Fatalf("file:\n%s", content)
	}
}

func TestWorkers_UnwritableDevVarsValueIsLeftOutByName(t *testing.T) {
	values := map[string]string{"PLAIN": "ok"}
	for k, v := range unwritableDevVarsValues {
		values[k] = v
	}
	keys := sortedKeys(values)
	dir := makeWorkersDir(t, environmentsFor(keys))
	out := &bytes.Buffer{}
	d := baseDeps(t, dir)
	d.Out = out
	d.Fetch = staticFetch(values, nil)
	if err := envsync.Run(context.Background(), nil, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := readFile(t, filepath.Join(dir, ".dev.vars"))
	got := dotenvParse(content)
	if want := map[string]string{"PLAIN": "ok", "KEYTO_DEV_USER": "1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wrangler reads %q, want only %q:\n%s", got, want, content)
	}
	for k, v := range unwritableDevVarsValues {
		if !strings.Contains(out.String(), k) {
			t.Errorf("the output should name %s, got:\n%s", k, out.String())
		}
		if !strings.Contains(content, "# NOT WRITTEN: "+k+" ") {
			t.Errorf(".dev.vars should say %s was left out:\n%s", k, content)
		}
		if strings.Contains(out.String(), v) || strings.Contains(content, v) {
			t.Errorf("the value of %s leaked into the output or the file", k)
		}
	}
}

func FuzzQuoteDevVarsValue_RoundTripsThroughDotenv(f *testing.F) {
	for _, v := range trickyDevVarsValues {
		f.Add(v)
	}
	for _, v := range unwritableDevVarsValues {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if !utf8.ValidString(v) {
			t.Skip("Hub values arrive as JSON strings, always UTF-8")
		}
		quoted, err := envsync.QuoteDevVarsValue(v)
		if err != nil {
			if strings.Contains(err.Error(), v) && v != "" {
				t.Fatalf("the refusal quotes the value: %v", err)
			}
			return
		}
		src := "# header \"x\" 'y'\nBEFORE=1\n\nK=" + quoted + "\n# after\nAFTER='x'\nKEYTO_DEV_USER=1\n"
		want := map[string]string{"BEFORE": "1", "K": v, "AFTER": "x", "KEYTO_DEV_USER": "1"}
		if got := dotenvParse(src); !reflect.DeepEqual(got, want) {
			t.Fatalf("value %q written as %q reads back as %q", v, quoted, got)
		}
	})
}
