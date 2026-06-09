package shellinit

import (
	"strings"
	"testing"
)

// The POSIX wrapper must define a keyto() function that runs the real binary
// with the integration flag, cd's into the emitted target, and passes other
// subcommands through via `command keyto`.
func TestScript_POSIX_WiresIntegration(t *testing.T) {
	for _, sh := range []string{"", "sh", "bash", "zsh", "ksh"} {
		s, err := Script(sh)
		if err != nil {
			t.Fatalf("Script(%q) error: %v", sh, err)
		}
		for _, want := range []string{
			"keyto()",
			"KEYTO_SHELL_INTEGRATION=1",
			`cd "$__keyto_target"`,
			"command keyto",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("Script(%q) missing %q:\n%s", sh, want, s)
			}
		}
	}
}

func TestScript_Fish_UsesFishSyntax(t *testing.T) {
	s, err := Script("fish")
	if err != nil {
		t.Fatalf("Script(fish) error: %v", err)
	}
	if !strings.Contains(s, "function keyto") || !strings.HasSuffix(strings.TrimSpace(s), "end") {
		t.Errorf("fish script malformed:\n%s", s)
	}
	if !strings.Contains(s, "KEYTO_SHELL_INTEGRATION=1") {
		t.Errorf("fish script missing integration flag:\n%s", s)
	}
}

func TestScript_UnknownShell_Errors(t *testing.T) {
	if _, err := Script("powershell"); err == nil {
		t.Fatal("expected error for unsupported shell, got nil")
	}
}
