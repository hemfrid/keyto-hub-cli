# `keyto env set` — write env vars to UAT/PROD

**Status:** approved design — 2026-06-28

## Purpose

Give CLI users a way to **set and update** environment variables in the UAT and
PROD environments via the Keyto Hub. This is the write counterpart to the
existing `keyto env sync`, which only **reads** Hub-managed secrets into a local
`.env`.

## Command

```
keyto env set KEY=VALUE [KEY2=VALUE2 ...]   # inline (scriptable)
keyto env set KEY                            # value omitted → hidden prompt
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--env uat\|prod` | `uat` | Target environment. |
| `--allow-prod` | off | Required to target `prod` (mirrors `keyto env sync`). |

Semantics: **upsert** — a key that doesn't exist is created; one that exists is
overwritten. This covers both "set" and "update" with one verb.

### Argument parsing

- Each arg is either `KEY=VALUE` or a bare `KEY`.
- **All args `KEY=VALUE`** → every pair is written in one Hub call.
- **Exactly one bare `KEY`** (no `=`) → prompt for its value on the terminal
  with echo disabled, then write it.
- **Mixing** inline pairs and a bare key, or more than one bare key → error
  (keeps parsing unambiguous; no guessing which key the prompt is for).
- Value is split on the **first** `=` only, so values containing `=`
  (base64 padding, connection strings) survive intact.
- Keys are validated against `^[A-Za-z_][A-Za-z0-9_]*$`; an invalid key is a
  hard error before any network call.

### PROD safety

- `--allow-prod` is the gate (same explicit opt-in as `env sync`).
- On an interactive terminal, additionally show a confirmation listing the
  **keys** being written (never values) and require `y/N` before proceeding.
- On a non-interactive stdin (CI), `--allow-prod` alone proceeds — the flag is
  the deliberate opt-in; we don't block scripts on a prompt that would auto-decline.

## Backend contract (to confirm)

The Hub owner was unsure whether a write endpoint exists. This contract mirrors
the existing read endpoint
(`POST /api/cli/projects/{org}/{repo}/env/{env}/values`). **Confirm with the
Hub owner before relying on it.** Until the Hub serves it, the command compiles
and runs but surfaces the Hub's error (e.g. 404/405).

```
PUT /api/cli/projects/{org}/{repo}/env/{env}/values
Authorization: Bearer <credential>
Content-Type: application/json

Request:  { "values": { "KEY": "VALUE", ... } }
Response: 200 { "env": "uat", "updated": ["KEY", ...] }
Errors:   401 (unauthenticated) | 403 (not permitted for env) | 422 (bad input)
```

The CLI does not log values. Non-2xx responses return an error that includes the
HTTP status but never the raw body (matches existing client methods).

## Code layout

Follows the repo convention: a per-command package with injected dependencies
for testing, plus a thin wiring function in `cmd/keyto/main.go`.

### `internal/hub/client.go`

Add a method mirroring `FetchEnvValues`:

```go
func (c *Client) SetEnvValues(
    ctx context.Context,
    org, repo, env string,
    values map[string]string,
) (updated []string, err error)
```

- `PUT` to `/api/cli/projects/{org}/{repo}/env/{env}/values` with
  `{"values": {...}}`.
- Sends the Bearer credential.
- Decodes `{"env", "updated"}`; returns `updated`. Non-2xx → error with status,
  no body.

### `internal/envset/envset.go` (new package)

```go
type Setter func(ctx context.Context, org, repo, env string, values map[string]string) (updated []string, err error)

type Deps struct {
    Creds  *config.Creds          // nil → "not authenticated"
    Cwd    string                 // contains .keyto/project.json
    Set    Setter                 // Hub write call
    Prompt func(label string) (string, error) // hidden value prompt
    In     io.Reader              // for the prod y/N confirm
    Out    io.Writer
}

func Run(ctx context.Context, args []string, d Deps) error
```

`Run` responsibilities:
1. Parse flags (`--env`, `--allow-prod`).
2. Auth check (`Creds == nil` → error).
3. Prod gate (`--env prod` requires `--allow-prod`).
4. Read project marker (`.keyto/project.json`); error if absent.
5. Parse args into a `values` map (rules above); prompt if a bare key.
6. Prod TTY confirmation (keys only).
7. Call `d.Set(...)`; print `set N key(s) in <env>: KEY1, KEY2`.

### `cmd/keyto/main.go`

- `runEnvDispatch`: add `case "set": return runEnvSet(ctx, args[1:])`.
- `runEnvSet`: load creds (error on expired, like `env sync`), read cwd, build a
  `hub.Client`, wire `envset.Deps` with the real `SetEnvValues`, a hidden-prompt
  function (`golang.org/x/term`, already a dependency), `os.Stdin`, `os.Stdout`.
- Add a usage line under `env sync` in `printUsage`.

## Testing

- `internal/envset/envset_test.go`: arg parsing (inline pairs, single bare key,
  rejected mixes, invalid key names, `=` in value), the prod gate
  (`--env prod` without `--allow-prod` errors), and a happy-path `Run` with a
  fake `Setter` asserting the values map. Prompt and confirm are injected fakes.
- `internal/hub/client_test.go`: add a `SetEnvValues` case with an `httptest`
  server asserting method/path/body and decoding the response (mirrors the
  existing `FetchEnvValues` test).

## Out of scope (YAGNI)

- `keyto env unset` (delete a key).
- `--from-file` / bulk `.env` import.
- A `get`/list command.

Each is a small follow-up if needed; none is required for "set and update".
