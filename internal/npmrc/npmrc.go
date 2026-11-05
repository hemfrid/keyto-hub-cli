// Package npmrc keeps the keyto credential for the Hub's npm package lane
// (keyto-hub `GET /api/npm/@hemfrid/<name>`, plan 1d Task 18/21) in the
// user's ~/.npmrc, so `npm ci` in a Workers checkout can install the private
// @hemfrid/workers-* packages without a GitHub token.
//
// It owns exactly one line, npm's host-scoped auth line for the lane, kept
// between a begin and an end marker so later runs can find and rewrite it (a
// rotated credential). That line only tells npm which token to send to the
// Hub's /api/npm/ prefix; it changes no registry, so no other repo on the
// machine resolves anything differently. Which packages go to the Hub is the
// project's choice (its own .npmrc scope line), and `keyto start` passes the
// scope to its own npm install as a command-line flag (ScopeFlag).
//
// v0.9.x before this wrote a user-wide `@hemfrid:registry=<hub>/api/npm/` line
// inside the same markers. That sent every @hemfrid package on the laptop to a
// lane that serves two of them, so Ensure removes it wherever it finds it
// inside keyto's markers, even when it can't write the credential. Every line
// outside the markers stays byte-for-byte as it was. The file holds a token, so
// it is written through internal/safefile: a 0600 temp file renamed into
// place, and a symlinked ~/.npmrc is refused, never written through. The token
// is never printed.
package npmrc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/safefile"
)

// The markers around keyto's lines. Matching is on these prefixes (after
// trimming whitespace), so the wording after them may change between releases.
// The begin marker's text is the one v0.9.x wrote, so a block cleaned of its
// legacy scope line needs no rewrite (and no probe) to be current.
const (
	BeginMarker = "# keyto-managed begin: @hemfrid package lane (keyto start rewrites the lines up to the end marker)"
	EndMarker   = "# keyto-managed end"

	beginPrefix = "# keyto-managed begin"
	lanePath    = "/api/npm/"
	filePerm    = 0o600

	// ScopeKey is the npm config key that maps the @hemfrid scope to a
	// registry. keyto never writes it to the user config any more.
	ScopeKey = "@hemfrid:registry"
)

// Deps are Ensure's inputs. Probe reports whether the Hub serves the lane
// (detail says why not); it is consulted only when the credential line would
// change.
type Deps struct {
	Path  string
	Creds *config.Creds
	Probe func(ctx context.Context) (served bool, detail string)
	Out   io.Writer
}

