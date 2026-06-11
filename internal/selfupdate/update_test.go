package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"darwin", "arm64", "keyto_darwin_arm64", false},
		{"darwin", "amd64", "keyto_darwin_amd64", false},
		{"linux", "amd64", "keyto_linux_amd64", false},
		{"windows", "amd64", "keyto_windows_amd64.exe", false},
		{"linux", "arm64", "", true},   // not built by the release workflow
		{"windows", "arm64", "", true}, // not built
		{"plan9", "amd64", "", true},
	}
	for _, c := range cases {
		got, err := assetName(c.goos, c.goarch)
		if c.wantErr {
			if err == nil {
				t.Errorf("assetName(%q,%q) expected error, got %q", c.goos, c.goarch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("assetName(%q,%q) unexpected error: %v", c.goos, c.goarch, err)
		}
		if got != c.want {
			t.Errorf("assetName(%q,%q) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	// sha256sum format: "<hex>  <name>" (two spaces). Include a couple of files.
	data := "" +
		"aaaa1111  keyto_darwin_amd64\n" +
		"bbbb2222  keyto_darwin_arm64\n" +
		"cccc3333  keyto_windows_amd64.exe\n"

	got, err := parseChecksum(data, "keyto_darwin_arm64")
	if err != nil {
		t.Fatalf("parseChecksum: %v", err)
	}
	if got != "bbbb2222" {
		t.Errorf("parseChecksum = %q, want bbbb2222", got)
	}

	if _, err := parseChecksum(data, "keyto_linux_amd64"); err == nil {
		t.Error("expected error for an asset absent from the checksums, got nil")
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("the downloaded binary bytes")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])

	if err := verifyChecksum(data, good); err != nil {
		t.Errorf("verifyChecksum with correct hash: %v", err)
	}
	// Case-insensitive hex must still match.
	if err := verifyChecksum(data, "ABCD"); err == nil {
		t.Error("verifyChecksum expected mismatch error, got nil")
	}
}

func TestReplaceExecutable_SwapsContentsLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyto")
	if err := os.WriteFile(path, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable(path, []byte("NEW BINARY"), 0o755); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW BINARY" {
		t.Errorf("contents = %q, want %q", got, "NEW BINARY")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("expected the executable bit set, got %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the binary in dir, found leftovers: %v", entries)
	}
}

// releaseServer stands in for GitHub: the latest-release API, the per-platform
// binary asset, and checksums.txt. binPath/sumPath are the download paths it
// serves under DownloadBase.
func releaseServer(t *testing.T, tag, asset string, bin []byte, checksums string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case "/dl/" + tag + "/" + asset:
			_, _ = w.Write(bin)
		case "/dl/" + tag + "/checksums.txt":
			_, _ = io.WriteString(w, checksums)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestUpdate_DownloadsVerifiesAndReplaces(t *testing.T) {
	const tag, goos, goarch = "v0.2.0", "darwin", "arm64"
	asset := "keyto_darwin_arm64"
	newBin := []byte("brand new keyto v0.2.0 binary payload")
	sum := sha256.Sum256(newBin)
	checks := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	srv := releaseServer(t, tag, asset, newBin, checks)
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "keyto")
	if err := os.WriteFile(exe, []byte("old v0.1.0 binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Update(context.Background(), UpdateConfig{
		Current: "v0.1.0", GOOS: goos, GOARCH: goarch,
		HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
		ExePath: exe, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Updated || res.From != "v0.1.0" || res.To != "v0.2.0" {
		t.Errorf("result = %+v, want {From:v0.1.0 To:v0.2.0 Updated:true}", res)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, newBin) {
		t.Errorf("binary not replaced with the downloaded payload; got %q", got)
	}
}

func TestUpdate_AlreadyLatest_LeavesBinaryUntouched(t *testing.T) {
	const tag = "v0.2.0"
	// Download paths intentionally 404 — if Update tries to fetch, it errors,
	// proving the up-to-date short-circuit didn't fire.
	srv := releaseServer(t, tag, "unused", nil, "")
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "keyto")
	_ = os.WriteFile(exe, []byte("v0.2.0 binary"), 0o755)

	res, err := Update(context.Background(), UpdateConfig{
		Current: "v0.2.0", GOOS: "darwin", GOARCH: "arm64",
		HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
		ExePath: exe, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Updated {
		t.Errorf("expected Updated=false when already on latest, got %+v", res)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "v0.2.0 binary" {
		t.Errorf("binary should be untouched, got %q", got)
	}
}

func TestUpdate_ChecksumMismatch_DoesNotReplace(t *testing.T) {
	const tag, asset = "v0.2.0", "keyto_darwin_arm64"
	newBin := []byte("payload that will fail verification")
	// Wrong checksum on purpose.
	checks := fmt.Sprintf("%s  %s\n", "00000000000000000000000000000000", asset)

	srv := releaseServer(t, tag, asset, newBin, checks)
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "keyto")
	_ = os.WriteFile(exe, []byte("ORIGINAL"), 0o755)

	_, err := Update(context.Background(), UpdateConfig{
		Current: "v0.1.0", GOOS: "darwin", GOARCH: "arm64",
		HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
		ExePath: exe, Out: io.Discard,
	})
	if err == nil {
		t.Fatal("expected a checksum-mismatch error, got nil")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "ORIGINAL" {
		t.Errorf("binary must not be replaced on checksum failure; got %q", got)
	}
}

func TestUpdate_UnsupportedPlatform_Errors(t *testing.T) {
	srv := releaseServer(t, "v0.2.0", "unused", nil, "")
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "keyto")
	_ = os.WriteFile(exe, []byte("ORIGINAL"), 0o755)

	_, err := Update(context.Background(), UpdateConfig{
		Current: "v0.1.0", GOOS: "linux", GOARCH: "arm64",
		HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
		ExePath: exe, Out: io.Discard,
	})
	if err == nil {
		t.Fatal("expected an unsupported-platform error, got nil")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "ORIGINAL" {
		t.Errorf("binary must be untouched on unsupported platform; got %q", got)
	}
}

func TestUpdate_DevBuild_InstallsLatest(t *testing.T) {
	const tag, asset = "v0.2.0", "keyto_darwin_arm64"
	newBin := []byte("latest release for a dev build")
	sum := sha256.Sum256(newBin)
	checks := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	srv := releaseServer(t, tag, asset, newBin, checks)
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "keyto")
	_ = os.WriteFile(exe, []byte("dev"), 0o755)

	res, err := Update(context.Background(), UpdateConfig{
		Current: "dev", GOOS: "darwin", GOARCH: "arm64",
		HTTPClient: srv.Client(), APIURL: srv.URL + "/latest", DownloadBase: srv.URL + "/dl",
		ExePath: exe, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Updated || res.To != tag {
		t.Errorf("dev build should install the latest release; got %+v", res)
	}
}
