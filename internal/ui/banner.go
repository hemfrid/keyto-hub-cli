package ui

import (
	_ "embed"
	"fmt"
	"io"
	"strings"
)

//go:embed banner.txt
var bannerArt string

const compact = "▌ KEYTO ▐ · local dev"
const bannerWidth = 91 // combined KEYTO logo + rocket art width in runes

// Banner writes the startup banner to w, gated on interactivity. Pure +
// testable: callers pass the TTY/width facts (see tty.go helpers).
func Banner(w io.Writer, isTTY bool, termWidth int, quiet bool, version string) {
	if quiet || !isTTY {
		return
	}
	if termWidth < bannerWidth {
		fmt.Fprintf(w, "%s · keyto-cli %s\n", compact, version)
		return
	}
	fmt.Fprintln(w, strings.TrimRight(bannerArt, "\n"))
	fmt.Fprintf(w, "keyto-cli %s\n", version)
}
