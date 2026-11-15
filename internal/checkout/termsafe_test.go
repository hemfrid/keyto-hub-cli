package checkout_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// The Hub's project list is server text. A name, org, repo or role that
// carries terminal control sequences must reach the terminal without them.
var hostileProject = hub.Project{
	Name: "evil\x1b]0;pwned\x07\x1b[2J‮name",
	Org:  "hem\x1b[31mfrid",
	Repo: "re\x1b]52;c;AAAA\x07po",
	Role: "own\x1b[2Jer\nfake line",
}

const controlChars = "\x1b\x07‮\r"

func assertTerminalSafe(t *testing.T, what, text string) {
	t.Helper()
	if strings.ContainsAny(text, controlChars) {
		t.Errorf("%s carries a control character: %q", what, text)
	}
	if strings.Contains(text, "\nfake line") {
		t.Errorf("%s lets Hub text start a new line: %q", what, text)
	}
}

func TestPicker_PrintsHubTextWithoutControlSequences_AndRefusesTheName(t *testing.T) {
	var out bytes.Buffer
	cloned := false
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{hostileProject}),
		Clone:       func(repoURL, dir string) error { cloned = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("1\n\n"), // pick it
		Out:         &out,
	}
	_, err := checkout.Run(context.Background(), "", d)
	// The list is shown without control sequences; the name itself is then
	// refused, because it is not a plain folder name.
	if err == nil || cloned {
		t.Fatalf("want the name refused and nothing cloned, got err=%v cloned=%v", err, cloned)
	}
	assertTerminalSafe(t, "the picker output", out.String())
	assertTerminalSafe(t, "the refusal", err.Error())
	for _, want := range []string{"evilname", "(owner fake line)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the readable text %q should remain; got %q", want, out.String())
		}
	}
}

// A project whose name is fine but whose org, repo and role are not.
var hostileOrgRepo = hub.Project{Name: "acme-web", Org: hostileProject.Org, Repo: hostileProject.Repo, Role: hostileProject.Role}

func TestClone_PrintsTheResultWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{hostileOrgRepo}),
		Clone:       func(repoURL, dir string) error { return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("1\n\n"), // pick it, accept the default directory
		Out:         &out,
	}
	if _, err := checkout.Run(context.Background(), "", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTerminalSafe(t, "the picker and clone output", out.String())
	for _, want := range []string{"1. acme-web (owner fake line)", "Checkout directory [/tmp/cwd/acme-web]", "Cloned acme-web into /tmp/cwd/acme-web"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the readable text %q should remain; got %q", want, out.String())
		}
	}
}

func TestMarkerPrompt_PrintsTheMarkerNameWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	marker := &project.Marker{Name: hostileProject.Name, Org: "hemfrid", Repo: "x", HubURL: "https://hub.example.com"}
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(marker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("y\n"),
		Out:         &out,
	}
	if _, err := checkout.Run(context.Background(), "", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTerminalSafe(t, "the marker prompt", out.String())
	if !strings.Contains(out.String(), "Work in evilname?") {
		t.Errorf("the readable name should remain; got %q", out.String())
	}
}

func TestAdopt_PrintsOrgRepoAndNameWithoutControlSequences(t *testing.T) {
	var out bytes.Buffer
	cwd := t.TempDir()
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{hostileProject}),
		Clone:       func(repoURL, dir string) error { return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		OriginURL: func(dir string) (string, error) {
			return "https://hub.example.com/git/" + hostileProject.Org + "/" + hostileProject.Repo + ".git", nil
		},
		Toplevel: func(dir string) (string, error) { return cwd, nil },
		Cwd:      cwd,
		In:       strings.NewReader("1\ny\n"), // pick it, adopt the folder
		Out:      &out,
	}
	if _, err := checkout.Run(context.Background(), "", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTerminalSafe(t, "the adopt output", out.String())
	for _, want := range []string{"already a checkout of hemfrid/repo.", "Adopted existing checkout of evilname in"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the readable text %q should remain; got %q", want, out.String())
		}
	}
}

func TestCloneFailure_ErrorNamesTheRepoWithoutControlSequences(t *testing.T) {
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{hostileOrgRepo}),
		Clone:       func(repoURL, dir string) error { return errors.New("exit status 128") },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("1\n\n"),
		Out:         &bytes.Buffer{},
	}
	_, err := checkout.Run(context.Background(), "", d)
	if err == nil {
		t.Fatal("expected the clone error")
	}
	assertTerminalSafe(t, "the clone error", err.Error())
	if !strings.Contains(err.Error(), "https://hub.example.com/git/hemfrid/repo.git") || !strings.Contains(err.Error(), "exit status 128") {
		t.Errorf("the readable URL and cause should remain; got %q", err.Error())
	}
}
