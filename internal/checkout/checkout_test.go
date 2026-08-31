package checkout_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// ---- fake helpers ----

func noopClone(repoURL, dir string) error                              { return nil }
func noopWire(dir string, m *project.Marker, email, name string) error { return nil }
func noopWriteMarker(dir string, m *project.Marker) error              { return nil }

func fakeList(projects []hub.Project) func(ctx context.Context) ([]hub.Project, error) {
	return func(ctx context.Context) ([]hub.Project, error) {
		return projects, nil
	}
}

func fakeReadMarker(m *project.Marker) func(dir string) (*project.Marker, error) {
	return func(dir string) (*project.Marker, error) {
		return m, nil
	}
}

func makeCreds() *config.Creds {
	return &config.Creds{
		HubURL:    "https://hub.example.com",
		UserEmail: "alice@example.com",
		UserName:  "Alice",
	}
}

func twoProjects() []hub.Project {
	return []hub.Project{
		{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", Role: "owner"},
		{Name: "beta-api", Org: "hemfrid", Repo: "beta-api", Role: "member"},
	}
}

// ---- tests ----

// T1: nil creds → error mentioning keyto auth; no clone called.
func TestRun_NilCreds_ReturnsAuthError(t *testing.T) {
	cloneCalled := false
	d := checkout.Deps{
		Creds:       nil,
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err == nil {
		t.Fatal("expected error for nil creds, got nil")
	}
	if !strings.Contains(err.Error(), "keyto auth") {
		t.Errorf("error should mention 'keyto auth', got: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when creds are nil")
	}
}

// T2: projectArg set, cwd marker matches → Wire called on Cwd, Clone NOT called.
func TestRun_ProjectArg_CwdMarkerMatches_RewiresInPlace(t *testing.T) {
	cloneCalled := false
	var wiredDir string
	cwdMarker := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire: func(dir string, m *project.Marker, email, name string) error {
			wiredDir = dir
			return nil
		},
		ReadMarker:  fakeReadMarker(cwdMarker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when cwd marker already matches the project")
	}
	if wiredDir != "/tmp/acme-web" {
		t.Errorf("Wire called with dir %q, want /tmp/acme-web", wiredDir)
	}
}

// T3: projectArg set, not in cwd → List+find, Clone called with right remote URL + dir.
func TestRun_ProjectArg_NotInCwd_ClonesWithCorrectURL(t *testing.T) {
	var clonedURL, clonedDir string
	var wiredDir string
	var wroteMarker bool

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: func(repoURL, dir string) error {
			clonedURL = repoURL
			clonedDir = dir
			return nil
		},
		Wire: func(dir string, m *project.Marker, email, name string) error {
			wiredDir = dir
			return nil
		},
		ReadMarker: fakeReadMarker(nil), // cwd is not a keyto project
		WriteMarker: func(dir string, m *project.Marker) error {
			wroteMarker = true
			return nil
		},
		Cwd: "/tmp/cwd",
		// Simulate user confirming with empty line (default dir), then selecting first project
		In:  strings.NewReader("\n"), // accept default checkout dir
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := "https://hub.example.com/git/hemfrid/acme-web.git"
	if clonedURL != wantURL {
		t.Errorf("Clone URL = %q, want %q", clonedURL, wantURL)
	}
	wantDir := "/tmp/cwd/acme-web"
	if clonedDir != wantDir {
		t.Errorf("Clone dir = %q, want %q", clonedDir, wantDir)
	}
	if wiredDir != wantDir {
		t.Errorf("Wire dir = %q, want %q", wiredDir, wantDir)
	}
	if !wroteMarker {
		t.Error("WriteMarker was not called")
	}
}

// T4: projectArg set, custom dir typed by user.
func TestRun_ProjectArg_CustomDir_UsedForClone(t *testing.T) {
	var clonedDir string

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { clonedDir = dir; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("/home/user/projects/my-acme\n"),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clonedDir != "/home/user/projects/my-acme" {
		t.Errorf("Clone dir = %q, want /home/user/projects/my-acme", clonedDir)
	}
}

// T5: projectArg set, project not a member → error, no clone.
func TestRun_ProjectArg_NotMember_Error(t *testing.T) {
	cloneCalled := false

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "nonexistent-project", d)
	if err == nil {
		t.Fatal("expected error when project not found, got nil")
	}
	if cloneCalled {
		t.Error("Clone must not be called when project is not found")
	}
}

// T6: no arg + cwd marker present + prompt "y" → re-wire in place, no clone.
func TestRun_NoArg_CwdMarker_PromptYes_RewiresInPlace(t *testing.T) {
	cloneCalled := false
	var wiredDir string
	cwdMarker := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire: func(dir string, m *project.Marker, email, name string) error {
			wiredDir = dir
			return nil
		},
		ReadMarker:  fakeReadMarker(cwdMarker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		In:          strings.NewReader("y\n"),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when re-wiring in place")
	}
	if wiredDir != "/tmp/acme-web" {
		t.Errorf("Wire dir = %q, want /tmp/acme-web", wiredDir)
	}
}

// T7: no arg + cwd marker present + prompt "n" → picker path (List shown, selection → clone).
func TestRun_NoArg_CwdMarker_PromptNo_FallsToPicker(t *testing.T) {
	cloneCalled := false
	cwdMarker := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(cwdMarker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		// "n" → decline in-place; "1" → pick first project; "" → accept default dir
		In:  strings.NewReader("n\n1\n\n"),
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cloneCalled {
		t.Error("Clone should be called after picking from list")
	}
}

// T8: no arg + no cwd marker → picker; selection → clone with correct project.
func TestRun_NoArg_NoCwdMarker_Picker_ClonesSelected(t *testing.T) {
	var clonedURL string

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: func(repoURL, dir string) error {
			clonedURL = repoURL
			return nil
		},
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		// Select project 2 (beta-api), accept default dir
		In:  strings.NewReader("2\n\n"),
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := "https://hub.example.com/git/hemfrid/beta-api.git"
	if clonedURL != wantURL {
		t.Errorf("Clone URL = %q, want %q", clonedURL, wantURL)
	}
}

// T9: no arg + no cwd marker + empty list → graceful message, no clone.
func TestRun_NoArg_EmptyList_GracefulMessage(t *testing.T) {
	cloneCalled := false
	var out bytes.Buffer

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{}),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(""),
		Out:         &out,
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error for empty list: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when no projects are available")
	}
	if out.Len() == 0 {
		t.Error("expected a message to be printed when no projects are available")
	}
}

// T10: List error is propagated.
func TestRun_ListError_Propagated(t *testing.T) {
	listErr := errors.New("network failure")

	d := checkout.Deps{
		Creds: makeCreds(),
		List: func(ctx context.Context) ([]hub.Project, error) {
			return nil, listErr
		},
		Clone:       noopClone,
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err == nil {
		t.Fatal("expected error from List, got nil")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("expected error to wrap listErr, got: %v", err)
	}
}

// T11: Wire error is propagated.
func TestRun_WireError_Propagated(t *testing.T) {
	wireErr := errors.New("git config failed")
	cwdMarker := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: noopClone,
		Wire: func(dir string, m *project.Marker, email, name string) error {
			return wireErr
		},
		ReadMarker:  fakeReadMarker(cwdMarker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "acme-web", d)
	if err == nil {
		t.Fatal("expected error from Wire, got nil")
	}
	if !errors.Is(err, wireErr) {
		t.Errorf("expected error to wrap wireErr, got: %v", err)
	}
}

// T12: the clone path returns the checkout directory (used by shell integration
// to cd the calling shell into the project).
func TestRun_ClonePath_ReturnsCheckoutDir(t *testing.T) {
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       noopClone,
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader("\n"), // accept default checkout dir
		Out:         &bytes.Buffer{},
	}

	dir, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/tmp/cwd/acme-web" {
		t.Errorf("returned dir = %q, want /tmp/cwd/acme-web", dir)
	}
}

