package selfupdate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// MaybeNotify reads the latest tag from ~/.keyto/update-check.json, a file
// on disk that anything running as the user can write. A cached tag that is
// not vX.Y.Z is never printed. (A fresh cache, so no network call is made.)
func TestMaybeNotify_NeverPrintsACachedTagThatIsNotAVersion(t *testing.T) {
	for _, latest := range []string{
		"v9.9.9-\x1b]52;c;ZXZpbA==\x07", // parses as v9.9.9 (the suffix is cut), so it would print
		"v9.9.9+\x1b[2J",
		"v9.9.9-x‮",
	} {
		home := t.TempDir()
		t.Setenv("KEYTO_HOME", home)
		if err := saveCache(filepath.Join(home, cacheFile), cache{CheckedAt: time.Now(), Latest: latest}); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		MaybeNotify("v0.1.0", true, &out)
		if out.Len() != 0 {
			t.Errorf("cached tag %q: printed %q", latest, out.String())
		}
	}
}

func TestMaybeNotify_PrintsAValidNewerCachedTag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KEYTO_HOME", home)
	if err := saveCache(filepath.Join(home, cacheFile), cache{CheckedAt: time.Now(), Latest: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	MaybeNotify("v0.1.0", true, &out)
	if !strings.Contains(out.String(), "v9.9.9") {
		t.Errorf("want a notice for v9.9.9, got %q", out.String())
	}
}
