package ai

import (
	"context"
	"strings"
	"testing"
)

// Tags a misbehaving Hub (or whatever answers as it) could send in
// /api/ai-bundle/meta: path traversal, terminal escapes, near-misses.
var hostileBundleTags = []string{
	"",
	"v0.2.0/../../x",
	"../v0.2.0",
	"v0.2.0\x1b]52;c;ZXZpbA==\x07",
	"v0.2.0\x1b[2J",
	"v0.2.0\n",
	"rc/1.3.0-1/../x",
	"rc/1.3.0",
	"rc/../1.3.0-1",
	"main",
	"e394b3f7b87f5bd19cae55f21a95d1357549783d",
	"v0.2.0‮",
	// Whitespace around a valid tag.
	" v0.2.0",
	"v0.2.0 ",
	"\tv0.2.0",
	"v0.2.0\t",
	"v0.2.0\n",
	"v0.2.0\u00a0",
	" rc/1.3.0-1",
	"rc/1.3.0-1 ",
	"rc/ 1.3.0-1",
	// Digits that are not ASCII 0-9.
	"v\uff11.2.3",         // fullwidth 1
	"v1.\u0662.3",         // Arabic-Indic 2
	"v1.2.\u0969",         // Devanagari 3
	"rc/1.3.0-\uff11",     // fullwidth 1 in the candidate number
	"rc/\U0001d7cf.3.0-1", // mathematical bold 1
}

func TestValidBundleTag(t *testing.T) {
	for _, tag := range []string{"v0.2.0", "v1.2.0", "rc/1.3.0-1", "rc/10.20.30-4"} {
		if !ValidBundleTag(tag) {
			t.Errorf("%q should be valid", tag)
		}
	}
	for _, tag := range hostileBundleTags {
		if ValidBundleTag(tag) {
			t.Errorf("%q should be refused", tag)
		}
	}
}

func checkRefused(t *testing.T, what, tag string, err error, tarballCalls int) {
	t.Helper()
	if err == nil {
		t.Errorf("%s, tag %q: want an error", what, tag)
		return
	}
	if tarballCalls != 0 {
		t.Errorf("%s, tag %q: the tarball was fetched %d time(s)", what, tag, tarballCalls)
	}
	if tag != "" && strings.Contains(err.Error(), tag) {
		t.Errorf("%s, tag %q: the error echoes the Hub's tag: %q", what, tag, err.Error())
	}
	if strings.ContainsAny(err.Error(), "\x1b\n‮") {
		t.Errorf("%s, tag %q: the error carries control characters: %q", what, tag, err.Error())
	}
}

func hostileDeps(t *testing.T, tag string, tarballCalls *int) Deps {
	d := fakeDeps(t, bundleFixture())
	d.Meta = withTag(d.Meta, tag)
	inner := d.Tarball
	d.Tarball = func(ctx context.Context, tg string) ([]byte, error) {
		*tarballCalls++
		return inner(ctx, tg)
	}
	return d
}

func TestInit_RefusesAHostileHubTagBeforeDownloading(t *testing.T) {
	for _, tag := range hostileBundleTags {
		root := initTestRepo(t)
		calls := 0
		_, err := Init(context.Background(), root, hostileDeps(t, tag, &calls))
		checkRefused(t, "Init", tag, err, calls)
		if pin, _ := LoadPin(root); pin != nil {
			t.Errorf("Init, tag %q: a pin was written", tag)
		}
	}
}

func TestUpdate_RefusesAHostileHubTagBeforeDownloading(t *testing.T) {
	for _, tag := range hostileBundleTags {
		root := initInstalled(t)
		calls := 0
		_, err := Update(context.Background(), root, hostileDeps(t, tag, &calls))
		checkRefused(t, "Update", tag, err, calls)
		if pin, _ := LoadPin(root); pin == nil || pin.Tag != "v0.2.0" {
			t.Errorf("Update, tag %q: the pin changed", tag)
		}
	}
}

func TestStatus_RefusesAHostileHubTag(t *testing.T) {
	for _, tag := range hostileBundleTags {
		root := initInstalled(t)
		calls := 0
		_, err := GetStatus(context.Background(), root, hostileDeps(t, tag, &calls))
		checkRefused(t, "GetStatus", tag, err, calls)
	}
}

func TestInit_AcceptsAnRCTag(t *testing.T) {
	root := initTestRepo(t)
	calls := 0
	res, err := Init(context.Background(), root, hostileDeps(t, "rc/1.3.0-1", &calls))
	if err != nil || res.Tag != "rc/1.3.0-1" || calls != 1 {
		t.Fatalf("res=%+v calls=%d err=%v", res, calls, err)
	}
}

// The pin is a committed file anyone with push can edit. Its tag is shown, not
// trusted: an unrecognized one is masked, and update still replaces it.
func TestAHostilePinTagIsMaskedInStatusAndUpdate(t *testing.T) {
	const evil = "v0.1.0\x1b]52;c;ZXZpbA==\x07"
	root := initInstalled(t)
	pin, err := LoadPin(root)
	if err != nil || pin == nil {
		t.Fatal(err)
	}
	pin.Tag = evil
	if err := SavePin(root, pin); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	st, err := GetStatus(context.Background(), root, fakeDeps(t, bundleFixture()))
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.InstalledTag != UnrecognizedTag || st.UpToDate {
		t.Errorf("status: installed=%q upToDate=%v", st.InstalledTag, st.UpToDate)
	}

	res, err := Update(context.Background(), root, fakeDeps(t, bundleFixture()))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.FromTag != UnrecognizedTag || res.Tag != "v0.2.0" {
		t.Errorf("update: from=%q to=%q", res.FromTag, res.Tag)
	}
	if pin, _ := LoadPin(root); pin == nil || pin.Tag != "v0.2.0" {
		t.Errorf("the pin was not repaired")
	}
}

// Hub-scaffolded projects' pins carry no tag at all (ai-capabilities setup.sh
// writes none): that must keep working, shown as it is.
func TestAnEmptyPinTagIsShownAsIs(t *testing.T) {
	if DisplayTag("") != "" {
		t.Errorf("DisplayTag(\"\") = %q", DisplayTag(""))
	}
	if DisplayTag("rc/1.3.0-1") != "rc/1.3.0-1" || DisplayTag("v0.2.0") != "v0.2.0" {
		t.Errorf("valid tags must show as they are")
	}
}
