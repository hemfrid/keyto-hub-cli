// Package shellinit emits the shell function that gives `keyto checkout` the
// ability to change the calling shell's working directory after a clone.
//
// A CLI binary is a child process and cannot cd its parent shell, so we ship a
// thin wrapper function (sourced from the user's shell rc via
// `eval "$(keyto shell-init)"`). For `keyto checkout` the function runs the
// real binary with KEYTO_SHELL_INTEGRATION=1 — which makes the binary print
// the resolved project directory on stdout and route all human output to
// stderr — then cd's into that directory. `keyto start` will become a
// foreground boot loop whose stdout must stream (not be captured), so it is
// passed straight through to the real binary unchanged. Every other subcommand
// is also passed straight through.
package shellinit

import "fmt"

// posixFunc is the wrapper for POSIX-family shells (bash, zsh, sh, ksh). It
// avoids `local` (not POSIX) by using a uniquely-named variable it unsets.
const posixFunc = `keyto() {
  if [ "$1" = "checkout" ]; then
    __keyto_target="$(KEYTO_SHELL_INTEGRATION=1 command keyto "$@")" || return $?
    if [ -n "$__keyto_target" ] && [ -d "$__keyto_target" ]; then
      cd "$__keyto_target" || return $?
    fi
    unset __keyto_target
  else
    command keyto "$@"
  fi
}`

// fishFunc is the wrapper for the fish shell.
const fishFunc = `function keyto
  if test "$argv[1]" = "checkout"
    set -l __keyto_target (KEYTO_SHELL_INTEGRATION=1 command keyto $argv)
    or return $status
    if test -n "$__keyto_target" -a -d "$__keyto_target"
      cd "$__keyto_target"
    end
  else
    command keyto $argv
  end
end`

// Script returns the shell-integration wrapper for the given shell. An empty
// shell name, or any POSIX-family shell (bash/zsh/sh/ksh), yields the POSIX
// function; "fish" yields the fish function. Unknown shells return an error.
func Script(shell string) (string, error) {
	switch shell {
	case "", "sh", "bash", "zsh", "ksh":
		return posixFunc + "\n", nil
	case "fish":
		return fishFunc + "\n", nil
	default:
		return "", fmt.Errorf("unsupported shell %q (supported: bash, zsh, sh, ksh, fish)", shell)
	}
}
