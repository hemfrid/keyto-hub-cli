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

// The marker (.keyto/project.json) is a file committed in the repo. When a
// checkout is re-wired in place, a marker's org and repo used to become the
// git remote on the signed-in Hub. A marker that names a repo the user did not
// mean would send their pushes there.
//
// So the marker only says WHICH of the user's projects this folder is: its
// org/repo has to be one of the user's own Hub projects (compared without
// case). From there on the Hub's own name, org and repo are used: in the
// prompt, in the wiring, and written back to the marker.

// rewire records what a run wired and wrote.
type rewire struct {
	wired   []project.Marker
	written []project.Marker
	out     bytes.Buffer
}

func (r *rewire) deps(marker *project.Marker, list func(context.Context) ([]hub.Project, error), input string) checkout.Deps {
	return checkout.Deps{
		Creds: makeCreds(),
		List:  list,
		Clone: func(repoURL, dir string) error { return nil },
		Wire: func(dir string, m *project.Marker, email, name string) error {
			r.wired = append(r.wired, *m)
			return nil
		},
		ReadMarker: fakeReadMarker(marker),
		WriteMarker: func(dir string, m *project.Marker) error {
			r.written = append(r.written, *m)
			return nil
		},
		Cwd: "/tmp/acme-web",
		In:  strings.NewReader(input),
		Out: &r.out,
	}
}

const hubURL = "https://hub.example.com"

func marker(name, org, repo string) *project.Marker {
	return &project.Marker{Name: name, Org: org, Repo: repo, HubURL: hubURL}
}

func TestRewire_RefusesAMarkerWhoseRepoIsNotOneOfTheUsersProjects(t *testing.T) {
	markers := []*project.Marker{
		marker("acme-web", "hemfrid", "payroll"),       // a repo the user is not a member of
		marker("acme-web", "someone-else", "acme-web"), // another org
	}
	for _, m := range markers {
		for _, mode := range []struct{ name, arg, input string }{
			{"keyto checkout <name> inside the checkout", "acme-web", ""},
			{"keyto checkout, in a folder with that marker", "", "y\n"},
		} {
			var r rewire
			_, err := checkout.Run(context.Background(), mode.arg, r.deps(m, fakeList(twoProjects()), mode.input))
			if err == nil || len(r.wired) != 0 || len(r.written) != 0 {
				t.Errorf("%s/%s (%s): want an error and nothing wired or written, got err=%v wired=%d written=%d", m.Org, m.Repo, mode.name, err, len(r.wired), len(r.written))
				continue
			}
			if strings.Contains(r.out.String(), "ready") || strings.Contains(r.out.String(), "Work in") {
				t.Errorf("%s/%s (%s): prompted or reported ready: %q", m.Org, m.Repo, mode.name, r.out.String())
			}
			for _, want := range []string{m.Org + "/" + m.Repo, ".keyto/project.json"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s/%s (%s): want %q in %q", m.Org, m.Repo, mode.name, want, err.Error())
				}
			}
		}
	}
}

func TestRewire_AnUnchangedMarkerIsWiredAsItIsAndNotRewritten(t *testing.T) {
	for _, mode := range []struct{ arg, input string }{{"acme-web", ""}, {"", "y\n"}} {
		var r rewire
		_, err := checkout.Run(context.Background(), mode.arg, r.deps(marker("acme-web", "hemfrid", "acme-web"), fakeList(twoProjects()), mode.input))
		if err != nil {
			t.Fatalf("arg %q: unexpected error: %v", mode.arg, err)
		}
		if len(r.wired) != 1 || r.wired[0] != *marker("acme-web", "hemfrid", "acme-web") {
			t.Errorf("arg %q: wired %+v", mode.arg, r.wired)
		}
		if len(r.written) != 0 {
			t.Errorf("arg %q: the marker file was rewritten for no change: %+v", mode.arg, r.written)
		}
	}
}

func TestRewire_ACaseVariantMarkerIsWiredWithTheHubsSpelling(t *testing.T) {
	want := *marker("acme-web", "hemfrid", "acme-web") // as the Hub lists it
	for _, mode := range []struct{ arg, input string }{{"acme-web", ""}, {"", "y\n"}} {
		var r rewire
		_, err := checkout.Run(context.Background(), mode.arg, r.deps(marker("acme-web", "Hemfrid", "Acme-Web"), fakeList(twoProjects()), mode.input))
		if err != nil {
			t.Fatalf("arg %q: unexpected error: %v", mode.arg, err)
		}
		if len(r.wired) != 1 || r.wired[0] != want {
			t.Errorf("arg %q: wired %+v, want the Hub's spelling %+v", mode.arg, r.wired, want)
		}
		if len(r.written) != 1 || r.written[0] != want {
			t.Errorf("arg %q: marker written %+v, want %+v", mode.arg, r.written, want)
		}
	}
}

