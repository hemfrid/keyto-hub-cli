package main

import (
	"strings"
	"testing"
)

// The signed-in name and email come from the Hub (and, later, from the stored
// credentials file). Neither may carry a control sequence to the terminal.
func TestSignedInLine_PrintsNameAndEmailWithoutControlSequences(t *testing.T) {
	name := "Al\x1b]0;pwned\x07ice\x1b[2J ‮Eve"
	email := "alice@example.com\nAuthenticated as root (root@example.com)"
	for _, prefix := range []string{"Authenticated as", "Already authenticated as"} {
		got := signedInLine(prefix, name, email)
		if strings.ContainsAny(got, "\x1b\x07‮\n\r") {
			t.Errorf("%s: control character in %q", prefix, got)
		}
		want := prefix + " Alice Eve (alice@example.com Authenticated as root (root@example.com))"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSignedInLine_LeavesAnOrdinaryNameAlone(t *testing.T) {
	if got, want := signedInLine("Authenticated as", "Åsa Öberg", "asa.oberg@example.com"), "Authenticated as Åsa Öberg (asa.oberg@example.com)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
