package envset_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/envset"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

func projectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := project.Write(dir, &project.Marker{Name: "acme", Org: "hemfrid", Repo: "acme-web", HubURL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func baseDeps(dir string, capture *map[string]string) envset.Deps {
	return envset.Deps{
		Creds: &config.Creds{Credential: "tok", HubURL: "https://h"},
		Cwd:   dir,
		Set: func(ctx context.Context, org, repo, env string, values map[string]string) ([]string, error) {
			*capture = values
			keys := make([]string, 0, len(values))
			for k := range values {
				keys = append(keys, k)
			}
			return keys, nil
		},
		Out: &strings.Builder{},
	}
}

func TestRun_InlinePairs(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	if err := envset.Run(context.Background(), []string{"A=1", "B=x=y"}, d); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got["A"] != "1" || got["B"] != "x=y" {
		t.Errorf("values = %v (B must keep the '=' in its value)", got)
	}
}

func TestRun_BareKeyPrompts(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	d.Prompt = func(label string) (string, error) { return "secret-val", nil }
	if err := envset.Run(context.Background(), []string{"TOKEN"}, d); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got["TOKEN"] != "secret-val" {
		t.Errorf("values = %v", got)
	}
}

func TestRun_RejectsMixedBareAndPair(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	d.Prompt = func(string) (string, error) { return "", nil }
	if err := envset.Run(context.Background(), []string{"A=1", "B"}, d); err == nil {
		t.Fatal("expected error mixing KEY=VALUE and bare KEY")
	}
}

func TestRun_RejectsInvalidKey(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	if err := envset.Run(context.Background(), []string{"bad-key=1"}, d); err == nil {
		t.Fatal("expected error on invalid key name")
	}
}

func TestRun_ProdRequiresAllowProd(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	if err := envset.Run(context.Background(), []string{"--env", "prod", "A=1"}, d); err == nil {
		t.Fatal("expected error: prod without --allow-prod")
	}
}

func TestRun_ProdConfirmAbort(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	d.Confirm = func(string) bool { return false }
	err := envset.Run(context.Background(), []string{"--env", "prod", "--allow-prod", "A=1"}, d)
	if err == nil {
		t.Fatal("expected abort error when confirm returns false")
	}
	if got != nil {
		t.Errorf("Set must not be called after abort, got %v", got)
	}
}

func TestRun_ProdConfirmProceeds(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	d.Confirm = func(string) bool { return true }
	if err := envset.Run(context.Background(), []string{"--env", "prod", "--allow-prod", "A=1"}, d); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got["A"] != "1" {
		t.Errorf("values = %v", got)
	}
}

func TestRun_NotAuthed(t *testing.T) {
	dir := projectDir(t)
	var got map[string]string
	d := baseDeps(dir, &got)
	d.Creds = nil
	if err := envset.Run(context.Background(), []string{"A=1"}, d); err == nil {
		t.Fatal("expected not-authenticated error")
	}
}

func TestRun_NoMarker(t *testing.T) {
	var got map[string]string
	d := baseDeps(t.TempDir(), &got) // empty dir, no .keyto/project.json
	if err := envset.Run(context.Background(), []string{"A=1"}, d); err == nil {
		t.Fatal("expected error when no project marker")
	}
}

func TestRun_AppFlagTargetsResolvedProject(t *testing.T) {
	// --app must work from a dir with NO marker, and must target the resolved
	// project's org/repo (not the cwd's).
	var gotOrg, gotRepo, gotApp string
	d := envset.Deps{
		Creds: &config.Creds{Credential: "tok", HubURL: "https://h"},
		Cwd:   t.TempDir(), // no marker on purpose
		Set: func(ctx context.Context, org, repo, env string, values map[string]string) ([]string, error) {
			gotOrg, gotRepo = org, repo
			return []string{}, nil
		},
		Resolve: func(ctx context.Context, app string) (string, string, error) {
			gotApp = app
			return "other-org", "other-repo", nil
		},
		Out: &strings.Builder{},
	}
	if err := envset.Run(context.Background(), []string{"--app", "other-app", "A=1"}, d); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotApp != "other-app" {
		t.Errorf("resolved app = %q, want other-app", gotApp)
	}
	if gotOrg != "other-org" || gotRepo != "other-repo" {
		t.Errorf("target = %s/%s, want other-org/other-repo", gotOrg, gotRepo)
	}
}

func TestRun_AppFlagOverridesMarker(t *testing.T) {
	// Even inside a checkout, --app wins over the cwd marker.
	dir := projectDir(t) // marker: hemfrid/acme-web
	var gotOrg, gotRepo string
	d := envset.Deps{
		Creds: &config.Creds{Credential: "tok", HubURL: "https://h"},
		Cwd:   dir,
		Set: func(ctx context.Context, org, repo, env string, values map[string]string) ([]string, error) {
			gotOrg, gotRepo = org, repo
			return []string{}, nil
		},
		Resolve: func(ctx context.Context, app string) (string, string, error) {
			return "other-org", "other-repo", nil
		},
		Out: &strings.Builder{},
	}
	if err := envset.Run(context.Background(), []string{"--app", "other-app", "A=1"}, d); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotOrg != "other-org" || gotRepo != "other-repo" {
		t.Errorf("target = %s/%s, want the --app project, not the marker", gotOrg, gotRepo)
	}
}

func TestRun_AppFlagNotFound(t *testing.T) {
	var got map[string]string
	d := baseDeps(t.TempDir(), &got)
	d.Resolve = func(ctx context.Context, app string) (string, string, error) {
		return "", "", fmt.Errorf("app %q not found", app)
	}
	if err := envset.Run(context.Background(), []string{"--app", "ghost", "A=1"}, d); err == nil {
		t.Fatal("expected error when --app is not found")
	}
}
