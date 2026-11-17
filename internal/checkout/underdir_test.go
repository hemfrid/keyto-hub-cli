package checkout

import (
	"path/filepath"
	"testing"
)

// The second check on the default clone folder: whatever the name rule lets
// through, the joined path has to end up strictly inside cwd.
func TestUnderDir(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "tmp", "cwd")
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"a folder directly inside", filepath.Join(base, "acme-web"), true},
		{"a nested folder", filepath.Join(base, "a", "b"), true},
		{"the directory itself", base, false},
		{"its parent", filepath.Dir(base), false},
		{"a sibling", filepath.Join(filepath.Dir(base), "x"), false},
		{"a sibling whose name starts the same", base + "-other", false},
		{"dot-dot that climbs out", base + string(filepath.Separator) + ".." + string(filepath.Separator) + "x", false},
		{"dot-dot that stays inside", base + string(filepath.Separator) + "a" + string(filepath.Separator) + ".." + string(filepath.Separator) + "b", true},
		{"another root", filepath.Join(string(filepath.Separator), "etc", "x"), false},
		{"a relative path", "acme-web", false},
		{"a folder named with two leading dots", filepath.Join(base, "..x"), true},
	}
	for _, c := range cases {
		if got := underDir(base, c.path); got != c.want {
			t.Errorf("%s: underDir(%q, %q) = %v, want %v", c.name, base, c.path, got, c.want)
		}
	}
}

func TestDefaultCheckoutDir_RefusesWhenTheJoinLeavesCwd(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "tmp", "cwd")
	if dir, err := defaultCheckoutDir(cwd, "acme-web"); err != nil || dir != filepath.Join(cwd, "acme-web") {
		t.Errorf("got %q, %v", dir, err)
	}
	// Names the name rule already refuses, and one it would not if it were ever
	// loosened: both checks have to hold.
	for _, name := range []string{"../x", "..", ".", "a/../../x", ""} {
		if dir, err := defaultCheckoutDir(cwd, name); err == nil {
			t.Errorf("name %q: want an error, got %q", name, dir)
		}
	}
}

func TestDefaultCheckoutDir_TheJoinCheckHoldsWithoutTheNameRule(t *testing.T) {
	// If the name rule were ever loosened, the join check is what is left.
	orig := folderNameOK
	folderNameOK = func(string) bool { return true }
	t.Cleanup(func() { folderNameOK = orig })

	cwd := filepath.Join(string(filepath.Separator), "tmp", "cwd")
	for _, name := range []string{"../x", "..", ".", "", "a/../../x", "../cwd-other", "../../etc/x"} {
		if dir, err := defaultCheckoutDir(cwd, name); err == nil {
			t.Errorf("name %q: want an error from the join check, got %q", name, dir)
		}
	}
	// A nested name stays inside cwd, so the join check alone lets it through.
	if dir, err := defaultCheckoutDir(cwd, "a/b"); err != nil || dir != filepath.Join(cwd, "a", "b") {
		t.Errorf("got %q, %v", dir, err)
	}
}
