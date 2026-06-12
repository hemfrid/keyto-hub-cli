package boot

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func baseDeps(rec *[]string) Deps {
	return Deps{
		HasMarker: func() bool { return true },
		Scripts: func() (map[string]string, error) {
			return map[string]string{"dev": "next dev", "migrate": "tsx lib/migrate.ts"}, nil
		},
		EnsurePrereqs:      func(ctx context.Context) error { *rec = append(*rec, "prereq"); return nil },
		EnvSync:            func(ctx context.Context) error { *rec = append(*rec, "envsync"); return nil },
		EnvFileExists:      func() bool { return true },
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
	d.EnvFileExists = func() bool { return false }
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
