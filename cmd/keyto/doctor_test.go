package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
)

// stubDiagnose swaps the diagnose seam for one returning the supplied checks so
// runDoctor's rendering/flag handling is tested without any real exec/probe. It
// also pins osVersion to a constant so the JSON shape is deterministic.
func stubDiagnose(t *testing.T, checks []prereq.CheckResult) {
	t.Helper()
	od, ov := diagnose, osVersion
	diagnose = func(ctx context.Context, deps prereq.Deps) []prereq.CheckResult { return checks }
	osVersion = func(ctx context.Context) string { return "test-os-version" }
	t.Cleanup(func() { diagnose, osVersion = od, ov })
}

func okChecks() []prereq.CheckResult {
	return []prereq.CheckResult{
		{Name: "git", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "git version 2.40"},
		{Name: "docker-engine", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "Docker 27"},
		{Name: "docker-daemon", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "running"},
		{Name: "docker-compose", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "plugin present"},
		{Name: "node", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "v20.20.2"},
		{Name: "inotify", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "not applicable"},
	}
}

// mixedChecks has one of each fixability tier so the tally + fix lines can be
// asserted: node auto, docker-compose command, docker-daemon manual.
func mixedChecks() []prereq.CheckResult {
	return []prereq.CheckResult{
		{Name: "git", Status: prereq.StatusOK, FixType: prereq.FixNone, Detail: "git version 2.40"},
		{Name: "node", Status: prereq.StatusMissing, FixType: prereq.FixAuto, Fix: "brew install node@20", Detail: "not installed"},
		{Name: "docker-compose", Status: prereq.StatusMissing, FixType: prereq.FixCommand, Fix: "brew install docker-compose", Detail: "plugin missing"},
		{Name: "docker-daemon", Status: prereq.StatusBlocked, FixType: prereq.FixManual, Fix: "reboot → BIOS/UEFI → enable Virtualization", Detail: "virtualization disabled"},
	}
}

func TestDispatch_DoctorRoutesToRunDoctor(t *testing.T) {
	stubDiagnose(t, okChecks())
	// dispatch("doctor") goes through runDoctor → printDoctorHuman (stdout). With
	// all-ok checks it must return nil (clean exit).
	if err := dispatch([]string{"doctor"}); err != nil {
		t.Fatalf("dispatch(doctor) with all-ok checks returned error: %v", err)
	}
}

func TestRunDoctor_BlockingIssues_ReturnsError(t *testing.T) {
	stubDiagnose(t, mixedChecks())
	if err := runDoctor(context.Background(), nil); err == nil {
		t.Fatal("expected non-nil error (non-zero exit) when a blocking issue is present")
	}
}

func TestRunDoctor_AllOK_NoError(t *testing.T) {
	stubDiagnose(t, okChecks())
	if err := runDoctor(context.Background(), nil); err != nil {
		t.Fatalf("all-ok doctor should exit 0 (nil error), got: %v", err)
	}
}

func TestRunDoctor_JSONShape(t *testing.T) {
	stubDiagnose(t, mixedChecks())
	var buf bytes.Buffer
	if err := emitDoctorJSON(context.Background(), &buf, mixedChecks()); err != nil {
		t.Fatalf("emitDoctorJSON error: %v", err)
	}

	var rep doctorReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("doctor --json output is not valid JSON: %v\n%s", err, buf.String())
	}

	if rep.OK {
		t.Error("ok should be false when a blocking check is present")
	}
	if rep.OS == "" || rep.Arch == "" {
		t.Errorf("os/arch must be populated, got os=%q arch=%q", rep.OS, rep.Arch)
	}
	if rep.OSVersion != "test-os-version" {
		t.Errorf("os_version = %q, want the stubbed value", rep.OSVersion)
	}
	if rep.CLIVersion != version {
		t.Errorf("cli_version = %q, want %q", rep.CLIVersion, version)
	}
	if len(rep.Checks) != len(mixedChecks()) {
		t.Fatalf("checks length = %d, want %d", len(rep.Checks), len(mixedChecks()))
	}
	// The per-check contract fields the Hub ingest keys off must round-trip.
	node := rep.Checks[1]
	if node.Name != "node" || node.Status != prereq.StatusMissing || node.FixType != prereq.FixAuto {
		t.Errorf("node check did not round-trip: %+v", node)
	}
	if node.Fix == "" {
		t.Error("node fix should round-trip non-empty")
	}

	// Confirm the raw JSON keys match the contract (snake_case), since the Hub
	// ingest reads these literal field names.
	raw := buf.String()
	for _, key := range []string{`"ok"`, `"os"`, `"os_version"`, `"arch"`, `"cli_version"`, `"checks"`, `"name"`, `"status"`, `"fix_type"`, `"fix"`, `"detail"`} {
		if !strings.Contains(raw, key) {
			t.Errorf("JSON output missing contract key %s\n%s", key, raw)
		}
	}
}

func TestRunDoctor_JSON_AllOK_OKTrue(t *testing.T) {
	var buf bytes.Buffer
	stubDiagnose(t, okChecks())
	if err := emitDoctorJSON(context.Background(), &buf, okChecks()); err != nil {
		t.Fatalf("emitDoctorJSON error: %v", err)
	}
	var rep doctorReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !rep.OK {
		t.Error("ok should be true when every check passes")
	}
}

func TestPrintDoctorHuman_FixLinesAndTally(t *testing.T) {
	var buf bytes.Buffer
	printDoctorHuman(&buf, mixedChecks())
	out := buf.String()

	// One status line per check.
	for _, name := range []string{"git", "node", "docker-compose", "docker-daemon"} {
		if !strings.Contains(out, name) {
			t.Errorf("summary missing a line for %q\n%s", name, out)
		}
	}
	// Each non-ok check renders an indented fix line tagged with its tier.
	for _, want := range []string{
		"fix [auto] brew install node@20",
		"fix [command] brew install docker-compose",
		"fix [manual] reboot → BIOS/UEFI → enable Virtualization",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing fix line %q\n%s", want, out)
		}
	}
	// The fixability tally.
	if !strings.Contains(out, "1 auto · 1 command · 1 manual") {
		t.Errorf("summary missing the expected tally\n%s", out)
	}
	// The Claude/Codex paste hint when issues exist.
	if !strings.Contains(out, "Claude") || !strings.Contains(out, "Codex") {
		t.Errorf("summary should hint pasting into Claude/Codex\n%s", out)
	}
}

func TestPrintDoctorHuman_AllOK_NoFixLines(t *testing.T) {
	var buf bytes.Buffer
	printDoctorHuman(&buf, okChecks())
	out := buf.String()
	if strings.Contains(out, "fix [") {
		t.Errorf("all-ok summary must not render any fix line\n%s", out)
	}
	if !strings.Contains(out, "0 auto · 0 command · 0 manual") {
		t.Errorf("all-ok tally should be all zero\n%s", out)
	}
	if !strings.Contains(out, "ready to `keyto start`") {
		t.Errorf("all-ok summary should give the ready message\n%s", out)
	}
}
