package prereq

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeEnv builds Deps whose probes/installs are scripted. ComposeOK reports the
// Compose v2 plugin present whenever docker is present (tests that exercise the
// missing-plugin path override it explicitly).
func fakeEnv(present map[string]string, daemonUp bool, accept bool, runErr error) (*Deps, *[]string) {
	var ran []string
	d := &Deps{
		OS:         "darwin",
		HasCommand: func(name string) bool { _, ok := present[name]; return ok },
		Version:    func(name string) (string, error) { return present[name], nil },
		DaemonUp:   func(ctx context.Context) bool { return daemonUp },
		ComposeOK:  func(ctx context.Context) bool { _, ok := present["docker"]; return ok },
		Prompt:     func(string) bool { return accept },
		Run: func(ctx context.Context, name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			if name == "brew" || name == "colima" { // post-install, mark present
				present["docker"], present["git"], present["node"] = "v20.20.2", "2.40", "v20.20.2"
			}
			return runErr
		},
	}
	return d, &ran
}

func TestEnsure_AllPresent_NoInstall(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"git": "2.40", "docker": "27", "node": "v20.20.2"}, true, false, nil)
	d.Out = &bytes.Buffer{}
	if err := Ensure(context.Background(), []Tool{Git, Docker, Node}, Opts{Deps: *d}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("expected no installs, ran: %v", *ran)
	}
}

func TestEnsure_WrongNodeVersion_GuidesAndErrors(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"git": "2.40", "docker": "27", "node": "v22.3.0"}, true, true, nil)
	d.Out = &bytes.Buffer{}
	err := Ensure(context.Background(), []Tool{Node}, Opts{Deps: *d})
	if err == nil {
		t.Fatal("expected wrong-version error")
	}
	if len(*ran) != 0 {
		t.Fatalf("must NOT install/downgrade node, ran: %v", *ran)
	}
	if !strings.Contains(err.Error(), "20") {
		t.Fatalf("error should name the required range: %v", err)
	}
}

func TestEnsure_MissingDocker_InstallAccepted(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"git": "2.40", "node": "v20.20.2", "brew": ""}, false, true, nil)
	d.Out = &bytes.Buffer{}
	if err := Ensure(context.Background(), []Tool{Docker}, Opts{Deps: *d}); err != nil {
		t.Fatalf("unexpected error after accepted install: %v", err)
	}
	if len(*ran) == 0 {
		t.Fatal("expected an install command to run")
	}
}

func TestEnsure_MissingDocker_InstallDeclined_Errors(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"git": "2.40", "node": "v20.20.2", "brew": ""}, false, false, nil)
	d.Out = &bytes.Buffer{}
	err := Ensure(context.Background(), []Tool{Docker}, Opts{Deps: *d})
	if err == nil {
		t.Fatal("expected error when install declined")
	}
	if len(*ran) != 0 {
		t.Fatalf("nothing should run when declined, ran: %v", *ran)
	}
	if !strings.Contains(err.Error(), "brew install") {
		t.Fatalf("declined error must print the manual command: %v", err)
	}
}

func TestEnsure_DockerBinaryPresentButDaemonDown_Errors(t *testing.T) {
	d, _ := fakeEnv(map[string]string{"git": "2.40", "docker": "27", "node": "v20.20.2"}, false, false, nil)
	d.Out = &bytes.Buffer{}
	err := Ensure(context.Background(), []Tool{Docker}, Opts{Deps: *d})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("expected daemon-down error, got: %v", err)
	}
}

func TestEnsure_NoPackageManager_InstructsOnly(t *testing.T) {
	d, ran := fakeEnv(map[string]string{}, false, true, nil) // no brew, nothing present
	d.OS = "linux"
	d.Out = &bytes.Buffer{}
	err := Ensure(context.Background(), []Tool{Git}, Opts{Deps: *d})
	if err == nil {
		t.Fatal("expected error when no package manager + missing tool")
	}
	if len(*ran) != 0 {
		t.Fatalf("must not run installs without a package manager, ran: %v", *ran)
	}
}

