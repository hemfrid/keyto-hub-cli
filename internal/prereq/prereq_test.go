package prereq

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// fakeEnv builds Deps whose probes/installs are scripted.
func fakeEnv(present map[string]string, daemonUp bool, accept bool, runErr error) (*Deps, *[]string) {
	var ran []string
	d := &Deps{
		OS:         "darwin",
		HasCommand: func(name string) bool { _, ok := present[name]; return ok },
		Version:    func(name string) (string, error) { return present[name], nil },
		DaemonUp:   func(ctx context.Context) bool { return daemonUp },
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
