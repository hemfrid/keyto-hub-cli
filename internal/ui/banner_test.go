package ui

import (
	"bytes"
	"strings"
	"testing"
)

// stableArtSubstring is a run of % chars from the KEYTO logo portion of the
// wide banner art. The rocket above it uses braille glyphs (no %), and the
// compact one-liner has no %, so this never collides with either.
const stableArtSubstring = "%%%%%%%%%%"

func TestBanner_TTY_WideTerminal_WritesFullArt(t *testing.T) {
	var buf bytes.Buffer
	Banner(&buf, true, 120, false, "v0.1.0")
	out := buf.String()

	if !strings.Contains(out, stableArtSubstring) {
		t.Errorf("expected full art (containing %q) but got:\n%s", stableArtSubstring, out)
	}
	if !strings.Contains(out, "keyto-cli v0.1.0") {
		t.Errorf("expected version string 'keyto-cli v0.1.0' in output but got:\n%s", out)
	}
}

func TestBanner_NonTTY_WritesNothing(t *testing.T) {
	var buf bytes.Buffer
	Banner(&buf, false, 120, false, "v0.1.0")
	if buf.Len() != 0 {
		t.Errorf("expected no output for non-TTY, got %q", buf.String())
	}
}

func TestBanner_NarrowTerminal_WritesCompactOneliner(t *testing.T) {
	var buf bytes.Buffer
	Banner(&buf, true, 40, false, "v0.1.0")
	out := buf.String()

	if !strings.Contains(out, "KEYTO") {
		t.Errorf("expected compact one-liner to contain 'KEYTO', got %q", out)
	}
	if !strings.Contains(out, "v0.1.0") {
		t.Errorf("expected compact one-liner to contain 'v0.1.0', got %q", out)
	}
	// Must NOT contain the full art
	if strings.Contains(out, stableArtSubstring) {
		t.Errorf("expected compact output (NOT full art), but found %q in:\n%s", stableArtSubstring, out)
	}
}

func TestBanner_Quiet_WritesNothing(t *testing.T) {
	var buf bytes.Buffer
	Banner(&buf, true, 120, true, "v0.1.0")
	if buf.Len() != 0 {
		t.Errorf("expected no output when quiet=true, got %q", buf.String())
	}
}
