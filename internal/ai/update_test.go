package ai

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// initInstalled sets up a repo with v0.2.0 installed and committed.
func initInstalled(t *testing.T) string {
	t.Helper()
	root := initTestRepo(t)
	if _, err := Init(context.Background(), root, fakeDeps(t, bundleFixture())); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)
	return root
}

// v2Fixture: api-designer changed upstream, settings.json unchanged,
// session-start.sh removed upstream, a new rule added.
func v2Fixture() map[string][]byte {
	return map[string][]byte{
		".claude/agents/api-designer.md": []byte("# api designer v2\n"),
		".claude/settings.json":          []byte("{}\n"),
		"CLAUDE.md":                      []byte("# {{PROJECT_NAME}}\n"),
		".claude/rules/new-rule.md":      []byte("# new rule on {{BASE_BRANCH}}\n"),
	}
}

func TestUpdateAppliesNewVersion(t *testing.T) {
	root := initInstalled(t)
	d := fakeDeps(t, v2Fixture())
	d.Meta = withTag(d.Meta, "v0.3.0")

	res, err := Update(context.Background(), root, d)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.UpToDate {
		t.Fatal("should not be up to date")
	}

	got, _ := os.ReadFile(filepath.Join(root, ".claude/agents/api-designer.md"))
	if string(got) != "# api designer v2\n" {
		t.Errorf("unmodified file not updated: %q", got)
	}
	added, _ := os.ReadFile(filepath.Join(root, ".claude/rules/new-rule.md"))
	if string(added) != "# new rule on main\n" {
		t.Errorf("new file not written/substituted: %q", added)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/hooks/session-start.sh")); !os.IsNotExist(err) {
		t.Error("upstream-removed unmodified file should be deleted")
	}
	pin, _ := LoadPin(root)
	if pin.Tag != "v0.3.0" {
		t.Errorf("pin tag = %q", pin.Tag)
	}
}

func TestUpdateSkipsLocallyModified(t *testing.T) {
	root := initInstalled(t)
	mine := []byte("# MY customized agent\n")
	if err := os.WriteFile(filepath.Join(root, ".claude/agents/api-designer.md"), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	d := fakeDeps(t, v2Fixture())
	d.Meta = withTag(d.Meta, "v0.3.0")
	res, err := Update(context.Background(), root, d)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(res.SkippedModified) != 1 || res.SkippedModified[0] != ".claude/agents/api-designer.md" {
		t.Fatalf("SkippedModified = %v", res.SkippedModified)
	}
	got, _ := os.ReadFile(filepath.Join(root, ".claude/agents/api-designer.md"))
	if string(got) != string(mine) {
		t.Error("locally modified file was clobbered")
	}
	// Old baseline hash kept so the NEXT update still sees it as modified.
	pin, _ := LoadPin(root)
	for _, f := range pin.Files {
		if f.Path == ".claude/agents/api-designer.md" && f.SHA256 == sha(mine) {
			t.Error("pin baseline must stay at the installed version's hash, not the local edit")
		}
	}
}

func TestUpdateDoesNotResurrectDeleted(t *testing.T) {
	root := initInstalled(t)
	if err := os.Remove(filepath.Join(root, ".claude/agents/api-designer.md")); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	d := fakeDeps(t, v2Fixture())
	d.Meta = withTag(d.Meta, "v0.3.0")
	res, err := Update(context.Background(), root, d)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(res.MissingLocal) != 1 {
		t.Fatalf("MissingLocal = %v", res.MissingLocal)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/agents/api-designer.md")); !os.IsNotExist(err) {
		t.Error("deliberately deleted file was resurrected")
	}
}

func TestUpdateUpToDate(t *testing.T) {
	root := initInstalled(t)
	res, err := Update(context.Background(), root, fakeDeps(t, bundleFixture())) // same tag v0.2.0
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.UpToDate {
		t.Error("same tag should report up to date")
	}
}

func TestUpdateSkipsExistingUnpinnedFile(t *testing.T) {
	root := initTestRepo(t)
	// Write the file before init so Init skips it (not pinned).
	preexisting := []byte("# my local new-rule\n")
	if err := os.MkdirAll(filepath.Join(root, ".claude/rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude/rules/new-rule.md"), preexisting, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)
	if _, err := Init(context.Background(), root, fakeDeps(t, bundleFixture())); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	// v2 ships new-rule.md; our file is not pinned, so it must be left alone.
	d := fakeDeps(t, v2Fixture())
	d.Meta = withTag(d.Meta, "v0.3.0")
	res, err := Update(context.Background(), root, d)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(res.SkippedExisting) != 1 || res.SkippedExisting[0] != ".claude/rules/new-rule.md" {
		t.Fatalf("SkippedExisting = %v", res.SkippedExisting)
	}
	got, _ := os.ReadFile(filepath.Join(root, ".claude/rules/new-rule.md"))
	if string(got) != string(preexisting) {
		t.Error("unpinned existing file was overwritten")
	}
}

func TestUpdateRequiresInstallAndCleanTree(t *testing.T) {
	root := initTestRepo(t)
	if _, err := Update(context.Background(), root, fakeDeps(t, bundleFixture())); err == nil {
		t.Error("expected not-installed error")
	}
	installed := initInstalled(t)
	if err := os.WriteFile(filepath.Join(installed, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(context.Background(), installed, fakeDeps(t, bundleFixture())); err == nil {
		t.Error("expected dirty-tree refusal")
	}
}
