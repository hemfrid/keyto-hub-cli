package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
)

// diagnosticsSchemaVersion is the envelope version the Hub's
// /api/cli/diagnostics ingest expects around a doctor report.
const diagnosticsSchemaVersion = 1

// doctorTools is the prerequisite set `keyto doctor` diagnoses — the same set
// `keyto start` ensures. Docker expands to two checks (engine + daemon) inside
// prereq.Diagnose.
var doctorTools = []prereq.Tool{prereq.Git, prereq.Docker, prereq.DockerCompose, prereq.Node}

// doctorReport is the machine form emitted by `keyto doctor --json`. Its shape
// is the producer half of the Hub /api/cli/diagnostics ingest contract (the
// later --report chunk adds a `schema_version` envelope around the same fields).
type doctorReport struct {
	OK         bool                 `json:"ok"`
	OS         string               `json:"os"`
	OSVersion  string               `json:"os_version"`
	Arch       string               `json:"arch"`
	CLIVersion string               `json:"cli_version"`
	Checks     []prereq.CheckResult `json:"checks"`
}

// diagnosticsPayload is the body POSTed to the Hub's /api/cli/diagnostics
// ingest: the doctor report with the schema_version envelope field the contract
// requires. Embedding doctorReport keeps the per-field json tags (ok/os/…)
// identical to the --json output the Hub already understands.
type diagnosticsPayload struct {
	SchemaVersion int `json:"schema_version"`
	doctorReport
}

// osVersion is a seam over the real OS-version probe so runDoctor is testable
// without shelling out. Real impl: uname -r / sw_vers / cmd ver.
var osVersion = realOSVersion

// postDiagnostics is a seam over the Hub upload so runDoctor's report path can
// be tested without hitting the network. The real impl builds a hub.Client from
// the stored credential and POSTs the payload to /api/cli/diagnostics.
var postDiagnostics = func(ctx context.Context, creds *config.Creds, payload any) error {
	client := hub.Client{BaseURL: creds.HubURL, Credential: creds.Credential}
	return client.PostDiagnostics(ctx, payload)
}

// diagnose is a seam over the prereq diagnosis (detection + inotify) so
// runDoctor's flag handling, JSON shape and human summary can be tested with
// scripted CheckResults — no real exec/probe in the test path.
var diagnose = diagnoseAll

// runDoctor implements `keyto doctor`: a detect-only diagnosis of local
// prerequisites, classified by fixability and rendered for both humans
// (LLM-pasteable summary) and machines (--json). --fix runs the consent-gated
// installer for the auto/command-fixable ones. It writes human output to stdout
// and returns a non-nil error (non-zero exit) when any blocking issue remains.
func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	fix := fs.Bool("fix", false, "install the fixable prerequisites (consent-gated)")
	yes := fs.Bool("yes", false, "auto-confirm installs during --fix")
	report := fs.Bool("report", true, "upload the report to the Hub (default when authenticated)")
	noReport := fs.Bool("no-report", false, "do not upload the report to the Hub")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("doctor: parse flags: %w", err)
	}

	deps := realPrereqDeps(ctx)
	checks := diagnose(ctx, deps)

	if *fix {
		// Attempt to install the auto/command-fixable tools (consent-gated via
		// prereq.Ensure). manual items are never auto-run — re-diagnose after so
		// the summary reflects the post-fix state.
		if err := prereq.Ensure(ctx, doctorTools, prereq.Opts{Deps: deps, AutoYes: *yes}); err != nil {
			fmt.Fprintln(os.Stderr, "keyto doctor --fix:", err)
		}
		checks = diagnose(ctx, deps)
	}

	if *asJSON {
		if err := emitDoctorJSON(ctx, os.Stdout, checks); err != nil {
			return err
		}
	} else {
		printDoctorHuman(os.Stdout, checks)
	}

	// Upload the report to the Hub so it lands in /admin/diagnostics. Default-on
	// when authenticated; --no-report opts out. Always best-effort: a failed
	// upload never changes the exit code (that reflects the local diagnosis).
	maybeReportDoctor(ctx, checks, *report && !*noReport)

	if anyBlocking(checks) {
		return fmt.Errorf("doctor found blocking prerequisite issues — see above")
	}
	return nil
}