// T13: re-wiring in place returns cwd (integration cd's to the current project).
func TestRun_RewireInPlace_ReturnsCwd(t *testing.T) {
	cwdMarker := &project.Marker{
		Name:   "acme-web",
		Org:    "hemfrid",
		Repo:   "acme-web",
		HubURL: "https://hub.example.com",
	}
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       noopClone,
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(cwdMarker),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/acme-web",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	dir, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/tmp/acme-web" {
		t.Errorf("returned dir = %q, want /tmp/acme-web", dir)
	}
}

// T15: picker selection whose org/repo matches cwd's git origin → adopt in
// place (accept default Y): marker written to cwd, Wire on cwd, no clone,
// returns cwd.
func TestRun_Picker_CwdOriginMatches_AdoptsInPlace(t *testing.T) {
	cloneCalled := false
	var markerDir string
	var wiredDir string

	d := checkout.Deps{
		Creds: makeCreds(),
		List:  fakeList(twoProjects()),
		Clone: func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire: func(dir string, m *project.Marker, email, name string) error {
			wiredDir = dir
			return nil
		},
		ReadMarker: fakeReadMarker(nil),
		WriteMarker: func(dir string, m *project.Marker) error {
			markerDir = dir
			return nil
		},
		OriginURL: func(dir string) (string, error) {
			return "https://github.com/hemfrid/acme-web.git", nil
		},
		Cwd: "/tmp/acme-web",
		// "1" → pick acme-web; "" → accept adopt prompt (default yes)
		In:  strings.NewReader("1\n\n"),
		Out: &bytes.Buffer{},
	}

	dir, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when adopting an existing checkout")
	}
	if markerDir != "/tmp/acme-web" {
		t.Errorf("WriteMarker dir = %q, want /tmp/acme-web", markerDir)
	}
	if wiredDir != "/tmp/acme-web" {
		t.Errorf("Wire dir = %q, want /tmp/acme-web", wiredDir)
	}
	if dir != "/tmp/acme-web" {
		t.Errorf("returned dir = %q, want /tmp/acme-web", dir)
	}
}

