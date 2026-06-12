package ui

import (
	"os"
	"strconv"

	"golang.org/x/term"
)

// IsStdoutTTY reports whether stdout is an interactive terminal.
func IsStdoutTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// IsStderrTTY reports whether stderr is an interactive terminal. The `start`
// banner writes to stderr so stdout stays clean for shell integration (which
// captures the resolved project directory from stdout).
func IsStderrTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// IsStdinTTY reports whether stdin is an interactive terminal. Used to gate
// y/N consent prompts: a non-interactive stdin means there is no human to
// answer, so the prompt must decline rather than block.
func IsStdinTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// TermWidth returns the terminal width, trying stdout then stderr then the
// COLUMNS env var, and falling back to a large default (assume wide) when
// unknown. Checking stderr matters under shell integration, where stdout is a
// captured pipe and only stderr is attached to the real terminal.
func TermWidth() int {
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
	}
	if c := os.Getenv("COLUMNS"); c != "" {
		if w, err := strconv.Atoi(c); err == nil && w > 0 {
			return w
		}
	}
	return 1 << 30 // unknown -> assume wide
}