// daemonDownEnv builds Deps where docker is installed but the daemon is down;
// `colima start` flips it up. up is read by reference so the post-start
// DaemonUp re-check observes the change.
func daemonDownEnv(colimaPresent, accept bool) (*Deps, *[]string, *bool) {
	present := map[string]string{"docker": "27"}
	if colimaPresent {
		present["colima"] = ""
	}
	var ran []string
	up := false
	d := &Deps{
		OS:         "darwin",
		HasCommand: func(name string) bool { _, ok := present[name]; return ok },
		Version:    func(name string) (string, error) { return present[name], nil },
		DaemonUp:   func(ctx context.Context) bool { return up },
		ComposeOK:  func(ctx context.Context) bool { return true },
		Prompt:     func(string) bool { return accept },
		Run: func(ctx context.Context, name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			if name == "colima" {
				up = true // `colima start` brings the daemon up
			}
			return nil
		},
		Out: &bytes.Buffer{},
	}
	return d, &ran, &up
}

func TestEnsure_DaemonDown_ColimaStart_Accepted(t *testing.T) {
	d, ran, _ := daemonDownEnv(true, true)
	if err := Ensure(context.Background(), []Tool{Docker}, Opts{Deps: *d}); err != nil {
		t.Fatalf("unexpected error after accepted colima start: %v", err)
	}
	found := false
	for _, c := range *ran {
		if strings.HasPrefix(c, "colima start") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected `colima start` to run; ran: %v", *ran)
	}
}

func TestEnsure_DaemonDown_ColimaStart_Declined_Errors(t *testing.T) {
	d, ran, _ := daemonDownEnv(true, false) // colima present but user declines
	err := Ensure(context.Background(), []Tool{Docker}, Opts{Deps: *d})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("expected daemon-down error when start declined, got: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("must NOT start colima without consent, ran: %v", *ran)
	}
}

func TestEnsure_ComposePluginPresent_OK(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"docker": "27"}, true, false, nil)
	d.Out = &bytes.Buffer{}
	d.ComposeOK = func(ctx context.Context) bool { return true }
	if err := Ensure(context.Background(), []Tool{DockerCompose}, Opts{Deps: *d}); err != nil {
		t.Fatalf("compose plugin present should pass: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("nothing should run for a present compose plugin, ran: %v", *ran)
	}
}

func TestEnsure_ComposePluginMissing_Errors(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"docker": "27"}, true, false, nil)
	d.Out = &bytes.Buffer{}
	d.ComposeOK = func(ctx context.Context) bool { return false }
	err := Ensure(context.Background(), []Tool{DockerCompose}, Opts{Deps: *d})
	if err == nil {
		t.Fatal("expected an error when the compose plugin is missing")
	}
	if !strings.Contains(err.Error(), "Compose") || !strings.Contains(err.Error(), "docker-compose") {
		t.Fatalf("error should name the compose plugin and the macOS fix: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("compose check must not run installs, ran: %v", *ran)
	}
}

func TestEnsure_ComposePluginMissing_Linux_NamesPluginPackage(t *testing.T) {
	d, _ := fakeEnv(map[string]string{"docker": "27", "apt-get": ""}, true, false, nil)
	d.OS = "linux"
	d.Out = &bytes.Buffer{}
	d.ComposeOK = func(ctx context.Context) bool { return false }
	err := Ensure(context.Background(), []Tool{DockerCompose}, Opts{Deps: *d})
	if err == nil || !strings.Contains(err.Error(), "docker-compose-plugin") {
		t.Fatalf("linux compose guidance should name docker-compose-plugin, got: %v", err)
	}
}

