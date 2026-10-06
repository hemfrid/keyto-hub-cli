// Package termsafe neutralises terminal control sequences in untrusted text.
package termsafe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize strips terminal control sequences from server-provided
// text so it cannot write the clipboard (OSC 52), spoof links (OSC 8), retitle
// the window, or move/erase the cursor to hide or fake output. With keepColour
// only SGR (`ESC [ digits;digits m`) survives; without it, plain text remains.
// The result is always a single line: newlines and Unicode line separators
// become a space (so text cannot forge extra output lines), tab is kept, and
// every other C0/C1 control (incl. CR, DEL) and invisible/reordering format
// character (bidi overrides, zero-widths, BOM, tags; ZWJ is kept) is dropped.
// SGR is allow-listed (see validSGR); if any was kept, a reset is appended so
// styles cannot bleed into the CLI's own output.
func Sanitize(s string, keepColour bool) string {
	rs := decodeKeepingC1(s)
	var b strings.Builder
	b.Grow(len(s))
	return sanitizeRunes(rs, keepColour, &b)
}

const sgrReset = "\x1b[0m"

func sanitizeRunes(rs []rune, keepColour bool, b *strings.Builder) string {
	keptSGR := false
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == 0x1b:
			i = skipEscape(rs, i, keepColour, b, &keptSGR)
		case r == 0x9b: // 8-bit CSI
			i = skipCSI(rs, i+1, false, b, i, &keptSGR)
		case r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f:
			i = skipString(rs, i+1)
		case r == '\t':
			b.WriteRune(r)
			i++
		case r == '\n' || r == 0x2028 || r == 0x2029:
			b.WriteByte(' ')
			i++
		case r != 0x200d && unicode.Is(unicode.Cf, r):
			i++
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			i++
		default:
			b.WriteRune(r)
			i++
		}
	}
	out := b.String()
	if keptSGR && !strings.HasSuffix(out, sgrReset) {
		out += sgrReset
	}
	return out
}

// decodeKeepingC1 decodes UTF-8, mapping stray C1 bytes (0x80-0x9f) to their
// code points (a terminal may honour them as 8-bit controls) and dropping
// other invalid bytes.
func decodeKeepingC1(s string) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			if c := rune(s[i]); c >= 0x80 && c <= 0x9f {
				out = append(out, c)
			}
		} else {
			out = append(out, r)
		}
		i += n
	}
	return out
}

// skipEscape consumes the sequence starting at the ESC at rs[i] and returns the
// index after it, writing it to b only if it is a kept SGR.
func skipEscape(rs []rune, i int, keepColour bool, b *strings.Builder, kept *bool) int {
	if i+1 >= len(rs) {
		return i + 1
	}
	switch c := rs[i+1]; {
	case c == '[':
		return skipCSI(rs, i+2, keepColour, b, i, kept)
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(rs, i+2)
	case c >= 0x20 && c <= 0x2f: // nF: ESC <intermediates> <final>
		j := i + 1
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
			j++
		}
		if j < len(rs) && rs[j] >= 0x30 && rs[j] <= 0x7e {
			j++
		}
		return j
	case c >= 0x30 && c <= 0x7e: // two-byte sequence
		return i + 2
	}
	return i + 1 // lone ESC; the next char is handled on its own
}

// skipCSI consumes a CSI body from rs[j]; start is where the introducer began.
func skipCSI(rs []rune, j int, keepColour bool, b *strings.Builder, start int, kept *bool) int {
	paramsOnly := true
	k := j
	for k < len(rs) && rs[k] >= 0x30 && rs[k] <= 0x3f {
		if !(rs[k] >= '0' && rs[k] <= '9') && rs[k] != ';' {
			paramsOnly = false
		}
		k++
	}
	for k < len(rs) && rs[k] >= 0x20 && rs[k] <= 0x2f {
		paramsOnly = false
		k++
	}
	if k >= len(rs) || rs[k] < 0x40 || rs[k] > 0x7e {
		return k // malformed/interrupted: drop what we consumed
	}
	if keepColour && paramsOnly && rs[k] == 'm' && rs[start] == 0x1b && validSGR(string(rs[j:k])) {
		b.WriteString(string(rs[start : k+1]))
		*kept = true
	}
	return k + 1
}

// skipString consumes an OSC/DCS/SOS/PM/APC payload up to BEL, ST (ESC \ or
// C1 0x9c), or the end of input.
func skipString(rs []rune, j int) int {
	for ; j < len(rs); j++ {
		switch {
		case rs[j] == 0x07 || rs[j] == 0x9c:
			return j + 1
		case rs[j] == 0x1b:
			if j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 2
			}
			return j // a fresh escape aborts the string; reparse it
		}
	}
	return j
}

// validSGR allow-lists SGR parameters: reset, bold/dim/italic/underline/blink/
// reverse (0-7), strike (9), their off-switches (22-27, 29), 8-colour and
// bright foreground/background, default colours (39, 49), and the extended
// 38/48 forms ;5;n and ;2;r;g;b with valid arity and range. Conceal (8),
// reveal (28), fonts, and anything else make the whole sequence invalid.
func validSGR(params string) bool {
	if params == "" {
		return true
	}
	parts := strings.Split(params, ";")
	num := func(i, max int) bool {
		if i >= len(parts) || parts[i] == "" || len(parts[i]) > 3 {
			return false
		}
		n := 0
		for _, c := range parts[i] {
			n = n*10 + int(c-'0')
		}
		return n <= max
	}
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue // empty means 0
		}
		if len(parts[i]) > 3 {
			return false
		}
		n := 0
		for _, c := range parts[i] {
			n = n*10 + int(c-'0')
		}
		switch {
		case n <= 7, n == 9, n >= 22 && n <= 27, n == 29, n >= 30 && n <= 37, n == 39,
			n >= 40 && n <= 47, n == 49, n >= 90 && n <= 97, n >= 100 && n <= 107:
		case n == 38 || n == 48:
			switch {
			case i+1 < len(parts) && parts[i+1] == "5" && num(i+2, 255):
				i += 2
			case i+1 < len(parts) && parts[i+1] == "2" && num(i+2, 255) && num(i+3, 255) && num(i+4, 255):
				i += 4
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}
