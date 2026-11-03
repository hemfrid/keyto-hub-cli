package envsync

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// wrangler reads .dev.vars with dotenv.parse from dotenv 16.3.1 (bundled in
// wrangler 4.149.0; no dotenv-expand on this file). Its value grammar is not
// the .env one quoteEnvValue writes:
//
//   - unquoted: up to the first #, then trimmed of JavaScript whitespace;
//   - '…' and `…`: kept literally, line breaks included; nothing is unescaped;
//   - "…": only \n and \r are turned into line breaks; \\, \" and \t stay as
//     written;
//   - every \r\n and \r in the file becomes \n before parsing, so only "…"
//     with \r can carry a carriage return;
//   - a backslash before the closing quote reads as an escaped quote, and the
//     value then runs on into the following lines.
//
// quoteDevVarsValue picks the first spelling that reads back exactly. The
// tests check every choice against a port of that parser
// (dotenv_test.go), itself checked against the real package's output.

const (
	jsLineBreaks = "\r\n\u2028\u2029" // JavaScript's ^ and $ (m flag) break lines at all four
	quoteChars   = "'\"`"
)

// quoteDevVarsValue returns v spelled so wrangler reads exactly v. The error
// says why there is no such spelling and never contains v.
func quoteDevVarsValue(v string) (string, error) {
	if containsOverridesMarker(v) {
		// On the next sync the marker would be found inside the value and
		// the rest of the value taken for local overrides.
		return "", errors.New("it contains the line \"" + localOverridesMarker + "\"")
	}
	breaks := strings.ContainsAny(v, jsLineBreaks)
	switch {
	case devVarsBare(v):
		return v, nil
	case !breaks && literalQuoteKeeps(v, '\''):
		return "'" + v + "'", nil
	case !breaks && literalQuoteKeeps(v, '`'):
		return "`" + v + "`", nil
	case doubleQuoteKeeps(v):
		return `"` + strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(v) + `"`, nil
	case literalQuoteKeeps(v, '\''):
		return "'" + v + "'", nil // keeps its line breaks as real ones
	case literalQuoteKeeps(v, '`'):
		return "`" + v + "`", nil
	}
	switch {
	case strings.HasSuffix(v, `\`):
		return "", errors.New("it ends with a backslash and needs quotes")
	case strings.ContainsRune(v, '\r'):
		return "", errors.New(`it has a carriage return as well as a " or a literal \n or \r`)
	default:
		return "", errors.New("it contains both ' and `, and also \" or a literal \\n or \\r")
	}
}

// devVarsBare: the unquoted form reads back as v when v is not empty, has no
// # (a comment starts there), no quote character (dotenv would try a quoted
// form first, or strip a matching pair), no line break, and no whitespace at
// either end (it is trimmed). An empty value gets two single quotes instead:
// a bare "KEY=" lets dotenv take a quoted value from the next line.
func devVarsBare(v string) bool {
	if v == "" || strings.ContainsAny(v, "#"+quoteChars+jsLineBreaks) {
		return false
	}
	first, _ := utf8.DecodeRuneInString(v)
	last, _ := utf8.DecodeLastRuneInString(v)
	return !isJSSpace(first) && !isJSSpace(last)
}

// literalQuoteKeeps: '…' or `…` reads back as v when v has no such quote, no
// carriage return (it becomes \n) and no trailing backslash (it would escape
// the closing quote).
func literalQuoteKeeps(v string, q rune) bool {
	return !strings.ContainsRune(v, q) && !strings.ContainsRune(v, '\r') && !strings.HasSuffix(v, `\`)
}

// doubleQuoteKeeps: "…" with line breaks written as \n and \r reads back as v
// when v has no ", no trailing backslash, and no literal \n or \r of its own
// (dotenv would turn those into line breaks too).
func doubleQuoteKeeps(v string) bool {
	return !strings.ContainsRune(v, '"') && !strings.HasSuffix(v, `\`) &&
		!strings.Contains(v, `\n`) && !strings.Contains(v, `\r`)
}

// isJSSpace is JavaScript's \s and String.prototype.trim set.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// isOverridesMarker reports whether a line is the local overrides marker, as
// localOverrides finds it.
func isOverridesMarker(line string) bool {
	return strings.EqualFold(strings.TrimFunc(line, unicode.IsSpace), localOverridesMarker)
}

func containsOverridesMarker(v string) bool {
	lines, _ := splitDotenvLines(v)
	for _, line := range lines {
		if isOverridesMarker(line) {
			return true
		}
	}
	return false
}