// maybeReportDoctor uploads the doctor report to the Hub when enabled and a
// usable credential is on disk. It is non-fatal in every branch: a disabled
// flag, missing/expired credential, or upload error each leaves the local
// diagnosis untouched — at most it prints a one-line note to stderr. The local
// report has already been written to stdout by the caller.
func maybeReportDoctor(ctx context.Context, checks []prereq.CheckResult, enabled bool) {
	if !enabled {
		return
	}
	creds, err := config.Load()
	if err != nil {
		// ErrNotAuthed (no credential) → skip silently; any other load error is
		// equally non-fatal for a best-effort report.
		if !errors.Is(err, config.ErrNotAuthed) {
			fmt.Fprintf(os.Stderr, "note: couldn't send setup report to the Hub: %v\n", err)
		}
		return
	}
	if creds == nil || creds.Expired() {
		return
	}

	payload := diagnosticsPayload{
		SchemaVersion: diagnosticsSchemaVersion,
		doctorReport:  buildDoctorReport(ctx, checks),
	}

	if err := postDiagnostics(ctx, creds, payload); err != nil {
		fmt.Fprintf(os.Stderr, "note: couldn't send setup report to the Hub: %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "• sent your setup report to the Hub")
}

// diagnoseAll runs the prereq diagnosis plus the Linux inotify advisory, in the
// order they should be reported.
func diagnoseAll(ctx context.Context, deps prereq.Deps) []prereq.CheckResult {
	o := prereq.Opts{Deps: deps}
	checks := prereq.Diagnose(ctx, doctorTools, o)
	checks = append(checks, o.InotifyCheck(os.ReadFile))
	return checks
}

func anyBlocking(checks []prereq.CheckResult) bool {
	for _, c := range checks {
		if c.IsBlocking() {
			return true
		}
	}
	return false
}

// buildDoctorReport assembles the doctorReport from the diagnosed checks plus
// the host/CLI metadata. It is the single source of truth for the report shape
// shared by --json output and the Hub upload. ctx is threaded so the OS-version
// probe shares the command's cancellation.
func buildDoctorReport(ctx context.Context, checks []prereq.CheckResult) doctorReport {
	return doctorReport{
		OK:         !anyBlocking(checks),
		OS:         runtime.GOOS,
		OSVersion:  osVersion(ctx),
		Arch:       runtime.GOARCH,
		CLIVersion: version,
		Checks:     checks,
	}
}

// emitDoctorJSON writes the machine form. ctx is threaded so the OS-version
// probe shares the command's cancellation.
func emitDoctorJSON(ctx context.Context, w io.Writer, checks []prereq.CheckResult) error {
	rep := buildDoctorReport(ctx, checks)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// printDoctorHuman writes the LLM-pasteable summary: one line per check, an
// indented fix line for each non-ok check, a fixability tally, and a "paste this
// into Claude/Codex" hint when anything is wrong.
func printDoctorHuman(w io.Writer, checks []prereq.CheckResult) {
	fmt.Fprintf(w, "keyto doctor — local prerequisite diagnosis (%s/%s)\n\n", runtime.GOOS, runtime.GOARCH)

	var auto, command, manual int
	for _, c := range checks {
		detail := c.Detail
		if detail != "" {
			detail = " " + detail
		}
		fmt.Fprintf(w, "[%s] %s%s\n", c.Status, c.Name, detail)
		if c.IsBlocking() && c.Fix != "" {
			fmt.Fprintf(w, "    fix [%s] %s\n", c.FixType, c.Fix)
		}
		if c.IsBlocking() {
			switch c.FixType {
			case prereq.FixAuto:
				auto++
			case prereq.FixCommand:
				command++
			case prereq.FixManual:
				manual++
			}
		}
	}

	fmt.Fprintf(w, "\n%d auto · %d command · %d manual\n", auto, command, manual)

	if anyBlocking(checks) {
		fmt.Fprintln(w, "\nPaste the lines above into Claude or Codex and ask it to walk you through the fixes,")
		fmt.Fprintln(w, "or run `keyto doctor --fix` to install the auto-fixable ones.")
	} else {
		fmt.Fprintln(w, "\nAll prerequisites look good — you're ready to `keyto start`.")
	}
}

// realOSVersion probes the OS version string per platform. Best-effort: any
// failure yields "" (the JSON field is then empty rather than an error).
func realOSVersion(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "windows":
		out, err := exec.CommandContext(ctx, "cmd", "/c", "ver").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	default: // linux + unix
		out, err := exec.CommandContext(ctx, "uname", "-r").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}
