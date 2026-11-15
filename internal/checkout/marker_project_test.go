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
// checkout is re-wired in place, the marker's org and repo become the git
// remote on the signed-in Hub. A marker that names a repo the user did not
// mean (another project they can reach, or one they cannot) would send their
// pushes there. So the marker's org/repo has to be one of the user's own Hub
// projects, as the Hub lists them.

// rewireModes are the two ways a checkout is re-wired in place.
var rewireModes = []struct{ name, arg, input string }{
	{"keyto checkout <name> inside the checkout", "acme-web", ""},
	{"keyto checkout, answering yes to 'Work in …?'", "", "y\n"},
}

func rewireDeps(marker *project.Marker, list func(context.Context) ([]hub.Project, error), input string, wired *int, out *bytes.Buffer) checkout.Deps {
	return checkout.Deps{
		Creds:       makeCreds(),
		List:        list,
		Clone:       func(repoURL, dir string) error { return nil },
		Wire:        func(dir string, m *project.Marker, email, name string) error { *wired++; return nil },
		ReadMarker:  fakeReadMarker(marker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		In:          strings.NewReader(input),
		Out:         out,
	}
}

func TestRewire_RefusesAMarkerWhoseRepoIsNotOneOfTheUsersProjects(t *testing.T) {
	markers := []*project.Marker{
		// A repo the user is not a member of.
		{Name: "acme-web", Org: "hemfrid", Repo: "payroll", HubURL: "https://hub.example.com"},
		// Another org.
		{Name: "acme-web", Org: "someone-else", Repo: "acme-web", HubURL: "https://hub.example.com"},
		// The name of one of the user's projects, pointed at the repo of another of theirs.
		{Name: "acme-web", Org: "hemfrid", Repo: "beta-api", HubURL: "https://hub.example.com"},
	}
	for _, m := range markers {
		for _, mode := range rewireModes {
			wired := 0
			var out bytes.Buffer
			_, err := checkout.Run(context.Background(), mode.arg, rewireDeps(m, fakeList(twoProjects()), mode.input, &wired, &out))
			if err == nil || wired != 0 {
				t.Errorf("%s/%s (%s): want an error and no wiring, got err=%v wired=%d", m.Org, m.Repo, mode.name, err, wired)
				continue
			}
			if strings.Contains(out.String(), "ready") {
				t.Errorf("%s/%s (%s): reported ready: %q", m.Org, m.Repo, mode.name, out.String())
			}
			// The message says which repo, and what to do.
			for _, want := range []string{m.Org + "/" + m.Repo, ".keyto/project.json"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s/%s (%s): want %q in %q", m.Org, m.Repo, mode.name, want, err.Error())
				}
			}
		}
	}
}

func TestRewire_AcceptsAMarkerThatIsOneOfTheUsersProjects(t *testing.T) {
	markers := []*project.Marker{
		{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"},
		// GitHub owner and repo names are not case-sensitive.
		{Name: "acme-web", Org: "Hemfrid", Repo: "Acme-Web", HubURL: "https://hub.example.com"},
	}
	for _, m := range markers {
		for _, mode := range rewireModes {
			wired := 0
			var out bytes.Buffer
			_, err := checkout.Run(context.Background(), mode.arg, rewireDeps(m, fakeList(twoProjects()), mode.input, &wired, &out))
			if err != nil || wired != 1 {
				t.Errorf("%s/%s (%s): err=%v wired=%d, want none and 1", m.Org, m.Repo, mode.name, err, wired)
			}
		}
	}
}

func TestRewire_WhenTheProjectListCannotBeRead_NothingIsWired(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	listErr := &hub.StatusError{Op: "list-projects", StatusCode: 401}
	failing := func(ctx context.Context) ([]hub.Project, error) { return nil, listErr }
	for _, mode := range rewireModes {
		wired := 0
		var out bytes.Buffer
		_, err := checkout.Run(context.Background(), mode.arg, rewireDeps(m, failing, mode.input, &wired, &out))
		if err == nil || wired != 0 {
			t.Errorf("%s: want an error and no wiring, got err=%v wired=%d", mode.name, err, wired)
		}
		// A refused credential keeps its type, so `keyto checkout` signs in again.
		if !errors.Is(err, listErr) || !hub.IsUnauthorized(err) {
			t.Errorf("%s: the Hub's 401 lost its type: %v", mode.name, err)
		}
	}
}

func TestRewire_AMarkerWithControlCharactersIsNamedSafely(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "pay\x1b[2Jroll\nfake line", HubURL: "https://hub.example.com"}
	wired := 0
	var out bytes.Buffer
	_, err := checkout.Run(context.Background(), "acme-web", rewireDeps(m, fakeList(twoProjects()), "", &wired, &out))
	if err == nil || wired != 0 {
		t.Fatalf("want an error and no wiring, got err=%v wired=%d", err, wired)
	}
	if strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Errorf("the error carries a control character: %q", err.Error())
	}
}
