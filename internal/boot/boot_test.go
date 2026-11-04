package boot

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
	"github.com/hemfrid/keyto-hub-cli/internal/stack"
)

func baseDeps(rec *[]string) Deps {
	return Deps{
		HasMarker: func() bool { return true },
		Scripts: func() (map[string]string, error) {
			return map[string]string{"dev": "next dev", "migrate": "tsx lib/migrate.ts"}, nil
		},
		EnsurePrereqs: func(ctx context.Context, tools []prereq.Tool) error {
			*rec = append(*rec, "prereq")
			return nil
		},
		EnvSync:            func(ctx context.Context) error { *rec = append(*rec, "envsync"); return nil },
		EnvFileExists:      func(name string) bool { return true },
		HasCompose:         func() bool { return true },
		ComposeUp:          func(ctx context.Context) error { *rec = append(*rec, "composeup"); return nil },
		DBRunning:          func(ctx context.Context) bool { return true },
		NodeModulesPresent: func() bool { return true },
		Install:            func(ctx context.Context) error { *rec = append(*rec, "install"); return nil },
		RunScript: func(ctx context.Context, script string) error {
			*rec = append(*rec, "run:"+script)
			return nil
		},
		Out: &bytes.Buffer{},
	}
}

func TestRun_HappyPath_OrderedSteps(t *testing.T) {
	var rec []string
	if err := Run(context.Background(), baseDeps(&rec), Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []string{"prereq", "envsync", "composeup", "run:migrate", "run:dev"}
	if strings.Join(rec, ",") != strings.Join(want, ",") {
		t.Fatalf("step order = %v, want %v", rec, want)
	}
}

func TestRun_NoMarker_Errors(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.HasMarker = func() bool { return false }
	if err := Run(context.Background(), d, Flags{}); err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("want no-project error pointing at checkout, got %v", err)
	}
}

func TestRun_NoSyncWithoutEnvFile_Errors(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.EnvFileExists = func(name string) bool { return false }
	if err := Run(context.Background(), d, Flags{NoSync: true}); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("want .env guard error, got %v", err)
	}
}

func TestRun_NoDB_SkipsMigrate(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.DBRunning = func(ctx context.Context) bool { return false }
	_ = Run(context.Background(), d, Flags{})
	for _, s := range rec {
		if s == "run:migrate" {
			t.Fatal("migrate must be skipped when no DB is running")
		}
	}
}

func TestRun_MigrateFails_FatalBeforeApp(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.RunScript = func(ctx context.Context, script string) error {
		rec = append(rec, "run:"+script)
		if script == "migrate" {
			return errors.New("relation already exists")
		}
		return nil
	}
	err := Run(context.Background(), d, Flags{})
	if err == nil {
		t.Fatal("migrate failure must be fatal")
	}
	for _, s := range rec {
		if s == "run:dev" {
			t.Fatal("app must NOT start after a migrate failure")
		}
	}
}

func TestRun_NoInstallFlag_SkipsInstallButRunsApp(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.NodeModulesPresent = func() bool { return false }
	_ = Run(context.Background(), d, Flags{NoInstall: true})
	for _, s := range rec {
		if s == "install" {
			t.Fatal("--no-install must skip npm install")
		}
	}
}

