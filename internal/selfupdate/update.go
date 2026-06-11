package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultDownloadBase is the GitHub release-asset download base. The per-tag
	// asset URL is DownloadBase + "/" + tag + "/" + assetName.
	DefaultDownloadBase = "https://github.com/hemfrid/keyto-hub-cli/releases/download"
	// releasesPage is shown when there is no prebuilt binary for the host.
	releasesPage = "https://github.com/hemfrid/keyto-hub-cli/releases"
	// downloadTimeout caps the whole update HTTP round-trip (tag + binary + sums).
	downloadTimeout = 60 * time.Second
	// maxDownload bounds an asset read so a misbehaving URL can't exhaust memory.
	maxDownload = 256 << 20 // 256 MiB
)

// supportedTargets is the set of GOOS/GOARCH the release workflow builds. Keep
// this in sync with .github/workflows/release.yml.
var supportedTargets = map[string]bool{
	"darwin/amd64":  true,
	"darwin/arm64":  true,
	"linux/amd64":   true,
	"windows/amd64": true,
}

// UpdateConfig parameterises Update. All fields are injectable so the whole
// flow can be exercised against an httptest server in tests.
type UpdateConfig struct {
	Current      string       // running version, e.g. "v0.1.6" (or "dev")
	GOOS         string       // target OS for asset selection (usually runtime.GOOS)
	GOARCH       string       // target arch for asset selection
	HTTPClient   *http.Client // injected for tests
	APIURL       string       // latest-release endpoint (DefaultAPI in production)
	DownloadBase string       // release-asset base (DefaultDownloadBase in production)
	ExePath      string       // path of the binary to replace (resolved, no symlinks)
	Out          io.Writer    // progress messages
}

// UpdateResult reports the outcome of an update attempt.
type UpdateResult struct {
	From    string // version we were on
	To      string // latest release tag
	Updated bool   // false when already current (no download performed)
}

// Update fetches the latest release, verifies its sha256 against the published
// checksums.txt, and atomically replaces the running binary. It is a no-op
// (Updated=false) when the current version already matches the latest release.
// A "dev"/unparseable current version always installs the latest.
func Update(ctx context.Context, cfg UpdateConfig) (UpdateResult, error) {
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}

	tag, err := LatestTag(ctx, cfg.HTTPClient, cfg.APIURL)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("update: fetch latest release: %w", err)
	}
	res := UpdateResult{From: cfg.Current, To: tag}

	// If the current version parses and is not older than the latest, we're done.
	// An unparseable current (e.g. "dev") falls through and installs the latest.
	if _, _, _, ok := parseSemver(cfg.Current); ok && !IsNewer(cfg.Current, tag) {
		return res, nil
	}

	asset, err := assetName(cfg.GOOS, cfg.GOARCH)
	if err != nil {
		return res, err
	}

	fmt.Fprintf(out, "Downloading keyto %s (%s)…\n", tag, asset)
	bin, err := download(ctx, cfg.HTTPClient, downloadURL(cfg.DownloadBase, tag, asset))
	if err != nil {
		return res, fmt.Errorf("update: download %s: %w", asset, err)
	}
	sums, err := download(ctx, cfg.HTTPClient, downloadURL(cfg.DownloadBase, tag, "checksums.txt"))
	if err != nil {
		return res, fmt.Errorf("update: download checksums: %w", err)
	}

	want, err := parseChecksum(string(sums), asset)
	if err != nil {
		return res, err
	}
	if err := verifyChecksum(bin, want); err != nil {
		return res, err
	}

	perm := fs.FileMode(0o755)
	if fi, statErr := os.Stat(cfg.ExePath); statErr == nil {
		perm = fi.Mode().Perm() | 0o111 // keep existing bits, ensure executable
	}
	if err := replaceExecutable(cfg.ExePath, bin, perm); err != nil {
		return res, fmt.Errorf("update: replace binary at %s: %w", cfg.ExePath, err)
	}

	res.Updated = true
	return res, nil
}

// Run is the `keyto update` entry point: it resolves the running binary,
// fills production defaults, performs the update, and prints the result.
func Run(ctx context.Context, version string, out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: locate current binary: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a symlink (e.g. a
	// Homebrew/asdf shim or ~/.local/bin link).
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	res, err := Update(ctx, UpdateConfig{
		Current:      version,
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		HTTPClient:   &http.Client{Timeout: downloadTimeout},
		APIURL:       DefaultAPI,
		DownloadBase: DefaultDownloadBase,
		ExePath:      exe,
		Out:          out,
	})
	if err != nil {
		return err
	}
	if !res.Updated {
		fmt.Fprintf(out, "keyto is already up to date (%s).\n", res.To)
		return nil
	}
	fmt.Fprintf(out, "Updated keyto %s → %s\n", res.From, res.To)
	return nil
}

// assetName maps a GOOS/GOARCH to its release asset filename, matching the
// names produced by the release workflow. It errors for targets that are not
// built (so the caller can point the user at the releases page).
func assetName(goos, goarch string) (string, error) {
	if !supportedTargets[goos+"/"+goarch] {
		return "", fmt.Errorf("update: no prebuilt keyto binary for %s/%s — see %s", goos, goarch, releasesPage)
	}
	name := fmt.Sprintf("keyto_%s_%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name, nil
}

// downloadURL builds the release-asset URL for a tag and asset filename.
func downloadURL(base, tag, asset string) string {
	return strings.TrimRight(base, "/") + "/" + tag + "/" + asset
}

// download GETs url and returns the body, bounded by maxDownload.
func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "keyto-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// parseChecksum returns the lowercase hex sha256 for asset from sha256sum-style
// "<hex>  <name>" lines (tolerating one or two spaces and a binary "*" prefix).
func parseChecksum(data, asset string) (string, error) {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("update: no checksum for %s in checksums.txt", asset)
}

// verifyChecksum confirms data hashes to want (hex sha256, case-insensitive).
func verifyChecksum(data []byte, want string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("update: checksum mismatch (got %s, want %s) — refusing to install", got, want)
	}
	return nil
}

// replaceExecutable atomically swaps the file at path with data. It writes a
// temp file in the same directory (so the final rename is atomic on one
// filesystem), then renames it into place.
//
// On Windows a running .exe cannot be overwritten, but it CAN be renamed: we
// move the current binary aside to "<path>.old", put the new one in place, and
// best-effort delete the old (which may still be locked until the process
// exits — Windows will let it be removed on the next run).
func replaceExecutable(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".keyto-update-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return err
	}

	if runtime.GOOS == "windows" {
		old := path + ".old"
		_ = os.Remove(old) // clear a stale one from a previous update
		if err := os.Rename(path, old); err != nil {
			cleanup()
			return err
		}
		if err := os.Rename(tmpName, path); err != nil {
			_ = os.Rename(old, path) // roll back
			cleanup()
			return err
		}
		_ = os.Remove(old) // best-effort; ignored if still locked
		return nil
	}

	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
