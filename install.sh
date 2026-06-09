#!/bin/sh
# keyto CLI installer (macOS / Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/hemfrid/keyto-hub-cli/main/install.sh | sh
#
# Downloads the right binary for your OS/arch from the latest GitHub
# release, verifies it against the published sha256 checksums, and
# installs it as `keyto` on your PATH. No token needed (public release).
#
# Env overrides:
#   KEYTO_VERSION       release tag to install (default: latest), e.g. v0.1.0
#   KEYTO_INSTALL_DIR   install directory (default: /usr/local/bin if
#                       writable, else ~/.local/bin)
set -eu

REPO="hemfrid/keyto-hub-cli"
VERSION="${KEYTO_VERSION:-latest}"

err()  { printf 'keyto-install: %s\n' "$1" >&2; exit 1; }
info() { printf '%s\n' "$1"; }

# --- detect OS / arch -------------------------------------------------
os="$(uname -s)"
case "$os" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) err "unsupported OS: $os (supported: macOS, Linux; on Windows use install.ps1)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  arm64 | aarch64) arch="arm64" ;;
  x86_64 | amd64)  arch="amd64" ;;
  *) err "unsupported architecture: $arch (supported: arm64, amd64)" ;;
esac

asset="keyto_${os}_${arch}"
# Built targets: darwin/amd64, darwin/arm64, linux/amd64 (no linux/arm64 yet).
if [ "$os" = "linux" ] && [ "$arch" = "arm64" ]; then
  err "no linux/arm64 build yet (available: darwin/amd64, darwin/arm64, linux/amd64). Build from source: go build -o keyto ./cmd/keyto"
fi

# --- download base ----------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

dl() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2" || err "download failed: $1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1" || err "download failed: $1"
  else
    err "need curl or wget to download"
  fi
}

info "Downloading ${asset} (${VERSION})..."
dl "${base}/${asset}"        "${tmp}/${asset}"
dl "${base}/checksums.txt"   "${tmp}/checksums.txt"

# --- verify sha256 ----------------------------------------------------
expected="$(awk -v a="$asset" '$2 == a {print $1}' "${tmp}/checksums.txt")"
[ -n "$expected" ] || err "no checksum for ${asset} in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
else
  err "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch for ${asset} (expected ${expected}, got ${actual})"

# --- choose install dir (no sudo: prefer a writable dir) --------------
if [ -n "${KEYTO_INSTALL_DIR:-}" ]; then
  dir="$KEYTO_INSTALL_DIR"
elif [ -w /usr/local/bin ]; then
  dir="/usr/local/bin"
else
  dir="${HOME}/.local/bin"
fi
mkdir -p "$dir"

target="${dir}/keyto"
cp "${tmp}/${asset}" "$target"
chmod 0755 "$target"

# macOS: clear the quarantine xattr so Gatekeeper doesn't block the
# (currently unsigned) binary.
if [ "$os" = "darwin" ]; then
  xattr -d com.apple.quarantine "$target" 2>/dev/null || true
fi

info "Installed keyto -> ${target}"
case ":${PATH}:" in
  *":${dir}:"*) : ;;
  *) info "NOTE: ${dir} is not on your PATH. Add it, e.g.:  export PATH=\"${dir}:\$PATH\"" ;;
esac

# --- shell integration (true `cd` after `keyto start`) ----------------
# Adds a thin `keyto` wrapper function to the user's shell rc so that
# `keyto start` can cd the calling shell into the cloned project (a child
# process cannot change its parent shell's directory). Idempotent: a marker
# line guards against duplicate entries on re-install.
install_shell_integration() {
  marker="# >>> keyto shell integration >>>"
  shell_name="$(basename "${SHELL:-}")"
  case "$shell_name" in
    zsh)  rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash) rc="${HOME}/.bashrc" ;;
    fish) rc="${HOME}/.config/fish/config.fish" ;;
    *)    rc="" ;;
  esac
  if [ -z "$rc" ]; then
    info "NOTE: shell integration not auto-installed for '$shell_name'. Add manually:  eval \"\$(keyto shell-init)\""
    return
  fi
  if [ -f "$rc" ] && grep -qF "$marker" "$rc" 2>/dev/null; then
    info "Shell integration already present in $rc"
    return
  fi
  mkdir -p "$(dirname "$rc")"
  {
    printf '\n%s\n' "$marker"
    printf 'eval "$(keyto shell-init)"\n'
    printf '%s\n' "# <<< keyto shell integration <<<"
  } >> "$rc"
  info "Added shell integration to $rc — restart your shell or run:  source $rc"
}
install_shell_integration

info "Next: run  keyto auth"
