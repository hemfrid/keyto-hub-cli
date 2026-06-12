// Package boot implements `keyto start`: the one-command local boot loop.
// All side effects are injected so the orchestration is unit-tested with fakes.
package boot

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type Deps struct {
	HasMarker          func() bool
	Scripts            func() (map[string]string, error)
	EnsurePrereqs      func(ctx context.Context) error
	EnvSync            func(ctx context.Context) error
	EnvFileExists      func() bool
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

	fmt.Fprintln(d.Out, "• checking prerequisites…")
	if err := d.EnsurePrereqs(ctx); err != nil {
		return err
	}

	if f.NoSync {
		if !d.EnvFileExists() {
			return fmt.Errorf("--no-sync but no .env to reuse — run without --no-sync, or `keyto env sync` first")
		}
		fmt.Fprintln(d.Out, "• --no-sync: reusing existing .env")
	} else {
		fmt.Fprintln(d.Out, "• syncing env…")
		if err := d.EnvSync(ctx); err != nil {
			return fmt.Errorf("env sync: %w", err)
		}
	}

	if d.HasCompose() {
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

	if _, hasMigrate := scripts["migrate"]; hasMigrate && !f.NoMigrate && d.DBRunning(ctx) {
		fmt.Fprintln(d.Out, "• applying migrations…")
		if err := d.RunScript(ctx, "migrate"); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "connect") {
				return fmt.Errorf("migrate could not reach the DB — check DATABASE_URL/POSTGRES_PORT: %w", err)
			}
			return fmt.Errorf("migrations failed (fix the schema and re-run): %w", err)
		}
	} else {
		fmt.Fprintln(d.Out, "• no migrations to run (no DB or no migrate script)")
	}

	// Open the app in the browser once it's listening. Launched before the
	// (blocking) dev server so it can poll while next dev boots; it opens the
	// browser when the URL responds and is a no-op if the server never comes up.
	if shouldOpenApp(d, f) {
		go d.OpenApp(ctx)
	}

	fmt.Fprintln(d.Out, "• starting the app (npm run dev) — Ctrl-C to stop. Backing services stay up; `docker compose down` to stop them.")
	return d.RunScript(ctx, "dev")
}
