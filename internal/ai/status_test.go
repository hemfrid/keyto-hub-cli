package ai

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusUpToDateAndClean(t *testing.T) {
	root := initInstalled(t)
	st, err := GetStatus(context.Background(), root, fakeDeps(t, bundleFixture()))
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.UpToDate || len(st.Modified) != 0 || len(st.Missing) != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestStatusReportsDriftAndNewTag(t *testing.T) {
	root := initInstalled(t)
	if err := os.WriteFile(filepath.Join(root, ".claude/agents/api-designer.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".claude/settings.json")); err != nil {
		t.Fatal(err)
	}
	d := fakeDeps(t, bundleFixture())
	d.Meta = withTag(d.Meta, "v0.9.0")

	st, err := GetStatus(context.Background(), root, d)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.UpToDate {
		t.Error("newer tag should not be up to date")
	}
	if st.InstalledTag != "v0.2.0" || st.LatestTag != "v0.9.0" {
		t.Errorf("tags = %q %q", st.InstalledTag, st.LatestTag)
	}
	if len(st.Modified) != 1 || st.Modified[0] != ".claude/agents/api-designer.md" {
		t.Errorf("Modified = %v", st.Modified)
	}
	if len(st.Missing) != 1 || st.Missing[0] != ".claude/settings.json" {
		t.Errorf("Missing = %v", st.Missing)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	if _, err := GetStatus(context.Background(), initTestRepo(t), fakeDeps(t, bundleFixture())); err == nil {
		t.Error("expected not-installed error")
	}
}
