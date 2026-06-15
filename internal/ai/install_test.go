package ai

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// fakeDeps serves a fixed bundle from memory (no network).
func fakeDeps(t *testing.T, files map[string][]byte) Deps {
	t.Helper()
	manifest := manifestFor(files)
	manifest.SourceSHA = "abc123"
	manifest.ManifestCommit = "def456"
	tgz := buildTarball(t, files)
	return Deps{
		Meta: func(context.Context) (*hub.AIBundleMeta, error) {
			return &hub.AIBundleMeta{
				Tag: "v0.2.0", PublishedAt: "2026-06-12T00:00:00Z",
				SourceRepo: "hemfrid/ai-capabilities", Manifest: *manifest,
			}, nil
		},
		Tarball: func(context.Context, string) ([]byte, error) { return tgz, nil },
		HubURL:  "https://hub.test",
	}
}

func bundleFixture() map[string][]byte {
	return map[string][]byte{
		".claude/agents/api-designer.md": []byte("# api designer\n"),
		".claude/hooks/session-start.sh": []byte("#!/bin/bash\ngit merge origin/{{BASE_BRANCH}}\n"),
		".claude/settings.json":          []byte("{}\n"),
		"CLAUDE.md":                      []byte("# {{PROJECT_NAME}}\n"),
	}
}

func TestInitInstallsBundleAndWritesPin(t *testing.T) {
	root := initTestRepo(t)
	res, err := Init(context.Background(), root, fakeDeps(t, bundleFixture()))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(res.Written) != len(bundleFixture()) || len(res.Skipped) != 0 {
		t.Fatalf("written=%v skipped=%v", res.Written, res.Skipped)
	}

	// Substitution applied on write.
	hook, err := os.ReadFile(filepath.Join(root, ".claude/hooks/session-start.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(hook) != "#!/bin/bash\ngit merge origin/main\n" {
		t.Errorf("substitution not applied: %q", hook)
	}
	// .sh files are executable.
	info, _ := os.Stat(filepath.Join(root, ".claude/hooks/session-start.sh"))
	if info.Mode()&0o111 == 0 {
		t.Error("hook not executable")
	}

	pin, err := LoadPin(root)
	if err != nil || pin == nil {
		t.Fatalf("pin: %v %v", pin, err)
	}
	if pin.Tag != "v0.2.0" || pin.InstallChannel != "cli" {
		t.Errorf("pin = %+v", pin)
	}
	if pin.InstallInputs.TelemetryEndpoint != "https://hub.test/api/telemetry/events" {
		t.Errorf("telemetry input = %q", pin.InstallInputs.TelemetryEndpoint)
	}
	if pin.InstallInputs.BaseBranch != "main" {
		t.Errorf("base branch input = %q", pin.InstallInputs.BaseBranch)
	}
	// Pin hashes are POST-substitution (the local baseline).
	for _, f := range pin.Files {
		content, err := os.ReadFile(filepath.Join(root, f.Path))
		if err != nil {
			t.Fatalf("pinned file missing: %s", f.Path)
		}
		if sha(content) != f.SHA256 {
			t.Errorf("pin hash not post-substitution for %s", f.Path)
		}
	}
}

func TestInitNeverOverwritesExistingFiles(t *testing.T) {
	root := initTestRepo(t)
	// Pre-existing project CLAUDE.md must survive untouched and stay out of the pin.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("MINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	res, err := Init(context.Background(), root, fakeDeps(t, bundleFixture()))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "CLAUDE.md" {
		t.Fatalf("skipped = %v", res.Skipped)
	}
	content, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(content) != "MINE\n" {
		t.Errorf("pre-existing file clobbered: %q", content)
	}
	pin, _ := LoadPin(root)
	for _, f := range pin.Files {
		if f.Path == "CLAUDE.md" {
			t.Error("skipped file must not be pinned")
		}
	}
}

func TestInitRefusesDirtyTree(t *testing.T) {
	root := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), root, fakeDeps(t, bundleFixture())); err == nil {
		t.Error("expected dirty-tree refusal")
	}
}

func TestInitRefusesWhenAlreadyInstalled(t *testing.T) {
	root := initTestRepo(t)
	if _, err := Init(context.Background(), root, fakeDeps(t, bundleFixture())); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)
	if _, err := Init(context.Background(), root, fakeDeps(t, bundleFixture())); err == nil {
		t.Error("expected already-installed refusal")
	}
}

// withTag wraps a Meta func, overriding the advertised release tag.
func withTag(inner func(context.Context) (*hub.AIBundleMeta, error), tag string) func(context.Context) (*hub.AIBundleMeta, error) {
	return func(ctx context.Context) (*hub.AIBundleMeta, error) {
		m, err := inner(ctx)
		if err != nil {
			return nil, err
		}
		m.Tag = tag
		m.Manifest.Tag = tag
		return m, nil
	}
}

// gitCommitAll stages and commits everything in the test repo.
func gitCommitAll(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "wip"}} {
		cmd := gitCmd(root, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
