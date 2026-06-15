package prereq

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// diagOpts builds Opts whose detection is fully scripted for Diagnose tests.
// present maps command name → version string (Version returns it). daemonUp /
// composeOK / virtOK are the daemon, compose-plugin and (windows-only)
// virtualization signals. os defaults to darwin; pass "" to keep darwin.
func diagOpts(os string, present map[string]string, daemonUp, composeOK bool, virtOK *bool) Opts {
	if os == "" {
		os = "darwin"
	}
	d := Deps{
		OS:         os,
		HasCommand: func(name string) bool { _, ok := present[name]; return ok },
		Version:    func(name string) (string, error) { return present[name], nil },
		DaemonUp:   func(ctx context.Context) bool { return daemonUp },
		ComposeOK:  func(ctx context.Context) bool { return composeOK },
		Out:        &bytes.Buffer{},
	}
	if virtOK != nil {
		v := *virtOK
		d.VirtualizationOK = func(ctx context.Context) bool { return v }
	}
	return Opts{Deps: d}
}

// findCheck returns the CheckResult with the given name, failing the test if
// absent — diagnose results are keyed by name, so this is the lookup helper.
func findCheck(t *testing.T, checks []CheckResult, name string) CheckResult {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, checks)
	return CheckResult{}
}

func TestDiagnose_AllPresent_AllOK(t *testing.T) {
	o := diagOpts("darwin",
		map[string]string{"git": "2.40", "docker": "27", "node": "v20.20.2"},
		true /* daemon up */, true /* compose ok */, nil)
	checks := Diagnose(context.Background(), []Tool{Git, Docker, DockerCompose, Node}, o)

	for _, name := range []string{"git", "docker-engine", "docker-daemon", "docker-compose", "node"} {
		c := findCheck(t, checks, name)
		if c.Status != StatusOK {
			t.Errorf("%s: status = %q, want ok", name, c.Status)
		}
		if c.FixType != FixNone {
			t.Errorf("%s: fix_type = %q, want none for an ok check", name, c.FixType)
		}
		if c.Fix != "" {
			t.Errorf("%s: ok check should carry no fix, got %q", name, c.Fix)
		}
	}
}

func TestDiagnose_MissingGit_AutoFixable(t *testing.T) {
	// darwin git → xcode-select --install (auto-capable).
	o := diagOpts("darwin", map[string]string{"docker": "27", "node": "v20.20.2"}, true, true, nil)
	checks := Diagnose(context.Background(), []Tool{Git}, o)
	c := findCheck(t, checks, "git")
	if c.Status != StatusMissing {
		t.Fatalf("git status = %q, want missing", c.Status)
	}
	if c.FixType != FixAuto {
		t.Fatalf("missing git on darwin should be auto-fixable, got %q", c.FixType)
	}
	if c.Fix == "" {
		t.Fatal("missing git must carry a fix")
	}
}

func TestDiagnose_MissingNode_NamesRangeAndFix(t *testing.T) {
	o := diagOpts("darwin", map[string]string{"git": "2.40", "brew": ""}, true, true, nil)
	checks := Diagnose(context.Background(), []Tool{Node}, o)
	c := findCheck(t, checks, "node")
	if c.Status != StatusMissing {
		t.Fatalf("node status = %q, want missing", c.Status)
	}
	if !strings.Contains(c.Detail, "20") {
		t.Errorf("node detail should name the required range, got %q", c.Detail)
	}
	if !strings.Contains(c.Fix, "node@24") {
		t.Errorf("node fix should name the brew install, got %q", c.Fix)
	}
}

func TestDiagnose_WrongNodeVersion_Classified(t *testing.T) {
	// Below the >=20.9 floor → out of range (v22+ is now accepted).
	o := diagOpts("darwin", map[string]string{"node": "v18.20.0", "brew": ""}, true, true, nil)
	checks := Diagnose(context.Background(), []Tool{Node}, o)
	c := findCheck(t, checks, "node")
	if c.Status != StatusWrongVersion {
		t.Fatalf("node status = %q, want wrong_version", c.Status)
	}
	if !strings.Contains(c.Detail, "v18.20.0") || !strings.Contains(c.Detail, "out of range") {
		t.Errorf("wrong-version detail should name the found version + range, got %q", c.Detail)
	}
	if c.Fix == "" {
		t.Error("wrong-version node must carry a fix")
	}
}

