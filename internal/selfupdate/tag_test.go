package selfupdate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// hostileTags are release tags a compromised or misbehaving release API could
// return: path traversal into the download URL, terminal escape sequences, and
// near-misses of vX.Y.Z.
var hostileTags = []string{
	"",
	"v0.2.0/../../../evil",
	"../v0.2.0",
	"v0.2.0\x1b]52;c;ZXZpbA==\x07",
	"v0.2.0\x1b[2J",
	"v0.2.0\n",
	" v0.2.0",
	"v0.2.0?x=1",
	"v0.2.0#x",
	"v0.2",
	"0.2.0",
	"v0.2.0-rc.1",
	"V0.2.0",
	"v0.2.0‮",
	"v1234567.0.0",
}

func latestServer(t *testing.T, tag string, downloads *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
			return
		}
		atomic.AddInt32(downloads, 1)
		http.NotFound(w, r)
	}))
}

func TestLatestTag_RefusesAnythingButVXYZ_WithoutEchoingIt(t *testing.T) {
	for _, tag := range hostileTags {
		var downloads int32
		srv := latestServer(t, tag, &downloads)
		got, err := LatestTag(context.Background(), srv.Client(), srv.URL+"/latest")
		srv.Close()
		if err == nil {
			t.Errorf("tag %q: want an error, got %q", tag, got)
			continue
		}
		if got != "" {
			t.Errorf("tag %q: returned %q alongside the error", tag, got)
		}
		if tag != "" && strings.Contains(err.Error(), tag) {
			t.Errorf("tag %q: the error echoes the server's tag: %q", tag, err.Error())
		}
		if strings.ContainsAny(err.Error(), "\x1b\n‮") {
			t.Errorf("tag %q: the error carries control characters: %q", tag, err.Error())
		}
	}
}

func TestLatestTag_AcceptsVXYZ(t *testing.T) {
	for _, tag := range []string{"v0.2.0", "v0.9.2", "v10.20.30"} {
		var downloads int32
		srv := latestServer(t, tag, &downloads)
		got, err := LatestTag(context.Background(), srv.Client(), srv.URL+"/latest")
		srv.Close()
		if err != nil || got != tag {
			t.Errorf("tag %q: got %q, %v", tag, got, err)
		}
	}
}

func TestUpdate_HostileTag_DownloadsNothingAndPrintsNothingOfIt(t *testing.T) {
	for _, tag := range []string{"v0.2.0/../../../evil", "v0.2.0\x1b]52;c;ZXZpbA==\x07"} {
		var downloads int32
		srv := latestServer(t, tag, &downloads)
		exe := filepath.Join(t.TempDir(), "keyto")
		if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		_, err := Update(context.Background(), UpdateConfig{
			Current: "v0.1.0", GOOS: "darwin", GOARCH: "arm64",
			HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
			ExePath: exe, Out: io.Writer(&out),
		})
		srv.Close()
		if err == nil {
			t.Errorf("tag %q: want an error", tag)
		}
		if n := atomic.LoadInt32(&downloads); n != 0 {
			t.Errorf("tag %q: %d download request(s) made", tag, n)
		}
		if strings.Contains(out.String(), tag) || (err != nil && strings.Contains(err.Error(), tag)) {
			t.Errorf("tag %q: printed: out=%q err=%v", tag, out.String(), err)
		}
		if got, _ := os.ReadFile(exe); string(got) != "old" {
			t.Errorf("tag %q: binary changed", tag)
		}
	}
}
