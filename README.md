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
keyto doctor            # diagnose local prerequisites and print how to fix them
keyto update            # update keyto in place to the latest release
```

The everyday loop is **`keyto checkout <project>` → `keyto start`**:

- **`keyto checkout`** clones the project via the Hub git proxy, wires the git
  remote / credential helper / your commit identity, and (with shell
  integration) drops you into the directory. With no argument it lists your
  projects; run inside an existing checkout it re-wires it. If git is missing it
  guides you through installing it first.
- **`keyto start`** brings the project up on your machine with one command. It
  runs the same preflight as `keyto doctor` — **git, the Docker engine + daemon,
  the Docker Compose v2 plugin, and Node ≥20** — and offers consent-gated
  installs of anything missing (Linux Node via NodeSource, macOS via brew/nvm,
  Windows via winget; only after you confirm). It warns on a low Linux inotify
  watcher limit (which can break `next dev` hot reload), then runs
  `keyto env sync`, `docker compose up -d --wait`, your `migrate` script (when a
  database is up), `npm install` (if needed), and finally `npm run dev` in the
  foreground. Once the dev server is listening it **opens the app in your
  browser** (`http://localhost:3000`, or `$PORT`). Flags: `--no-sync`,
  `--no-migrate`, `--no-install`, `--yes` (auto-confirm prereq installs),
  `--no-open` (don't open the browser).

> **Each project is its own Docker run.** `keyto env sync` writes
> `COMPOSE_PROJECT_NAME=<project>` plus deterministic per-project host ports
> (`POSTGRES_PORT`/`REDIS_PORT`/`MYSQL_PORT`) into `.env`, so every project gets
> its **own** containers, volume and network — `<project>-postgres-1`, not a
> shared `keyto-app`. This is why the project's database is created reliably
> (a shared volume only honors `POSTGRES_DB` on its first init) and lets two
> projects' stacks run side by side. On the rare port clash, override e.g.
> `POSTGRES_PORT` in `.env.local`.

Then edit locally (with Claude Code, your editor, whatever) and `git push` — it
flows through the Keyto Hub to GitHub.

### `keyto doctor`

`keyto doctor` runs the local prerequisite diagnostics on their own — **git, the
Docker engine + daemon, the Docker Compose v2 plugin, and Node 20+**. It is
**detect-only** (it changes nothing without `--fix`) and classifies each issue by
how it gets fixed:

- **auto** — keyto can install it for you;
- **command** — a single command you run (printed inline);
- **manual** — a multi-step human action (e.g. enabling CPU virtualization in
  BIOS/UEFI on Windows so Docker Desktop/WSL2 can start at all).

It prints an **AI-readable summary** you can paste straight into Claude or Codex
to be walked through the fixes (plus a fixability tally). Flags:

- `--json` — machine-readable output for tooling.
- `--fix` — run the auto/command fixes (consent-gated; manual items are never
  auto-run), then re-diagnose.
- `--report` / `--no-report` — upload the report to the Hub, where admins see it
  at `/admin/diagnostics`. Default-on when you're signed in; always best-effort,
  so a failed upload never changes the result.

`keyto start` runs the same checks as part of its preflight, so `keyto doctor` is
mainly for diagnosing a machine before (or instead of) booting a project.

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
  revocable, per-user credential stored `0600` in `~/.keyto/`. If your sign-in
  has expired or been revoked, `keyto checkout` / `keyto start` re-auth you
  automatically — no more `keyto auth --force`.
- `keyto checkout` → lists your projects, clones the chosen one via the Hub git
  proxy, and configures the git remote, the `keyto credential` helper, and your
  commit identity.
- `keyto start` → boots the checked-out project locally: prerequisite checks
  (git/Docker/Node, consent-gated install of anything missing) → `keyto env sync`
  (writes `COMPOSE_PROJECT_NAME` + per-project host ports for an isolated Docker
  run) → `docker compose up -d --wait` → `npm install` (if needed) →
  `npm run migrate` (when a DB is up) → `npm run dev`, then opens the app URL in
  the browser (`--no-open` to skip; skipped automatically on CI/non-interactive).
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