func TestEnsure_ComposePluginMissing_NoDocker_Skips(t *testing.T) {
	// When docker itself is absent, the Docker tool reports it; the compose
	// check should not double-report (and ComposeOK must not even be consulted).
	d, ran := fakeEnv(map[string]string{}, false, false, nil)
	d.Out = &bytes.Buffer{}
	d.ComposeOK = func(ctx context.Context) bool {
		t.Fatal("ComposeOK must not be called when docker is absent")
		return false
	}
	if err := Ensure(context.Background(), []Tool{DockerCompose}, Opts{Deps: *d}); err != nil {
		t.Fatalf("compose check should skip silently when docker is absent: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("nothing should run, ran: %v", *ran)
	}
}

func TestEnsure_DaemonDown_ComposeCheckStillPasses(t *testing.T) {
	// The compose plugin is installable independent of the daemon running —
	// `docker compose version` answers without a daemon. So a daemon-down env
	// with the plugin present must NOT fail the DockerCompose check.
	d, ran := fakeEnv(map[string]string{"docker": "27"}, false /* daemon down */, false, nil)
	d.Out = &bytes.Buffer{}
	d.ComposeOK = func(ctx context.Context) bool { return true }
	if err := Ensure(context.Background(), []Tool{DockerCompose}, Opts{Deps: *d}); err != nil {
		t.Fatalf("daemon-down must not fail the compose-plugin check: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("nothing should run, ran: %v", *ran)
	}
}

func TestInstallMethod_LinuxNode_UsesNodeSourceV20(t *testing.T) {
	d, _ := fakeEnv(map[string]string{"apt-get": ""}, false, true, nil)
	d.OS = "linux"
	o := Opts{Deps: *d}
	desc, cmd, auto := o.installMethod(Node)
	if !auto || cmd == nil {
		t.Fatalf("linux node install should be auto-capable, got auto=%v cmd=%v", auto, cmd)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "deb.nodesource.com/setup_20.x") {
		t.Fatalf("apt node install must use NodeSource v20, got: %s", joined)
	}
	if !strings.Contains(joined, "apt-get install -y nodejs") {
		t.Fatalf("apt node install must install nodejs after the setup script, got: %s", joined)
	}
	if !strings.Contains(desc, "NodeSource") {
		t.Fatalf("desc should mention NodeSource, got: %s", desc)
	}
	// It must NOT be the stale `apt-get install nodejs` with no NodeSource repo.
	if joined == "sudo apt-get install -y nodejs" {
		t.Fatalf("must not use the stale distro nodejs install: %s", joined)
	}
}

func TestInstallMethod_LinuxNode_Dnf_UsesNodeSourceV20(t *testing.T) {
	d, _ := fakeEnv(map[string]string{"dnf": ""}, false, true, nil)
	d.OS = "linux"
	o := Opts{Deps: *d}
	_, cmd, auto := o.installMethod(Node)
	if !auto || cmd == nil {
		t.Fatal("dnf node install should be auto-capable")
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "rpm.nodesource.com/setup_20.x") || !strings.Contains(joined, "dnf install -y nodejs") {
		t.Fatalf("dnf node install must use NodeSource v20 rpm setup, got: %s", joined)
	}
}

func TestInotifyWarning(t *testing.T) {
	o := Opts{Deps: Deps{OS: "linux"}}

	// Below threshold → warning naming the sysctl fix.
	w := o.InotifyWarning(func(string) ([]byte, error) { return []byte("8192\n"), nil })
	if w == "" {
		t.Fatal("expected a warning for a low watch limit")
	}
	if !strings.Contains(w, "fs.inotify.max_user_watches") || !strings.Contains(w, "sysctl") {
		t.Fatalf("warning should name the sysctl fix, got: %q", w)
	}

	// At/above threshold → no warning.
	if got := o.InotifyWarning(func(string) ([]byte, error) { return []byte("524288"), nil }); got != "" {
		t.Fatalf("healthy limit should produce no warning, got: %q", got)
	}
	if got := o.InotifyWarning(func(string) ([]byte, error) { return []byte("65536"), nil }); got != "" {
		t.Fatalf("limit exactly at the floor should produce no warning, got: %q", got)
	}

	// Unreadable / unparseable → silent (no nag).
	if got := o.InotifyWarning(func(string) ([]byte, error) { return nil, errors.New("nope") }); got != "" {
		t.Fatalf("unreadable limit should be silent, got: %q", got)
	}
	if got := o.InotifyWarning(func(string) ([]byte, error) { return []byte("garbage"), nil }); got != "" {
		t.Fatalf("unparseable limit should be silent, got: %q", got)
	}
}

func TestInotifyWarning_NonLinux_Silent(t *testing.T) {
	o := Opts{Deps: Deps{OS: "darwin"}}
	if got := o.InotifyWarning(func(string) ([]byte, error) { return []byte("8192"), nil }); got != "" {
		t.Fatalf("non-linux should never warn, got: %q", got)
	}
}