func TestShouldOpenApp(t *testing.T) {
	open := func(context.Context) {}
	cases := []struct {
		name string
		dep  func(context.Context)
		flag Flags
		want bool
	}{
		{"wired, default", open, Flags{}, true},
		{"wired, --no-open", open, Flags{NoOpen: true}, false},
		{"no dep", nil, Flags{}, false},
	}
	for _, c := range cases {
		d := Deps{OpenApp: c.dep}
		if got := shouldOpenApp(d, c.flag); got != c.want {
			t.Errorf("%s: shouldOpenApp = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRun_OpensApp_WhenWired(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	opened := make(chan struct{}, 1)
	d.OpenApp = func(context.Context) { opened <- struct{}{} }

	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	select {
	case <-opened:
		// browser-open was launched as expected
	case <-time.After(2 * time.Second):
		t.Fatal("OpenApp was not invoked")
	}
}

func TestRun_NoOpen_DoesNotOpenApp(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	opened := make(chan struct{}, 1)
	d.OpenApp = func(context.Context) { opened <- struct{}{} }

	if err := Run(context.Background(), d, Flags{NoOpen: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	select {
	case <-opened:
		t.Fatal("OpenApp must not run under --no-open")
	case <-time.After(100 * time.Millisecond):
		// no open — correct
	}
}

func TestRun_MissingNodeModules_Installs(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.NodeModulesPresent = func() bool { return false }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var installed bool
	for _, s := range rec {
		if s == "install" {
			installed = true
		}
		// install MUST precede migrate (npm run migrate needs tsx/drizzle from
		// node_modules) AND the app. Regression guard for the "tsx: command not
		// found" exit-127 on a fresh checkout.
		if (s == "run:migrate" || s == "run:dev") && !installed {
			t.Fatalf("install must run before %q when node_modules is absent", s)
		}
	}
	if !installed {
		t.Fatal("expected install to run when node_modules is absent")
	}
}

func TestRun_NextJS_PrereqsIncludeDockerAndCompose(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	var got []prereq.Tool
	d.EnsurePrereqs = func(ctx context.Context, tools []prereq.Tool) error { got = tools; return nil }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []prereq.Tool{prereq.Git, prereq.Docker, prereq.DockerCompose, prereq.Node}
	if !equalTools(got, want) {
		t.Fatalf("prereqs = %v, want %v", got, want)
	}
}

// workersDeps is baseDeps for a Workers repo: no compose file, no DB probe
// that could succeed, and a migrate script that applies D1 migrations locally.
func workersDeps(rec *[]string) Deps {
	d := baseDeps(rec)
	d.Stack = stack.Workers
	d.Scripts = func() (map[string]string, error) {
		return map[string]string{"dev": "node scripts/dev.mjs", "migrate": "wrangler d1 migrations apply DB --local"}, nil
	}
	d.HasCompose = func() bool { *rec = append(*rec, "hascompose"); return true }
	d.DBRunning = func(ctx context.Context) bool { *rec = append(*rec, "dbrunning"); return false }
	return d
}

func TestRun_Workers_PrereqsAreGitAndNodeOnly(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	var got []prereq.Tool
	d.EnsurePrereqs = func(ctx context.Context, tools []prereq.Tool) error { got = tools; return nil }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// NodeWorkers, not Node: wrangler 4 needs Node >=22, above the Next.js 20.9.
	want := []prereq.Tool{prereq.Git, prereq.NodeWorkers}
	if !equalTools(got, want) {
		t.Fatalf("prereqs = %v, want %v (no Docker, no Compose, the Node 22 floor)", got, want)
	}
}

func TestRun_Workers_NoComposeMigrateWithoutDBProbeThenDev(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.ComposeUp = func(ctx context.Context) error {
		t.Fatal("ComposeUp must never be called for a Workers repo")
		return nil
	}
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []string{"prereq", "envsync", "run:migrate", "run:dev"}
	if strings.Join(rec, ",") != strings.Join(want, ",") {
		t.Fatalf("step order = %v, want %v (HasCompose and DBRunning are not consulted)", rec, want)
	}
}

func TestRun_Workers_NoMigrateScript_SkipsMigrate(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.Scripts = func() (map[string]string, error) { return map[string]string{"dev": "x"}, nil }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, s := range rec {
		if s == "run:migrate" {
			t.Fatal("migrate must not run without a migrate script")
		}
	}
}

func TestRun_Workers_NoMigrateFlag_SkipsMigrate(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	out := &bytes.Buffer{}
	d.Out = out
	if err := Run(context.Background(), d, Flags{NoMigrate: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(out.String(), "skipped (--no-migrate)") {
		t.Fatalf("the status line should say migrations were skipped by the flag, got:\n%s", out.String())
	}
	for _, s := range rec {
		if s == "run:migrate" {
			t.Fatal("--no-migrate must skip migrate")
		}
	}
}

func TestRun_Workers_InstallBeforeMigrate(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.NodeModulesPresent = func() bool { return false }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []string{"prereq", "envsync", "install", "run:migrate", "run:dev"}
	if strings.Join(rec, ",") != strings.Join(want, ",") {
		t.Fatalf("step order = %v, want %v", rec, want)
	}
}

func TestRun_Workers_NoSyncChecksDevVars(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	var asked string
	d.EnvFileExists = func(name string) bool { asked = name; return false }
	err := Run(context.Background(), d, Flags{NoSync: true})
	if err == nil || !strings.Contains(err.Error(), ".dev.vars") {
		t.Fatalf("want a .dev.vars guard error, got %v", err)
	}
	if asked != ".dev.vars" {
		t.Fatalf("EnvFileExists asked for %q, want .dev.vars", asked)
	}
}

func TestRun_Workers_StatusLinesNameWranglerDev(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	out := &bytes.Buffer{}
	d.Out = out
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "wrangler dev") {
		t.Fatalf("status lines should name wrangler dev, got:\n%s", s)
	}
	if strings.Contains(s, "docker") {
		t.Fatalf("a Workers boot must not mention docker, got:\n%s", s)
	}
}

func TestRun_Workers_MigrateFails_FatalBeforeApp(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.RunScript = func(ctx context.Context, script string) error {
		rec = append(rec, "run:"+script)
		if script == "migrate" {
			return errors.New("wrangler: connect ECONNREFUSED (D1 migration 0002)")
		}
		return nil
	}
	err := Run(context.Background(), d, Flags{})
	if err == nil || strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want a migrate error without the Postgres hint, got %v", err)
	}
	for _, s := range rec {
		if s == "run:dev" {
			t.Fatal("app must NOT start after a migrate failure")
		}
	}
}

func equalTools(a, b []prereq.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- the @hemfrid package lane in ~/.npmrc (Workers only) ----

func TestRun_Workers_NpmLaneBeforeInstall(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.NodeModulesPresent = func() bool { return false }
	d.NpmLane = func(ctx context.Context) error { rec = append(rec, "npmlane"); return nil }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []string{"prereq", "envsync", "npmlane", "install", "run:migrate", "run:dev"}
	if strings.Join(rec, ",") != strings.Join(want, ",") {
		t.Fatalf("step order = %v, want %v", rec, want)
	}
}

func TestRun_Workers_NpmLaneRunsWithNoSyncAndNoInstall(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	d.NpmLane = func(ctx context.Context) error { rec = append(rec, "npmlane"); return nil }
	if err := Run(context.Background(), d, Flags{NoSync: true, NoInstall: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(strings.Join(rec, ","), "npmlane") {
		t.Fatalf("the lane is set up even with --no-sync --no-install (a later npm ci needs it), steps: %v", rec)
	}
}

func TestRun_Workers_NpmLaneFailureIsATipNotAnError(t *testing.T) {
	var rec []string
	d := workersDeps(&rec)
	out := &bytes.Buffer{}
	d.Out = out
	d.NpmLane = func(ctx context.Context) error { return errors.New("no keyto credential — run `keyto auth`") }
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("a lane failure must not stop keyto start, got %v", err)
	}
	if !strings.Contains(out.String(), "tip: no keyto credential") {
		t.Fatalf("the failure should be printed as a tip, got:\n%s", out.String())
	}
	if !strings.Contains(strings.Join(rec, ","), "run:dev") {
		t.Fatalf("boot should carry on to the dev server, steps: %v", rec)
	}
}

func TestRun_NextJS_NeverTouchesNpmLane(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.NpmLane = func(ctx context.Context) error {
		t.Fatal("a Next.js checkout must not touch ~/.npmrc")
		return nil
	}
	if err := Run(context.Background(), d, Flags{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