// The project was renamed in the Hub after this checkout was made.
func renamedList() func(context.Context) ([]hub.Project, error) {
	return fakeList([]hub.Project{
		{Name: "acme-portal", Org: "hemfrid", Repo: "acme-web", Role: "owner"},
		{Name: "beta-api", Org: "hemfrid", Repo: "beta-api", Role: "member"},
	})
}

func TestRewire_ARenamedProject_Interactive_ShowsAndWiresTheHubsName(t *testing.T) {
	var r rewire
	_, err := checkout.Run(context.Background(), "", r.deps(marker("acme-web", "hemfrid", "acme-web"), renamedList(), "y\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := *marker("acme-portal", "hemfrid", "acme-web")
	if !strings.Contains(r.out.String(), "Work in acme-portal?") || strings.Contains(r.out.String(), "Work in acme-web?") {
		t.Errorf("the prompt must name the Hub's project: %q", r.out.String())
	}
	if len(r.wired) != 1 || r.wired[0] != want {
		t.Errorf("wired %+v, want %+v", r.wired, want)
	}
	if len(r.written) != 1 || r.written[0] != want {
		t.Errorf("marker written %+v, want %+v", r.written, want)
	}
	// The user is told the name changed.
	if !strings.Contains(r.out.String(), "acme-web") || !strings.Contains(r.out.String(), ".keyto/project.json") {
		t.Errorf("want a note that the marker was updated from acme-web: %q", r.out.String())
	}
}

func TestRewire_ARenamedProject_NamedWithTheNewName_Rewires(t *testing.T) {
	var r rewire
	cloned := 0
	d := r.deps(marker("acme-web", "hemfrid", "acme-web"), renamedList(), "")
	d.Clone = func(repoURL, dir string) error { cloned++; return nil }
	_, err := checkout.Run(context.Background(), "acme-portal", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloned != 0 {
		t.Errorf("this folder already is that project: it must be re-wired in place, not cloned again (%d clones)", cloned)
	}
	want := *marker("acme-portal", "hemfrid", "acme-web")
	if len(r.wired) != 1 || r.wired[0] != want || len(r.written) != 1 || r.written[0] != want {
		t.Errorf("wired %+v written %+v, want both %+v", r.wired, r.written, want)
	}
}

func TestRewire_ARenamedProject_NamedWithTheOldName_SaysTheNewName(t *testing.T) {
	var r rewire
	_, err := checkout.Run(context.Background(), "acme-web", r.deps(marker("acme-web", "hemfrid", "acme-web"), renamedList(), ""))
	if err == nil || len(r.wired) != 0 || len(r.written) != 0 {
		t.Fatalf("want an error and nothing wired or written, got err=%v wired=%d written=%d", err, len(r.wired), len(r.written))
	}
	if !strings.Contains(err.Error(), "acme-portal") {
		t.Errorf("the error should name the project as the Hub calls it now: %q", err.Error())
	}
}

// A marker that carries one project's name and another project's repo. The
// repo decides; the name shown and wired is the Hub's name for that repo.
func TestRewire_AMarkerNamingOneProjectAndAnothersRepo_NeverShowsTheWrongName(t *testing.T) {
	m := marker("acme-web", "hemfrid", "beta-api")

	var r rewire
	_, err := checkout.Run(context.Background(), "", r.deps(m, fakeList(twoProjects()), "y\n"))
	if err != nil {
		t.Fatalf("interactive: unexpected error: %v", err)
	}
	if !strings.Contains(r.out.String(), "Work in beta-api?") || strings.Contains(r.out.String(), "Work in acme-web?") {
		t.Errorf("interactive: the prompt must name the project the repo belongs to: %q", r.out.String())
	}
	if want := *marker("beta-api", "hemfrid", "beta-api"); len(r.wired) != 1 || r.wired[0] != want {
		t.Errorf("interactive: wired %+v, want %+v", r.wired, want)
	}

	// Asked for acme-web by name: this folder is beta-api's, so nothing is wired.
	var named rewire
	_, err = checkout.Run(context.Background(), "acme-web", named.deps(m, fakeList(twoProjects()), ""))
	if err == nil || len(named.wired) != 0 || len(named.written) != 0 {
		t.Fatalf("named: want an error and nothing wired, got err=%v wired=%d", err, len(named.wired))
	}
	if !strings.Contains(err.Error(), "beta-api") {
		t.Errorf("named: the error should say which project this folder is: %q", err.Error())
	}
}

func TestRewire_DecliningThePromptStillGoesToThePicker(t *testing.T) {
	var r rewire
	cloned := 0
	d := r.deps(marker("acme-web", "hemfrid", "acme-web"), fakeList(twoProjects()), "n\n2\n\n")
	d.Clone = func(repoURL, dir string) error { cloned++; return nil }
	if _, err := checkout.Run(context.Background(), "", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloned != 1 {
		t.Errorf("want the picked project cloned, got %d clones", cloned)
	}
}

func TestRewire_WhenTheProjectListCannotBeRead_NothingIsWired(t *testing.T) {
	listErr := &hub.StatusError{Op: "list-projects", StatusCode: 401}
	failing := func(ctx context.Context) ([]hub.Project, error) { return nil, listErr }
	for _, mode := range []struct{ arg, input string }{{"acme-web", ""}, {"", "y\n"}} {
		var r rewire
		_, err := checkout.Run(context.Background(), mode.arg, r.deps(marker("acme-web", "hemfrid", "acme-web"), failing, mode.input))
		if err == nil || len(r.wired) != 0 {
			t.Errorf("arg %q: want an error and no wiring, got err=%v wired=%d", mode.arg, err, len(r.wired))
		}
		// A refused credential keeps its type, so `keyto checkout` signs in again.
		if !errors.Is(err, listErr) || !hub.IsUnauthorized(err) {
			t.Errorf("arg %q: the Hub's 401 lost its type: %v", mode.arg, err)
		}
	}
}

func TestRewire_AMarkerWithControlCharactersIsNamedSafely(t *testing.T) {
	m := marker("acme-web", "hemfrid", "pay\x1b[2Jroll\nfake line")
	var r rewire
	_, err := checkout.Run(context.Background(), "acme-web", r.deps(m, fakeList(twoProjects()), ""))
	if err == nil || len(r.wired) != 0 {
		t.Fatalf("want an error and no wiring, got err=%v wired=%d", err, len(r.wired))
	}
	if strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Errorf("the error carries a control character: %q", err.Error())
	}
}

// A marker for a different project than the one asked for is left alone: the
// asked-for project is looked up and cloned as before.
func TestNamedCheckout_InAFolderOfAnotherProject_ClonesTheOneAskedFor(t *testing.T) {
	var r rewire
	var clonedURL string
	d := r.deps(marker("acme-web", "hemfrid", "acme-web"), fakeList(twoProjects()), "\n")
	d.Clone = func(repoURL, dir string) error { clonedURL = repoURL; return nil }
	if _, err := checkout.Run(context.Background(), "beta-api", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clonedURL != hubURL+"/git/hemfrid/beta-api.git" {
		t.Errorf("cloned %q", clonedURL)
	}
}

// The marker's hub URL is not this change's business: the remote is built from
// the signed-in Hub. A marker that differs only there is not rewritten, and a
// rewritten marker keeps the hub URL it had.
func TestRewire_LeavesTheMarkersHubURLAlone(t *testing.T) {
	other := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://keyto-hub-uat.example.com"}
	var same rewire
	if _, err := checkout.Run(context.Background(), "acme-web", same.deps(other, fakeList(twoProjects()), "")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(same.written) != 0 {
		t.Errorf("a marker that differs only in its hub URL was rewritten: %+v", same.written)
	}

	renamed := &project.Marker{Name: "old-name", Org: "hemfrid", Repo: "acme-web", HubURL: "https://keyto-hub-uat.example.com"}
	var r rewire
	if _, err := checkout.Run(context.Background(), "acme-web", r.deps(renamed, fakeList(twoProjects()), "")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://keyto-hub-uat.example.com"}
	if len(r.written) != 1 || r.written[0] != want {
		t.Errorf("marker written %+v, want %+v", r.written, want)
	}
}

// The marker file is committed in the repo. It is rewritten only once the
// wiring went through: a refused or failed wiring must not leave a changed
// .keyto/project.json behind.
func TestRewire_AFailedWiringLeavesTheMarkerFileUntouched(t *testing.T) {
	wireErr := errors.New("the project marker names another Hub than the one you are signed in to")
	for _, mode := range []struct{ arg, input string }{{"acme-portal", ""}, {"", "y\n"}} {
		var r rewire
		d := r.deps(marker("acme-web", "Hemfrid", "Acme-Web"), renamedList(), mode.input)
		d.Wire = func(dir string, m *project.Marker, email, name string) error { return wireErr }
		_, err := checkout.Run(context.Background(), mode.arg, d)
		if !errors.Is(err, wireErr) {
			t.Errorf("arg %q: want the wiring error, got %v", mode.arg, err)
		}
		if len(r.written) != 0 {
			t.Errorf("arg %q: the marker was rewritten although the wiring failed: %+v", mode.arg, r.written)
		}
		if strings.Contains(r.out.String(), "ready") || strings.Contains(r.out.String(), "was updated") {
			t.Errorf("arg %q: reported success: %q", mode.arg, r.out.String())
		}
	}
}

func TestRewire_TheMarkerIsWrittenAfterTheWiring(t *testing.T) {
	var order []string
	var r rewire
	d := r.deps(marker("acme-web", "hemfrid", "acme-web"), renamedList(), "")
	d.Wire = func(dir string, m *project.Marker, email, name string) error {
		order = append(order, "wire")
		return nil
	}
	d.WriteMarker = func(dir string, m *project.Marker) error { order = append(order, "write"); return nil }
	if _, err := checkout.Run(context.Background(), "acme-portal", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(order, ",") != "wire,write" {
		t.Errorf("order = %v, want wire then write", order)
	}
}