// T16: adopt prompt declined ("n") → falls through to prompt-dir + clone.
func TestRun_Picker_AdoptDeclined_FallsThroughToClone(t *testing.T) {
	var clonedDir string

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { clonedDir = dir; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		OriginURL: func(dir string) (string, error) {
			return "https://github.com/hemfrid/acme-web.git", nil
		},
		Cwd: "/tmp/acme-web",
		// "1" → pick acme-web; "n" → decline adopt; "" → accept default clone dir
		In:  strings.NewReader("1\nn\n\n"),
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clonedDir != "/tmp/acme-web/acme-web" {
		t.Errorf("Clone dir = %q, want /tmp/acme-web/acme-web", clonedDir)
	}
}

// T17: cwd origin points at a different repo → no adopt prompt, straight to clone.
func TestRun_Picker_OriginMismatch_NoAdoptPrompt(t *testing.T) {
	cloneCalled := false
	var out bytes.Buffer

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		OriginURL: func(dir string) (string, error) {
			return "https://github.com/other-org/other-repo.git", nil
		},
		Cwd: "/tmp/cwd",
		// "1" → pick acme-web; "" → accept default clone dir (no adopt prompt in between)
		In:  strings.NewReader("1\n\n"),
		Out: &out,
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cloneCalled {
		t.Error("Clone should be called when cwd origin does not match")
	}
	if strings.Contains(out.String(), "already a checkout") {
		t.Error("adopt prompt must not be shown on origin mismatch")
	}
}

// T18: cwd is not a git repo (OriginURL errors) → straight to clone.
func TestRun_Picker_CwdNotGitRepo_Clones(t *testing.T) {
	cloneCalled := false

	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList(twoProjects()),
		Clone:       func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		OriginURL: func(dir string) (string, error) {
			return "", errors.New("not a git repository")
		},
		Cwd: "/tmp/cwd",
		In:  strings.NewReader("1\n\n"),
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cloneCalled {
		t.Error("Clone should be called when cwd is not a git repo")
	}
}

// T19: explicit project arg (no cwd marker) with matching origin → adopt in place.
func TestRun_ProjectArg_CwdOriginMatches_AdoptsInPlace(t *testing.T) {
	cloneCalled := false
	var markerDir string

	d := checkout.Deps{
		Creds:      makeCreds(),
		List:       fakeList(twoProjects()),
		Clone:      func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:       noopWire,
		ReadMarker: fakeReadMarker(nil),
		WriteMarker: func(dir string, m *project.Marker) error {
			markerDir = dir
			return nil
		},
		// SSH-form origin must also match.
		OriginURL: func(dir string) (string, error) {
			return "git@github.com:hemfrid/acme-web.git", nil
		},
		Cwd: "/tmp/acme-web",
		In:  strings.NewReader("y\n"),
		Out: &bytes.Buffer{},
	}

	dir, err := checkout.Run(context.Background(), "acme-web", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when adopting an existing checkout")
	}
	if markerDir != "/tmp/acme-web" {
		t.Errorf("WriteMarker dir = %q, want /tmp/acme-web", markerDir)
	}
	if dir != "/tmp/acme-web" {
		t.Errorf("returned dir = %q, want /tmp/acme-web", dir)
	}
}

// T20: adoption writes a marker carrying the selected project's identity and
// the creds' Hub URL (Wire uses it to point origin at the Hub proxy).
func TestRun_Adopt_MarkerHasProjectIdentity(t *testing.T) {
	var got *project.Marker
	cloneCalled := false

	d := checkout.Deps{
		Creds:      makeCreds(),
		List:       fakeList(twoProjects()),
		Clone:      func(repoURL, dir string) error { cloneCalled = true; return nil },
		Wire:       noopWire,
		ReadMarker: fakeReadMarker(nil),
		WriteMarker: func(dir string, m *project.Marker) error {
			got = m
			return nil
		},
		OriginURL: func(dir string) (string, error) {
			return "https://hub.example.com/git/hemfrid/beta-api.git", nil
		},
		Cwd: "/tmp/beta-api",
		// "2" → pick beta-api; "y" → adopt
		In:  strings.NewReader("2\ny\n"),
		Out: &bytes.Buffer{},
	}

	_, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cloneCalled {
		t.Error("Clone must not be called when adopting an existing checkout")
	}
	if got == nil {
		t.Fatal("WriteMarker was not called")
	}
	if got.Name != "beta-api" || got.Org != "hemfrid" || got.Repo != "beta-api" || got.HubURL != "https://hub.example.com" {
		t.Errorf("marker = %+v, want beta-api/hemfrid/beta-api @ https://hub.example.com", got)
	}
}

// T14: an empty project list resolves no project → empty dir, no error.
func TestRun_EmptyList_ReturnsEmptyDir(t *testing.T) {
	d := checkout.Deps{
		Creds:       makeCreds(),
		List:        fakeList([]hub.Project{}),
		Clone:       noopClone,
		Wire:        noopWire,
		ReadMarker:  fakeReadMarker(nil),
		WriteMarker: noopWriteMarker,
		Cwd:         "/tmp/cwd",
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
	}

	dir, err := checkout.Run(context.Background(), "", d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "" {
		t.Errorf("returned dir = %q, want empty", dir)
	}
}