// UserConfigPath is where npm reads the user config: $npm_config_userconfig
// or $NPM_CONFIG_USERCONFIG when set, else <home>/.npmrc.
func UserConfigPath(getenv func(string) string, home string) string {
	for _, k := range []string{"npm_config_userconfig", "NPM_CONFIG_USERCONFIG"} {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return filepath.Join(home, ".npmrc")
}

// ScopeFlag is the npm command-line flag that maps @hemfrid to registry for
// one npm run. A flag outranks the project and user .npmrc files, so it reaches
// only the install keyto runs. (Not `npm_config_@hemfrid:registry` in the
// environment: a name with "@" and ":" is dropped by dash, so an npm shim run
// through /bin/sh on Linux would never see it.)
func ScopeFlag(registry string) string {
	return "--" + ScopeKey + "=" + registry
}

// laneLines is the managed line: npm's "nerf dart" auth line for the lane's
// prefix (tarball URLs sit under it too, so npm sends the token on both), and
// the registry URL it belongs to. The auth line mirrors the second line of
// keyto-hub's buildNpmLaneNpmrc.
type laneLines struct {
	registry string
	authKey  string // "//host/api/npm/:_authToken"
	token    string
}

func newLaneLines(hubURL, token string) (laneLines, error) {
	u, err := url.Parse(strings.TrimSpace(hubURL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return laneLines{}, fmt.Errorf("the Hub URL %q is not an http(s) URL", hubURL)
	}
	return laneLines{
		registry: u.Scheme + "://" + u.Host + lanePath,
		authKey:  "//" + u.Host + lanePath + ":_authToken",
		token:    token,
	}, nil
}

func (l laneLines) block() string {
	return BeginMarker + "\n" +
		l.authKey + "=" + l.token + "\n" +
		EndMarker + "\n"
}

// handWritten is the line with the token replaced by where to find it, for a
// tip the user can follow without the CLI printing the secret.
func (l laneLines) handWritten() string {
	tokenHint := fmt.Sprintf(`<the "credential" value in %s>`, filepath.Join(config.Dir(), "credentials"))
	return "    " + l.authKey + "=" + tokenHint
}

// tokenOK refuses a token that could end its line early or smuggle in another
// npm setting: it must be non-empty printable ASCII with no whitespace.
func tokenOK(tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// credsLines checks the credential and builds the managed line from it. The
// error is the tip Ensure returns.
func credsLines(c *config.Creds) (laneLines, error) {
	switch {
	case c == nil || c.Credential == "":
		return laneLines{}, errors.New("no keyto credential, so ~/.npmrc can't hold it for the Hub package lane — run `keyto auth`, then `keyto start` again")
	case c.Expired():
		return laneLines{}, errors.New("your sign-in has expired, so ~/.npmrc can't hold it for the Hub package lane — run `keyto auth`, then `keyto start` again")
	case !tokenOK(c.Credential):
		return laneLines{}, errors.New("your keyto credential has characters an .npmrc line can't hold; ~/.npmrc left unchanged — run `keyto auth --force`")
	}
	lines, err := newLaneLines(c.HubURL, c.Credential)
	if err != nil {
		return laneLines{}, fmt.Errorf("%w; ~/.npmrc left unchanged", err)
	}
	return lines, nil
}

// Ensure makes the managed block in d.Path hold the user's credential for the
// lane, and removes the user-wide scope line earlier releases put inside it.
// It returns the lane's registry URL when the credential line is in place
// (the caller passes it to npm with ScopeFlag), else "".
//
// The error is a tip for the user, never fatal to `keyto start`: no
// credential, a symlinked or unreadable file, or a block with no end marker.
// A Hub that doesn't serve the lane is reported on d.Out and returns "", nil.
func Ensure(ctx context.Context, d Deps) (string, error) {
	lines, credsErr := credsLines(d.Creds)

	existing, _, err := safefile.ReadRegular(d.Path)
	if err != nil {
		if credsErr != nil {
			return "", credsErr
		}
		switch {
		case errors.Is(err, safefile.ErrSymlink):
			return "", fmt.Errorf("%s is a symlink, and keyto never writes through one. To use the Hub package lane, add this line to the file it points at yourself:\n%s",
				d.Path, lines.handWritten())
		case errors.Is(err, safefile.ErrNotRegular):
			return "", fmt.Errorf("%s is not a regular file, so keyto left it alone. To use the Hub package lane, add this line to your npm user config yourself:\n%s",
				d.Path, lines.handWritten())
		}
		return "", err
	}

	cleaned, removed, err := dropLegacyScope(string(existing))
	if err != nil {
		return "", fmt.Errorf("%s: %w; fix it by hand and re-run", d.Path, err)
	}
	if credsErr != nil {
		if err := cleanUp(d, cleaned, removed); err != nil {
			return "", err
		}
		return "", credsErr
	}

	updated, shadowed, err := apply(cleaned, lines)
	if err != nil {
		return "", fmt.Errorf("%s: %w; fix it by hand and re-run", d.Path, err)
	}
	if updated == cleaned {
		if err := cleanUp(d, cleaned, removed); err != nil {
			return "", err
		}
		fmt.Fprintf(d.Out, "  %s already holds your keyto credential for %s\n", d.Path, lines.registry)
		noteShadowed(d, shadowed)
		return lines.registry, nil
	}
	if served, detail := d.Probe(ctx); !served {
		if err := cleanUp(d, cleaned, removed); err != nil {
			return "", err
		}
		fmt.Fprintf(d.Out, "  this Hub doesn't serve the @hemfrid package lane yet (%s); the credential line in %s left unchanged\n", detail, d.Path)
		return "", nil
	}
	if err := safefile.Write(d.Path, []byte(updated), filePerm); err != nil {
		return "", fmt.Errorf("could not update %s for the Hub package lane: %w", d.Path, err)
	}
	fmt.Fprintf(d.Out, "  %s: your keyto credential is now sent to %s, and to no other registry\n", d.Path, lines.registry)
	noteRemoved(d, removed)
	noteShadowed(d, shadowed)
	return lines.registry, nil
}

// cleanUp writes content when dropLegacyScope removed something, and says so.
func cleanUp(d Deps, content string, removed int) error {
	if removed == 0 {
		return nil
	}
	if err := safefile.Write(d.Path, []byte(content), filePerm); err != nil {
		return fmt.Errorf("could not remove the %s line an earlier keyto wrote to %s: %w", ScopeKey, d.Path, err)
	}
	noteRemoved(d, removed)
	return nil
}

func noteRemoved(d Deps, removed int) {
	if removed == 0 {
		return
	}
	fmt.Fprintf(d.Out, "  removed the user-wide %s line an earlier keyto wrote to %s: it sent every @hemfrid package to the Hub, which serves only the Workers ones\n",
		ScopeKey, d.Path)
}

func noteShadowed(d Deps, lineNos []int) {
	if len(lineNos) == 0 {
		return
	}
	nums := make([]string, len(lineNos))
	for i, n := range lineNos {
		nums[i] = strconv.Itoa(n)
	}
	fmt.Fprintf(d.Out, "  note: line %s of %s set the same key after keyto's block, and npm uses the last one — remove them to use the package lane\n",
		strings.Join(nums, ", "), d.Path)
}

// splitLines splits content into lines that keep their line endings, with no
// empty last element.
func splitLines(content string) []string {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// blockEnd returns the index of the end marker closing the block that begins
// at lines[begin], or an error naming the begin marker's line.
func blockEnd(lines []string, begin int) (int, error) {
	for end := begin + 1; end < len(lines); end++ {
		if isMarker(lines[end], EndMarker) {
			return end, nil
		}
	}
	return 0, fmt.Errorf("keyto's begin marker on line %d has no %q line after it", begin+1, EndMarker)
}

// dropLegacyScope removes the @hemfrid scope lines inside keyto's markers (the
// line v0.9.x wrote there) and returns how many it removed. Lines outside the
// markers are never touched, a user's own scope line included.
func dropLegacyScope(content string) (string, int, error) {
	lines := splitLines(content)
	var out []string
	removed := 0
	for i := 0; i < len(lines); i++ {
		if !isMarker(lines[i], beginPrefix) {
			out = append(out, lines[i])
			continue
		}
		end, err := blockEnd(lines, i)
		if err != nil {
			return "", 0, err
		}
		for j := i; j <= end; j++ {
			if lineKey(lines[j]) == ScopeKey {
				removed++
				continue
			}
			out = append(out, lines[j])
		}
		i = end
	}
	if removed == 0 {
		return content, 0, nil
	}
	return strings.Join(out, ""), removed, nil
}

// apply returns content with the managed block in place of the first existing
// one (dropping any others) or appended at the end, and the 1-based line
// numbers, in the result, of foreign lines after the block that set the
// managed key (npm takes the last value, so those win over keyto's).
func apply(content string, l laneLines) (string, []int, error) {
	lines := splitLines(content)
	var out []string
	placed := false
	for i := 0; i < len(lines); i++ {
		if !isMarker(lines[i], beginPrefix) {
			out = append(out, lines[i])
			continue
		}
		end, err := blockEnd(lines, i)
		if err != nil {
			return "", nil, err
		}
		if !placed {
			out = append(out, l.block())
			placed = true
		}
		i = end
	}
	if !placed {
		if n := len(out); n > 0 && !strings.HasSuffix(out[n-1], "\n") {
			out[n-1] += "\n"
		}
		out = append(out, l.block())
	}
	return strings.Join(out, ""), shadowedLines(out, l), nil
}

// shadowedLines numbers the foreign lines after the block that set the lane's
// auth key. out holds the block as one element.
func shadowedLines(out []string, l laneLines) []int {
	var nos []int
	lineNo, after := 0, false
	for _, chunk := range out {
		if strings.HasPrefix(chunk, BeginMarker) {
			lineNo += strings.Count(chunk, "\n")
			after = true
			continue
		}
		lineNo++
		if after && lineKey(chunk) == l.authKey {
			nos = append(nos, lineNo)
		}
	}
	return nos
}

// lineKey is the key of an npmrc `key = value` line, or "" for any other line.
func lineKey(line string) string {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

func isMarker(line, prefix string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), prefix)
}
