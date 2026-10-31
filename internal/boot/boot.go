// Package boot implements `keyto start`: the one-command local boot loop.
// All side effects are injected so the orchestration is unit-tested with fakes.
package boot

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
	"github.com/hemfrid/keyto-hub-cli/internal/stack"
)

type Deps struct {
	// Stack is the checkout's stack (stack.Detect). The zero value boots the
	// Next.js loop.
	Stack     stack.Kind
	HasMarker func() bool
	Scripts   func() (map[string]string, error)
	// EnsurePrereqs checks (and, consent-gated, installs) the given tools.
	EnsurePrereqs func(ctx context.Context, tools []prereq.Tool) error
	EnvSync       func(ctx context.Context) error
	// EnvFileExists reports whether the named env file (".env" or
	// ".dev.vars") exists in the project directory.
	EnvFileExists      func(name string) bool
	HasCompose         func() bool
	ComposeUp          func(ctx context.Context) error
	DBRunning          func(ctx context.Context) bool
	NodeModulesPresent func() bool
	Install            func(ctx context.Context) error
	RunScript          func(ctx context.Context, script string) error
	// OpenApp, when set, is launched in the background just before the dev
	// server starts. The real impl waits until the app URL responds, then opens
	// it in the browser. nil (e.g. CI / non-interactive) disables the behaviour.
	OpenApp func(ctx context.Context)
	Out     io.Writer
}

type Flags struct {
	NoSync, NoMigrate, NoInstall, Yes, NoOpen bool
}

// shouldOpenApp reports whether the browser should be opened once the dev
// server is up: an OpenApp dep is wired AND the user didn't pass --no-open.
func shouldOpenApp(d Deps, f Flags) bool {
	return d.OpenApp != nil && !f.NoOpen
}

// Prereqs returns the tools `keyto start` needs for a stack. A Workers repo
// runs on Node alone (wrangler dev, local D1/KV), so it needs no Docker.
func Prereqs(k stack.Kind) []prereq.Tool {
	if k == stack.Workers {
		return []prereq.Tool{prereq.Git, prereq.Node}
	}
	// next dev runs against `docker compose up`, so the Compose v2 plugin is
	// a hard prerequisite alongside the docker engine itself.
	return []prereq.Tool{prereq.Git, prereq.Docker, prereq.DockerCompose, prereq.Node}
}

// EnvFile is the file `keyto env sync` writes for a stack.
func EnvFile(k stack.Kind) string {
	if k == stack.Workers {
		return ".dev.vars"
	}
	return ".env"
}

func Run(ctx context.Context, d Deps, f Flags) error {
	if !d.HasMarker() {
		return fmt.Errorf("no keyto project in this directory — run `keyto checkout <project>` first")
	}
	scripts, err := d.Scripts()
	if err != nil {
		return fmt.Errorf("read package.json scripts: %w", err)
	}
	if _, ok := scripts["dev"]; !ok {
		return fmt.Errorf("no \"dev\" script in package.json — nothing to run")
	}

	workers := d.Stack == stack.Workers
	envFile := EnvFile(d.Stack)

	fmt.Fprintln(d.Out, "• checking prerequisites…")
	if err := d.EnsurePrereqs(ctx, Prereqs(d.Stack)); err != nil {
		return err
	}

	if f.NoSync {
		if !d.EnvFileExists(envFile) {
			return fmt.Errorf("--no-sync but no %s to reuse — run without --no-sync, or `keyto env sync` first", envFile)
		}
		fmt.Fprintf(d.Out, "• --no-sync: reusing existing %s\n", envFile)
	} else {
		fmt.Fprintln(d.Out, "• syncing env…")
		if err := d.EnvSync(ctx); err != nil {
			return fmt.Errorf("env sync: %w", err)
		}
	}

	// A Workers repo has no backing services: D1 and KV run inside wrangler.
	if !workers && d.HasCompose() {
		fmt.Fprintln(d.Out, "• starting backing services (docker compose up --wait)…")
		if err := d.ComposeUp(ctx); err != nil {
			return fmt.Errorf("docker compose up: %w (a port collision? set POSTGRES_PORT; or `docker compose logs`)", err)
		}
	}

	// Install deps BEFORE migrate: `npm run migrate` runs tsx/drizzle from
	// node_modules, which a fresh checkout doesn't have yet (else exit 127,
	// "tsx: command not found").
	if !f.NoInstall && !d.NodeModulesPresent() {
		fmt.Fprintln(d.Out, "• installing dependencies…")
		if err := d.Install(ctx); err != nil {
			return fmt.Errorf("npm install: %w", err)
		}
	}

	// A Workers repo's migrate script applies the D1 migrations locally with
	// wrangler, so there is no database to probe first.
	_, hasMigrate := scripts["migrate"]
	if hasMigrate && !f.NoMigrate && (workers || d.DBRunning(ctx)) {
		fmt.Fprintln(d.Out, "• applying migrations…")
		if err := d.RunScript(ctx, "migrate"); err != nil {
			if !workers && strings.Contains(strings.ToLower(err.Error()), "connect") {
				return fmt.Errorf("migrate could not reach the DB — check DATABASE_URL/POSTGRES_PORT: %w", err)
			}
			return fmt.Errorf("migrations failed (fix the schema and re-run): %w", err)
		}
	} else if workers {
		fmt.Fprintln(d.Out, "• no migrations to run")
	} else {
		fmt.Fprintln(d.Out, "• no migrations to run (no DB or no migrate script)")
	}

	// Open the app in the browser once it's listening. Launched before the
	// (blocking) dev server so it can poll while next dev boots; it opens the
	// browser when the URL responds and is a no-op if the server never comes up.
	if shouldOpenApp(d, f) {
		go d.OpenApp(ctx)
	}

	if workers {
		fmt.Fprintln(d.Out, "• starting the app (npm run dev → wrangler dev) — Ctrl-C to stop.")
	} else {
		fmt.Fprintln(d.Out, "• starting the app (npm run dev) — Ctrl-C to stop. Backing services stay up; `docker compose down` to stop them.")
	}
	return d.RunScript(ctx, "dev")
}
