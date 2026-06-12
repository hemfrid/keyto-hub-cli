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
keyto auth              # sign in via your browser (Keyto SSO) — stores a credential locally
keyto checkout <name>   # clone a project and wire git to push through the Hub, then cd into it
keyto start             # boot the project locally: prereqs → env sync → docker compose → migrate → npm run dev
keyto update            # update keyto in place to the latest release
```

The everyday loop is **`keyto checkout <project>` → `keyto start`**:

- **`keyto checkout`** clones the project via the Hub git proxy, wires the git
  remote / credential helper / your commit identity, and (with shell
  integration) drops you into the directory. With no argument it lists your
  projects; run inside an existing checkout it re-wires it.
- **`keyto start`** brings the project up on your machine with one command. It
  checks that **git, Docker (daemon running), and Node ≥20** are present —
  offering to install anything missing (only after you confirm) — then runs
  `keyto env sync`, `docker compose up -d --wait`, your `migrate` script (when a
  database is up), `npm install` (if needed), and finally `npm run dev` in the
  foreground. Flags: `--no-sync`, `--no-migrate`, `--no-install`, `--yes`
  (auto-confirm prereq installs).

Then edit locally (with Claude Code, your editor, whatever) and `git push` — it
flows through the Keyto Hub to GitHub.

> **Renamed:** clone+wire moved from `keyto start` to **`keyto checkout`**;
> `keyto start` is now the local boot loop. `keyto start <name>` and the old
> `keyto dev` still work for now but print a deprecation notice. After upgrading,
> re-run `eval "$(keyto shell-init)"` (or reinstall) so the shell `cd` follows
> `keyto checkout`.

By default the CLI targets the production Hub (`https://hub.keytolabs.com`).
Override with `KEYTO_HUB_URL` (e.g. to test against UAT).

### Shell integration (cd into the project)

The installer adds a small `keyto` shell function to your rc so that
`keyto checkout` drops your shell straight into the cloned project — a plain
binary can't change its parent shell's directory, so the function does the
`cd` for it. (`keyto start` runs the app in the foreground, so it streams
straight through.) Without integration, `keyto checkout` prints the `cd`
command for you to run. To add it manually (or in a new shell), source the
snippet:

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
- `keyto checkout` → lists your projects, clones the chosen one via the Hub git
  proxy, and configures the git remote, the `keyto credential` helper, and your
  commit identity.
- `keyto start` → boots the checked-out project locally: prerequisite checks
  (git/Docker/Node, consent-gated install of anything missing) → `keyto env sync`
  → `docker compose up -d --wait` → `npm run migrate` (when a DB is up) →
  `npm install` (if needed) → `npm run dev`.
- `git push` → the credential helper supplies the credential → the Hub authorizes
  you live against project membership → relays to GitHub as the App. Revoking your
  access at the Hub cuts pushes immediately.

`keyto auth` / `keyto checkout` / `keyto start` also check (at most once a day,
cached in `~/.keyto/`) whether a newer release exists and nudge you to run
`keyto update`.
The check is fail-silent and never runs in non-interactive sessions.

`keyto update` downloads the latest release binary for your platform, verifies
its sha256 against the published `checksums.txt`, and atomically replaces the
running executable (on Windows the old binary is moved aside, since a running
`.exe` can't be overwritten in place).
