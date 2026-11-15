package ghname

import (
	"strings"
	"testing"
)

func TestValidOwner(t *testing.T) {
	for _, ok := range []string{"hemfrid", "Hemfrid", "a", "a-b", "a1-b2-c3", "sean-mi", strings.Repeat("a", 39)} {
		if !ValidOwner(ok) {
			t.Errorf("%q should be a valid owner", ok)
		}
	}
	for _, bad := range []string{
		"", "-a", "a-", "a--b", "a_b", "a.b", "a b", " a", "a ", strings.Repeat("a", 40),
		"a/b", `a\b`, "..", ".", "a/../b", "a?x=1", "a#x", "a@evil.example", "evil.example:443",
		"a\nb", "a\rb", "a\x00b", "a\x1b[2Jb", "a‮b", "a%2fb", "hemfrid.git", "ａ", "naïve",
	} {
		if ValidOwner(bad) {
			t.Errorf("%q should be refused as an owner", bad)
		}
	}
}

func TestValidRepo(t *testing.T) {
	for _, ok := range []string{
		"acme-web", "keyto-hub", "a", "A_b.c-d", ".github", "x.y", "keyto-hub-workers-template",
		"app-401", "..x", strings.Repeat("a", 100),
	} {
		if !ValidRepo(ok) {
			t.Errorf("%q should be a valid repo", ok)
		}
	}
	for _, bad := range []string{
		"", ".", "..", strings.Repeat("a", 101), "a b", " a", "a ",
		"a/b", `a\b`, "a/../b", "../x", "a?x=1", "a#x", "a@evil.example", "a:b", "a%2fb", "a+b", "a~b",
		"a\nb", "a\rb", "a\x00b", "a\x1b[2Jb", "a‮b", "naïve", "ａ",
		"x.git", "x.GIT", ".git",
	} {
		if ValidRepo(bad) {
			t.Errorf("%q should be refused as a repo", bad)
		}
	}
}
