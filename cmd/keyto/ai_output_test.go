package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/ai"
)

// Bundle file paths come from the Hub-relayed manifest and from the pin file.
// The tarball check refuses a path that escapes the repo, not one that carries
// control characters, so the report lines strip them.
const hostilePath = ".claude/skills/x\x1b]0;pwned\x07\x1b[2J‮.md\n  removed: everything"

func assertReportSafe(t *testing.T, out string) {
	t.Helper()
	if strings.ContainsAny(out, "\x1b\x07‮\r") {
		t.Errorf("the report carries a control character: %q", out)
	}
	if strings.Contains(out, "\n  removed: everything") {
		t.Errorf("a path forged a report line: %q", out)
	}
	if !strings.Contains(out, ".claude/skills/x.md   removed: everything") {
		t.Errorf("the readable path should remain on one line: %q", out)
	}
}

func TestAIInitReport_PrintsPathsWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	writeAIInit(&out, &ai.InitResult{Tag: "v1.2.0", Written: []string{"a"}, Skipped: []string{hostilePath}})
	assertReportSafe(t, out.String())
	if !strings.Contains(out.String(), "  kept: .claude/skills/x.md") {
		t.Errorf("unexpected report: %q", out.String())
	}
}

func TestAIUpdateReport_PrintsPathsWithoutControlSequences(t *testing.T) {
	res := &ai.UpdateResult{
		FromTag: "v1.1.0", Tag: "v1.2.0",
		Updated: []string{hostilePath}, Added: []string{hostilePath}, SkippedModified: []string{hostilePath},
		SkippedExisting: []string{hostilePath}, MissingLocal: []string{hostilePath},
		RemovedUpstream: []string{hostilePath}, KeptModified: []string{hostilePath},
	}
	var out bytes.Buffer
	writeAIUpdate(&out, res)
	assertReportSafe(t, out.String())
	if got := strings.Count(out.String(), ".claude/skills/x.md"); got != 7 {
		t.Errorf("want all 7 lists reported, got %d: %q", got, out.String())
	}
}

func TestAIStatusReport_PrintsPathsWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	writeAIStatus(&out, &ai.Status{InstalledTag: "v1.1.0", LatestTag: "v1.2.0", Modified: []string{hostilePath}, Missing: []string{hostilePath}})
	assertReportSafe(t, out.String())
	for _, want := range []string{"  locally modified: .claude/skills/x.md", "  locally deleted:  .claude/skills/x.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("want %q in %q", want, out.String())
		}
	}
}

func TestAIReports_AnOrdinaryPathIsUnchanged(t *testing.T) {
	var out bytes.Buffer
	writeAIStatus(&out, &ai.Status{InstalledTag: "v1.2.0", UpToDate: true, Modified: []string{".claude/rules/åäö tests.md"}})
	if want := "Bundle v1.2.0 — up to date.\n  locally modified: .claude/rules/åäö tests.md\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
