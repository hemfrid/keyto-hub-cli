package envsync_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// dotenvParse is a port of parse() in dotenv 16.3.1 (lib/main.js). wrangler
// 4.149.0, the version the Workers template pins, bundles that exact copy
// (wrangler-dist/cli.js, "node_modules/.pnpm/dotenv@16.3.1") and reads
// .dev.vars with it: tryLoadDotDevDotVars calls dotenv.parse and nothing
// else (dotenv-expand only runs on the .env fallback, never on .dev.vars).
// The JavaScript it ports:
//
//	const LINE = /(?:^|^)\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)(\s*'(?:\\'|[^'])*'|\s*"(?:\\"|[^"])*"|\s*`(?:\\`|[^`])*`|[^#\r\n]+)?\s*(?:#.*)?(?:$|$)/mg
//	let lines = src.toString().replace(/\r\n?/mg, '\n')
//	while ((match = LINE.exec(lines)) != null) {
//	  const key = match[1]
//	  let value = (match[2] || '').trim()
//	  const maybeQuote = value[0]
//	  value = value.replace(/^(['"`])([\s\S]*)\1$/mg, '$2')
//	  if (maybeQuote === '"') {
//	    value = value.replace(/\\n/g, '\n')
//	    value = value.replace(/\\r/g, '\r')
//	  }
//	  obj[key] = value
//	}
//
// How the JavaScript regex is carried over to Go's RE2, which picks the same
// match a backtracking engine would (leftmost-first):
//   - \s is JavaScript's whitespace set (jsSpace, Unicode spaces included),
//     and . excludes all four JavaScript line breaks.
//   - With the m flag, ^ matches at the start and after \n, \r, U+2028 and
//     U+2029. Go's ^ only knows \n, so the loop below tries a match only at
//     those positions, anchored with \A.
//   - $ (m flag) is "end of input or a line break next". RE2 has no
//     lookahead, so the Go pattern consumes that line break instead; the next
//     search starts right after it, which is the first place JavaScript's ^
//     would accept anyway.
//   - The unquote step uses a backreference (\1), which RE2 lacks, so
//     jsUnquote does it by hand.
//
// testdata/dotenv-16.3.1-golden.json holds the real dotenv 16.3.1's output
// for a corpus of files (regenerate with testdata/gen-dotenv-golden.mjs);
// TestDotenvPort_MatchesDotenv1631 keeps this port faithful to it.
func dotenvParse(src string) map[string]string {
	src = crlfRe.ReplaceAllString(src, "\n")
	obj := map[string]string{}
	for p := 0; p < len(src); {
		if !jsLineStart(src, p) {
			p++
			continue
		}
		m := dotenvLineRe.FindStringSubmatchIndex(src[p:])
		if m == nil {
			p++
			continue
		}
		key := src[p+m[2] : p+m[3]]
		value := ""
		if m[4] >= 0 {
			value = src[p+m[4] : p+m[5]]
		}
		value = strings.TrimFunc(value, isJSSpace)
		maybeQuote := byte(0)
		if value != "" {
			maybeQuote = value[0]
		}
		value = jsUnquote(value)
		if maybeQuote == '"' {
			value = strings.ReplaceAll(value, `\n`, "\n")
			value = strings.ReplaceAll(value, `\r`, "\r")
		}
		obj[key] = value
		p += m[1]
	}
	return obj
}

const (
	jsDot       = `[^\n\r\x{2028}\x{2029}]`
	jsLineBreak = `[\n\r\x{2028}\x{2029}]`
)

var (
	crlfRe       = regexp.MustCompile(`\r\n?`)
	dotenvLineRe = regexp.MustCompile(`\A` + jsSpace + `*(?:export` + jsSpace + `+)?([\w.-]+)(?:` + jsSpace + `*=` + jsSpace + `*?|:` + jsSpace + `+?)(` +
		jsSpace + `*'(?:\\'|[^'])*'|` +
		jsSpace + `*"(?:\\"|[^"])*"|` +
		jsSpace + "*`(?:\\\\`|[^`])*`|" +
		`[^#\r\n]+)?` + jsSpace + `*(?:#` + jsDot + `*)?(?:` + jsLineBreak + `|\z)`)
	jsSpaceRe = regexp.MustCompile(`\A` + jsSpace + `\z`)
)

func isJSSpace(r rune) bool { return jsSpaceRe.MatchString(string(r)) }

// jsLineStart reports whether ^ (m flag) matches at byte offset p.
func jsLineStart(s string, p int) bool {
	if p == 0 {
		return true
	}
	if c := s[p-1]; c == '\n' || c == '\r' {
		return true
	}
	return strings.HasSuffix(s[:p], "\u2028") || strings.HasSuffix(s[:p], "\u2029")
}

// jsLineBreakAt reports whether a JavaScript line break starts at offset p.
func jsLineBreakAt(s string, p int) bool {
	if p >= len(s) {
		return false
	}
	if c := s[p]; c == '\n' || c == '\r' {
		return true
	}
	return strings.HasPrefix(s[p:], "\u2028") || strings.HasPrefix(s[p:], "\u2029")
}

// jsUnquote is value.replace(/^(['"`])([\s\S]*)\1$/mg, '$2'): at each line
// start holding a quote character, the greedy [\s\S]* runs to the LAST
// matching quote that $ accepts (end of input or a line break next).
func jsUnquote(v string) string {
	var sb strings.Builder
	last := 0
	for p := 0; p < len(v); {
		if jsLineStart(v, p) && strings.IndexByte("'\"`", v[p]) >= 0 {
			end := -1
			for j := len(v) - 1; j > p; j-- {
				if v[j] == v[p] && (j+1 == len(v) || jsLineBreakAt(v, j+1)) {
					end = j
					break
				}
			}
			if end >= 0 {
				sb.WriteString(v[last:p])
				sb.WriteString(v[p+1 : end])
				last, p = end+1, end+1
				continue
			}
		}
		p++
	}
	sb.WriteString(v[last:])
	return sb.String()
}

type dotenvGolden struct {
	Name   string            `json:"name"`
	Src    string            `json:"src"`
	Parsed map[string]string `json:"parsed"`
}

func TestDotenvPort_MatchesDotenv1631(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "dotenv-16.3.1-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []dotenvGolden
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 20 {
		t.Fatalf("golden corpus has %d cases; expected the full corpus", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if !utf8.ValidString(tc.Src) {
				t.Fatalf("corpus source is not UTF-8")
			}
			if got := dotenvParse(tc.Src); !reflect.DeepEqual(got, tc.Parsed) {
				t.Fatalf("port disagrees with dotenv 16.3.1\nsrc:  %q\ngot:  %q\nwant: %q", tc.Src, got, tc.Parsed)
			}
		})
	}
}
