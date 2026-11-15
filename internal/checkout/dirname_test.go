package checkout_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// The project name comes from the Hub and becomes the default clone folder
// (<cwd>/<name>). A name that is not a plain folder name is refused before
// anything is cloned or any directory is offered.

func runWithName(t *testing.T, name, arg, input string) (cloned []string, out string, err error) {
	t.Helper()
	var buf bytes.Buffer
	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList([]hub.Project{{Name: name, Org: "hemfrid", Repo: "acme-web", Role: "owner"}}),
		Clone: func(repoURL, dir string) error {
			cloned = append(cloned, dir)
			return nil
		},
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(input),
		Out:         &buf,
	}
	_, err = checkout.Run(context.Background(), arg, d)
	return cloned, buf.String(), err
}

func TestCheckout_RefusesAProjectNameThatIsNotAPlainFolderName(t *testing.T) {
	names := []string{
		"../x", "..", ".", "a/b", `a\b`, "/etc/x", `C:\x`, "C:x", "a/../b", "x/", `x\`,
		"", " ", " x", "x ", "x.", "-x", ".git", ".x",
		"a\x00b", "a\nb", "a\x1b[2Jb", "a\u202eb", "naïve", "a b",
		"CON", "nul", "com1", "LPT9", "con.txt",
		strings.Repeat("a", 101),
	}
	for _, name := range names {
		// Picked from the list (the user typed nothing but "1").
		cloned, out, err := runWithName(t, name, "", "1\n\n")
		if err == nil {
			t.Errorf("name %q (picker): want an error, got none", name)
		}
		if len(cloned) != 0 {
			t.Errorf("name %q (picker): cloned into %v", name, cloned)
		}
		if strings.Contains(out, "Checkout directory [") {
			t.Errorf("name %q (picker): a directory was offered: %q", name, out)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\x1b\x00\n\u202e") {
			t.Errorf("name %q: the error carries a control character: %q", name, err.Error())
		}
		// Named on the command line: the Hub's entry has to match it exactly.
		if name == "" {
			continue
		}
		cloned, _, err = runWithName(t, name, name, "\n")
		if err == nil || len(cloned) != 0 {
			t.Errorf("name %q (argument): want an error and no clone, got err=%v cloned=%v", name, err, cloned)
		}
	}
}

func TestCheckout_AcceptsOrdinaryProjectNames(t *testing.T) {
	for _, name := range []string{
		"acme-web", "a1b", "keyto-hub-workers-template", "minerva-web-planner",
		"app-401", "x", "Legacy_Project.v2", strings.Repeat("a", 58),
	} {
		cloned, _, err := runWithName(t, name, "", "1\n\n")
		if err != nil {
			t.Errorf("name %q: unexpected error: %v", name, err)
			continue
		}
		if len(cloned) != 1 || cloned[0] != "/tmp/cwd/"+name {
			t.Errorf("name %q: cloned into %v, want /tmp/cwd/%s", name, cloned, name)
		}
	}
}

func TestCheckout_ADirectoryTheUserTypesIsTheirOwnChoice(t *testing.T) {
	cloned, _, err := runWithName(t, "acme-web", "", "1\n../elsewhere\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cloned) != 1 || !strings.HasSuffix(cloned[0], "elsewhere") {
		t.Errorf("cloned into %v", cloned)
	}
}
