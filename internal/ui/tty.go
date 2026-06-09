package ui

import (
	"os"

	"golang.org/x/term"
)

// IsStdoutTTY reports whether stdout is an interactive terminal.
func IsStdoutTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// TermWidth returns the terminal width, or a large default when unknown.
func TermWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 1 << 30 // unknown -> assume wide
	}
	return w
}
