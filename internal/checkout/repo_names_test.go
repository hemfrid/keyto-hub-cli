package checkout_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// The Hub's org and repo for a project go into the git remote URL,
// <hub>/git/<org>/<repo>.git. A value that is not a GitHub owner or repo name
// could change where that URL points, so it is refused before any git command
// runs or any prompt that names it is shown.
var hostileOrgRepos = []struct{ org, repo string }{
	{"hemfrid/../../other", "acme-web"},
	{"hemfrid", "../../other/repo"},
	{"hemfrid", "acme-web/../../x"},
	{"hemfrid", "acme-web?service=git-upload-pack"},
	{"hemfrid", "acme-web#frag"},
	{"evil.example@hemfrid", "acme-web"},
	{"hemfrid", "x@evil.example"},
	{"hemfrid", "acme-web\n[core]\n\tsshCommand = evil"},
	{"hem\rfrid", "acme-web"},
	{"hemfrid", "acme web"},
	{"hemfrid", "acme%2fweb"},
	{"", "acme-web"},
	{"hemfrid", ""},
	{"hemfrid", "acme-web.git"},
	{"--upload-pack=evil", "acme-web"},
}

func TestCheckout_RefusesAHubOrgOrRepoThatIsNotAGitHubName(t *testing.T) {
	for _, c := range hostileOrgRepos {
		for _, arg := range []string{"", "acme-web"} { // from the picker, and named
			var out bytes.Buffer
			var cloned, wired, originAsked int
			d := checkout.Deps{
				Creds:       makeCreds(),
				List:        fakeList([]hub.Project{{Name: "acme-web", Org: c.org, Repo: c.repo, Role: "owner"}}),
				Clone:       func(repoURL, dir string) error { cloned++; return nil },
				Wire:        func(dir string, m *project.Marker, email, name string) error { wired++; return nil },
				ReadMarker:  fakeReadMarker(nil),
				WriteMarker: noopWriteMarker,
				OriginURL: func(dir string) (string, error) {
					originAsked++
					return "https://x/" + c.org + "/" + c.repo + ".git", nil
				},
				Toplevel: func(dir string) (string, error) { return "/tmp/cwd", nil },
				Cwd:      "/tmp/cwd",
				In:       strings.NewReader("1\ny\n\n"),
				Out:      &out,
			}
			_, err := checkout.Run(context.Background(), arg, d)
			if err == nil {
				t.Errorf("org %q repo %q (arg %q): want an error", c.org, c.repo, arg)
				continue
			}
			if cloned+wired+originAsked != 0 {
				t.Errorf("org %q repo %q (arg %q): clone=%d wire=%d origin=%d, want none", c.org, c.repo, arg, cloned, wired, originAsked)
			}
			if strings.ContainsAny(err.Error(), "\n\r\x1b") {
				t.Errorf("org %q repo %q: the error carries a control character: %q", c.org, c.repo, err.Error())
			}
			if strings.Contains(out.String(), "already a checkout of") || strings.Contains(out.String(), "Checkout directory [") {
				t.Errorf("org %q repo %q: a prompt was shown: %q", c.org, c.repo, out.String())
			}
		}
	}
}

func TestCheckout_OrdinaryOrgAndRepoBuildTheSameURLAsBefore(t *testing.T) {
	var clonedURL string
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{{Name: "acme-web", Org: "Hemfrid", Repo: "Acme_Web.v2", Role: "owner"}}),
		Clone:       func(repoURL, dir string) error { clonedURL = repoURL; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("1\n\n"),
		Out:         &bytes.Buffer{},
	}
	if _, err := checkout.Run(context.Background(), "", d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://hub.example.com/git/Hemfrid/Acme_Web.v2.git"; clonedURL != want {
		t.Errorf("clone URL = %q, want %q", clonedURL, want)
	}
}
