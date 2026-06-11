# keyto

The Keyto CLI — clone a Keyto Hub project to your machine and push your work
**through the Hub**, with no GitHub account needed. The Hub authenticates you
(SSO), mints a short-lived per-repo token server-side, and relays your `git push`
to GitHub as the single Keyto GitHub App.

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/hemfrid/keyto-hub-cli/main/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/hemfrid/keyto-hub-cli/main/install.ps1 | iex
```

Both installers download the latest release binary for your platform, **verify it
against the published sha256 checksums**, and put `keyto` on your PATH. Pin a
version with `KEYTO_VERSION=vX.Y.Z`; override the location with
`KEYTO_INSTALL_DIR` (macOS/Linux).

## Usage

```sh
keyto auth      # sign in via your browser (Keyto SSO) — stores a credential locally
keyto start     # pick a project, clone it, and wire git to push through the Hub
keyto update    # update keyto in place to the latest release
```

Then edit locally (with Claude Code, your editor, whatever) and `git push` — it
flows through the Keyto Hub to GitHub. `keyto start` also detects when you're
already inside a Keyto project and offers to resume it.

By default the CLI targets the production Hub (`https://hub.keytolabs.com`).
Override with `KEYTO_HUB_URL` (e.g. to test against UAT).

### Shell integration (cd into the project)

The installer adds a small `keyto` shell function to your rc so that
`keyto start` drops your shell straight into the cloned project — a plain
binary can't change its parent shell's directory, so the function does the
`cd` for it. Without integration, `keyto start` prints the `cd` command for you
to run. To add it manually (or in a new shell), source the snippet:

```sh
eval "$(keyto shell-init)"   # add to ~/.zshrc / ~/.bashrc; fish supported too
```

## Build from source

```sh
go build -o keyto ./cmd/keyto
```

## How it fits together

- `keyto auth` → browser SSO (RFC 8252 loopback + PKCE) → the Hub issues a
  revocable, per-user credential stored `0600` in `~/.keyto/`.
- `keyto start` → lists your projects, clones the chosen one via the Hub git
  proxy, and configures the git remote, the `keyto credential` helper, and your
  commit identity.
- `git push` → the credential helper supplies the credential → the Hub authorizes
  you live against project membership → relays to GitHub as the App. Revoking your
  access at the Hub cuts pushes immediately.

`keyto auth` / `keyto start` also check (at most once a day, cached in
`~/.keyto/`) whether a newer release exists and nudge you to run `keyto update`.
The check is fail-silent and never runs in non-interactive sessions.

`keyto update` downloads the latest release binary for your platform, verifies
its sha256 against the published `checksums.txt`, and atomically replaces the
running executable (on Windows the old binary is moved aside, since a running
`.exe` can't be overwritten in place).
