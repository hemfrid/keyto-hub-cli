package termsafe

import (
	"strings"
	"testing"
)

func TestSanitize_PlainMode(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text is unchanged", "all good: 3 pods ready", "all good: 3 pods ready"},
		{"tab is kept", "a\tb", "a\tb"},
		{"zero-width joiner is kept", "a‍b", "a‍b"},
		{"newline becomes a space", "line one\nline two", "line one line two"},
		{"carriage return is dropped", "real\rfake", "realfake"},
		{"unicode line and paragraph separators become spaces", "a b c", "a b c"},
		{"OSC 52 clipboard write, BEL-terminated", "x\x1b]52;c;cGF5bG9hZA==\x07y", "xy"},
		{"OSC 52 clipboard write, ST-terminated", "x\x1b]52;c;cGF5bG9hZA==\x1b\\y", "xy"},
		{"OSC 8 hyperlink keeps only the visible text", "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07", "click"},
		{"OSC 0 window title", "\x1b]0;pwned\x07ok", "ok"},
		{"unterminated OSC swallows to the end", "ok\x1b]0;pwned", "ok"},
		{"CSI erase display and cursor home", "a\x1b[2J\x1b[Hb", "ab"},
		{"CSI with private parameters", "a\x1b[?25lb", "ab"},
		{"SGR colour is dropped without keepColour", "\x1b[31mred\x1b[0m", "red"},
		{"interrupted CSI is dropped", "a\x1b[12", "a"},
		{"8-bit CSI (C1 0x9b)", "a\u009b2Jb", "ab"},
		{"8-bit OSC (C1 0x9d) up to C1 ST", "a\u009d0;pwned\u009cb", "ab"},
		{"raw C1 byte 0x9b in invalid UTF-8", "a\x9b2Jb", "ab"},
		{"DCS, SOS, PM and APC strings", "a\x1bPq\x1b\\b\x1bXs\x1b\\c\x1b^p\x1b\\d\x1b_a\x1b\\e", "abcde"},
		{"two-byte escape (ESC c, full reset)", "a\x1bcb", "ab"},
		{"nF escape (charset select)", "a\x1b(0b", "ab"},
		{"lone ESC at the end", "a\x1b", "a"},
		{"bidi override U+202E", "abc‮def", "abcdef"},
		{"bidi embeddings and overrides U+202A to U+202D", "a\u202ab\u202bc\u202cd\u202de", "abcde"},
		{"left-to-right and right-to-left marks U+200E, U+200F", "a\u200eb\u200fc", "abc"},
		{"an RLO-wrapped file name reads in its stored order", "invoice_\u202egnp.exe", "invoice_gnp.exe"},
		{"bidi isolates U+2066 to U+2069", "a⁦b⁧c⁨d⁩e", "abcde"},
		{"zero-width space", "sk-live-12​3456", "sk-live-123456"},
		{"byte order mark", "\xef\xbb\xbfhello", "hello"},
		{"soft hyphen and word joiner", "a­b⁠c", "abc"},
		{"tag characters", "a\U000e0041b", "ab"},
		{"C0 controls: NUL, BEL, backspace, DEL", "a\x00b\x07c\x08d\x7fe", "abcde"},
		{"other invalid UTF-8 bytes are dropped", "a\xffb", "ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitize(c.in, false); got != c.want {
				t.Errorf("Sanitize(%q, false) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitize_KeepColour_AllowsOnlyListedSGR(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"a colour is kept and a reset is appended", "\x1b[31mred", "\x1b[31mred\x1b[0m"},
		{"an existing trailing reset is not doubled", "\x1b[1;32mok\x1b[0m", "\x1b[1;32mok\x1b[0m"},
		{"empty parameters mean reset", "\x1b[mx", "\x1b[mx\x1b[0m"},
		{"256-colour foreground", "\x1b[38;5;200mx", "\x1b[38;5;200mx\x1b[0m"},
		{"truecolour background", "\x1b[48;2;1;2;3mx", "\x1b[48;2;1;2;3mx\x1b[0m"},
		{"conceal (8) is refused", "\x1b[8mhidden", "hidden"},
		{"reveal (28) is refused", "\x1b[28mx", "x"},
		{"one bad parameter refuses the whole sequence", "\x1b[31;8mx", "x"},
		{"256-colour index out of range", "\x1b[38;5;256mx", "x"},
		{"38 without its arguments", "\x1b[38mx", "x"},
		{"truecolour with a missing component", "\x1b[38;2;1;2mx", "x"},
		{"an alternative font (10) is refused", "\x1b[10mx", "x"},
		{"a 4-digit parameter is refused", "\x1b[0031mx", "x"},
		{"colon sub-parameters are refused", "\x1b[38:5:200mx", "x"},
		{"an 8-bit CSI SGR is never kept", "\u009b31mx", "x"},
		{"non-SGR CSI is still dropped", "\x1b[2Jx", "x"},
		{"OSC is still dropped", "\x1b]52;c;AAAA\x07x", "x"},
		{"newlines still become spaces", "\x1b[31ma\nb", "\x1b[31ma b\x1b[0m"},
		{"bidi embeddings, overrides and marks are dropped with colour kept", "\x1b[32ma\u202a\u202b\u202c\u202d\u200e\u200fb", "\x1b[32mab\x1b[0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitize(c.in, true); got != c.want {
				t.Errorf("Sanitize(%q, true) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitize_ResultIsAlwaysOneLineWithoutEscapes(t *testing.T) {
	in := "a\nb\rc\x1b[2Jd\x1b]0;t\x07e f‮g\x9bHh"
	got := Sanitize(in, false)
	if strings.ContainsAny(got, "\n\r\x1b\x07 ‮\u009b") {
		t.Errorf("Sanitize left a control character: %q", got)
	}
}

func TestStatusLine_IsBuiltFromTheCodeAlone(t *testing.T) {
	cases := map[int]string{
		401: "401 Unauthorized",
		409: "409 Conflict",
		503: "503 Service Unavailable",
		499: "499", // Go has no text for it: the number alone
	}
	for code, want := range cases {
		if got := StatusLine(code); got != want {
			t.Errorf("StatusLine(%d) = %q, want %q", code, got, want)
		}
	}
}
