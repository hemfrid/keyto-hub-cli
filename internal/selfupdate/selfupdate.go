// Package selfupdate gives the CLI a lightweight "a newer version is
// available" nudge. It queries the GitHub releases API for the latest tag,
// compares it to the running version, and prints an update hint — throttled to
// at most once per day and persisted in ~/.keyto so the common case is a
// zero-network, instant cache read. It is fail-silent: any network/parse/FS
// error simply means no nudge, never a broken command.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
)

const (
	// DefaultAPI is the GitHub releases endpoint for the latest published release.
	DefaultAPI = "https://api.github.com/repos/hemfrid/keyto-hub-cli/releases/latest"

	checkTTL    = 24 * time.Hour
	httpTimeout = 1500 * time.Millisecond
	cacheFile   = "update-check.json"
)

// cache is the persisted update-check state in ~/.keyto/update-check.json.
type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// MaybeNotify checks (throttled, fail-silent) whether a newer release exists
// and writes a one-line nudge to out. It returns immediately for dev builds,
// non-interactive sessions, or when the running version is up to date. The
// network is hit at most once per checkTTL; otherwise the cached tag is used.
func MaybeNotify(version string, interactive bool, out io.Writer) {
	if !interactive || version == "" || version == "dev" {
		return
	}

	path := filepath.Join(config.Dir(), cacheFile)
	c, _ := loadCache(path)

	latest := c.Latest
	if needsRefresh(c.CheckedAt, time.Now()) {
		ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
		defer cancel()
		if tag, err := LatestTag(ctx, http.DefaultClient, DefaultAPI); err == nil && tag != "" {
			latest = tag
			_ = saveCache(path, cache{CheckedAt: time.Now(), Latest: tag})
		}
	}

	if latest != "" && IsNewer(version, latest) {
		fmt.Fprint(out, Notice(version, latest))
	}
}

// LatestTag fetches the latest release tag (e.g. "v0.1.2") from the GitHub
// releases API. The client and URL are injectable for testing.
func LatestTag(ctx context.Context, client *http.Client, apiURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "keyto-cli")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}

	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.TagName, nil
}

// IsNewer reports whether latest is a strictly higher semantic version than
// current. Both are "vMAJOR.MINOR.PATCH" (with optional leading "v" and
// optional pre-release/build suffix, which is ignored). If either is
// unparseable, it returns false — we never nag on uncertainty.
func IsNewer(current, latest string) bool {
	cMaj, cMin, cPatch, ok1 := parseSemver(current)
	lMaj, lMin, lPatch, ok2 := parseSemver(latest)
	if !ok1 || !ok2 {
		return false
	}
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPatch > cPatch
}

// Notice formats the update nudge.
func Notice(current, latest string) string {
	return fmt.Sprintf("\nA new keyto is available: %s (you have %s)\n  Update:  keyto update\n", latest, current)
}

// parseSemver parses "vX.Y.Z" (leading "v" optional; pre-release/build metadata
// after a '-' or '+' is dropped) into its numeric components.
func parseSemver(s string) (maj, min, patch int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// needsRefresh reports whether the cached check is missing or older than
// checkTTL relative to now (so we hit the network at most once per day).
func needsRefresh(checkedAt, now time.Time) bool {
	return checkedAt.IsZero() || now.Sub(checkedAt) > checkTTL
}

func loadCache(path string) (cache, error) {
	var c cache
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

func saveCache(path string, c cache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