func TestDiagnose_DockerDaemonDown_Blocked(t *testing.T) {
	o := diagOpts("darwin", map[string]string{"docker": "27"}, false /* daemon down */, true, nil)
	checks := Diagnose(context.Background(), []Tool{Docker}, o)

	engine := findCheck(t, checks, "docker-engine")
	if engine.Status != StatusOK {
		t.Errorf("engine present should be ok, got %q", engine.Status)
	}
	daemon := findCheck(t, checks, "docker-daemon")
	if daemon.Status != StatusBlocked {
		t.Fatalf("daemon-down should be blocked, got %q", daemon.Status)
	}
	if daemon.FixType != FixCommand {
		t.Errorf("daemon-down (non-windows) should be a command fix, got %q", daemon.FixType)
	}
	if !strings.Contains(daemon.Detail, "not running") {
		t.Errorf("daemon detail should say not running, got %q", daemon.Detail)
	}
}

func TestDiagnose_DockerMissing_DaemonBlockedOnEngine(t *testing.T) {
	// No docker binary at all: engine is missing, daemon is blocked-on-engine.
	o := diagOpts("darwin", map[string]string{"brew": ""}, false, false, nil)
	checks := Diagnose(context.Background(), []Tool{Docker}, o)

	engine := findCheck(t, checks, "docker-engine")
	if engine.Status != StatusMissing {
		t.Errorf("engine should be missing, got %q", engine.Status)
	}
	daemon := findCheck(t, checks, "docker-daemon")
	if daemon.Status != StatusBlocked {
		t.Errorf("daemon should be blocked when engine absent, got %q", daemon.Status)
	}
	if !strings.Contains(daemon.Detail, "engine isn't installed") {
		t.Errorf("daemon detail should point at the missing engine, got %q", daemon.Detail)
	}
}

func TestDiagnose_ComposePluginMissing_CommandFix(t *testing.T) {
	o := diagOpts("darwin", map[string]string{"docker": "27"}, true, false /* compose missing */, nil)
	checks := Diagnose(context.Background(), []Tool{DockerCompose}, o)
	c := findCheck(t, checks, "docker-compose")
	if c.Status != StatusMissing {
		t.Fatalf("compose status = %q, want missing", c.Status)
	}
	if c.FixType != FixCommand {
		t.Errorf("compose plugin is instruct-only, want command fix, got %q", c.FixType)
	}
	if !strings.Contains(c.Fix, "docker-compose") {
		t.Errorf("compose fix should name the install, got %q", c.Fix)
	}
}

func TestDiagnose_ComposeSkippedWhenDockerAbsent(t *testing.T) {
	// docker absent → the engine check reports it; compose should not double-flag.
	o := diagOpts("darwin", map[string]string{}, false, false, nil)
	o.ComposeOK = func(ctx context.Context) bool {
		t.Fatal("ComposeOK must not be consulted when docker is absent")
		return false
	}
	checks := Diagnose(context.Background(), []Tool{DockerCompose}, o)
	c := findCheck(t, checks, "docker-compose")
	if c.Status != StatusOK {
		t.Fatalf("compose should be ok-as-skipped when docker absent, got %q", c.Status)
	}
}

func TestDiagnose_LinuxComposeMissing_NamesPluginPackage(t *testing.T) {
	o := diagOpts("linux", map[string]string{"docker": "27", "apt-get": ""}, true, false, nil)
	checks := Diagnose(context.Background(), []Tool{DockerCompose}, o)
	c := findCheck(t, checks, "docker-compose")
	if !strings.Contains(c.Fix, "docker-compose-plugin") {
		t.Errorf("linux compose fix should name docker-compose-plugin, got %q", c.Fix)
	}
}

// The headline Windows case: docker binary present, daemon down, and CPU
// virtualization disabled in BIOS/UEFI → blocked + manual, with the BIOS reboot
// instruction and the browser-workspace fallback.
func TestDiagnose_WindowsVirtualizationOff_BlockedManual(t *testing.T) {
	virtOff := false
	o := diagOpts("windows", map[string]string{"docker": "27"}, false /* daemon down */, true, &virtOff)
	checks := Diagnose(context.Background(), []Tool{Docker}, o)

	daemon := findCheck(t, checks, "docker-daemon")
	if daemon.Status != StatusBlocked {
		t.Fatalf("daemon status = %q, want blocked", daemon.Status)
	}
	if daemon.FixType != FixManual {
		t.Fatalf("virtualization-off should be a MANUAL fix, got %q", daemon.FixType)
	}
	if !strings.Contains(daemon.Fix, "BIOS/UEFI") || !strings.Contains(daemon.Fix, "wsl --install") {
		t.Errorf("fix should walk through BIOS + wsl --install, got %q", daemon.Fix)
	}
	if !strings.Contains(strings.ToLower(daemon.Detail), "virtualization disabled") {
		t.Errorf("detail should name the disabled virtualization, got %q", daemon.Detail)
	}
	if !strings.Contains(strings.ToLower(daemon.Detail), "browser workspace") {
		t.Errorf("detail should mention the Keyto browser workspace fallback, got %q", daemon.Detail)
	}
}

// When virtualization IS enabled on Windows, a down daemon is the ordinary
// "start Docker" command case — not the manual BIOS path.
func TestDiagnose_WindowsVirtualizationOn_DaemonDown_CommandFix(t *testing.T) {
	virtOn := true
	o := diagOpts("windows", map[string]string{"docker": "27"}, false, true, &virtOn)
	checks := Diagnose(context.Background(), []Tool{Docker}, o)
	daemon := findCheck(t, checks, "docker-daemon")
	if daemon.Status != StatusBlocked || daemon.FixType != FixCommand {
		t.Fatalf("virtualization-on daemon-down should be blocked/command, got %q/%q", daemon.Status, daemon.FixType)
	}
	if strings.Contains(daemon.Fix, "BIOS") {
		t.Errorf("should not give the BIOS fix when virtualization is on, got %q", daemon.Fix)
	}
}

func TestInotifyCheck(t *testing.T) {
	// Low limit on linux → blocked + command fix naming the sysctl.
	o := Opts{Deps: Deps{OS: "linux"}}
	low := o.InotifyCheck(func(string) ([]byte, error) { return []byte("8192\n"), nil })
	if low.Status != StatusBlocked || low.FixType != FixCommand {
		t.Fatalf("low inotify → want blocked/command, got %q/%q", low.Status, low.FixType)
	}
	if !strings.Contains(low.Fix, "sysctl fs.inotify.max_user_watches") {
		t.Errorf("inotify fix should name the sysctl, got %q", low.Fix)
	}

	// Healthy limit → ok.
	healthy := o.InotifyCheck(func(string) ([]byte, error) { return []byte("524288"), nil })
	if healthy.Status != StatusOK {
		t.Errorf("healthy inotify → want ok, got %q", healthy.Status)
	}

	// Non-linux → ok, no fix.
	mac := (Opts{Deps: Deps{OS: "darwin"}}).InotifyCheck(func(string) ([]byte, error) {
		return nil, errors.New("unused")
	})
	if mac.Status != StatusOK || mac.Fix != "" {
		t.Errorf("non-linux inotify → want ok/no-fix, got %q/%q", mac.Status, mac.Fix)
	}
}

func TestCheckResult_IsBlocking(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{StatusOK, false},
		{StatusMissing, true},
		{StatusWrongVersion, true},
		{StatusBlocked, true},
	}
	for _, tc := range cases {
		if got := (CheckResult{Status: tc.status}).IsBlocking(); got != tc.want {
			t.Errorf("IsBlocking(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
