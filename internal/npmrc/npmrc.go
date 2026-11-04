// Package npmrc points the @hemfrid npm scope at the Hub's package lane in the
// user's ~/.npmrc, so `npm ci` in a Workers checkout can install the private
// @hemfrid/workers-* packages with the keyto credential instead of a GitHub
// token (keyto-hub `GET /api/npm/@hemfrid/<name>`, plan 1d Task 18/21).
//
// It owns exactly two lines, kept between a begin and an end marker so later
// runs can find and rewrite them (a rotated credential). Every other line of
// the file stays byte-for-byte as it was. The file holds a token, so it is
// written through internal/safefile: a 0600 temp file renamed into place, and a
// symlinked ~/.npmrc is refused, never written through. The token is never
// printed.
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
const (
	BeginMarker = "# keyto-managed begin: @hemfrid package lane (keyto start rewrites the lines up to the end marker)"
	EndMarker   = "# keyto-managed end"

	beginPrefix = "# keyto-managed begin"
	lanePath    = "/api/npm/"
	scopeKey    = "@hemfrid:registry"
	filePerm    = 0o600
)

// Deps are Ensure's inputs. Probe reports whether the Hub serves the lane
// (detail says why not); it is consulted only when the file would change.
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

// laneLines are the two managed lines: the scope registry, and npm's
// "nerf dart" auth line for the same prefix (tarball URLs sit under it too, so
// npm sends the token on both). They mirror keyto-hub's buildNpmLaneNpmrc.
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
		scopeKey + "=" + l.registry + "\n" +
		l.authKey + "=" + l.token + "\n" +
		EndMarker + "\n"
}

// handWritten is the block with the token replaced by where to find it, for
// a tip the user can follow without the CLI printing the secret.
func (l laneLines) handWritten() string {
	tokenHint := fmt.Sprintf(`<the "credential" value in %s>`, filepath.Join(config.Dir(), "credentials"))
	return "    " + scopeKey + "=" + l.registry + "\n    " + l.authKey + "=" + tokenHint
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

// Ensure makes the managed block in d.Path match the user's credential. The
// error it returns is a tip for the user, never fatal to `keyto start`: no
// credential, a symlinked or unreadable file, or a block with no end marker.
// A Hub that doesn't serve the lane is reported on d.Out and returns nil.
func Ensure(ctx context.Context, d Deps) error {
	switch {
	case d.Creds == nil || d.Creds.Credential == "":
		return errors.New("no keyto credential, so ~/.npmrc can't point @hemfrid at the Hub package lane — run `keyto auth`, then `keyto start` again")
	case d.Creds.Expired():
		return errors.New("your sign-in has expired, so ~/.npmrc can't point @hemfrid at the Hub package lane — run `keyto auth`, then `keyto start` again")
	case !tokenOK(d.Creds.Credential):
		return errors.New("your keyto credential has characters an .npmrc line can't hold; ~/.npmrc left unchanged — run `keyto auth --force`")
	}
	lines, err := newLaneLines(d.Creds.HubURL, d.Creds.Credential)
	if err != nil {
		return fmt.Errorf("%w; ~/.npmrc left unchanged", err)
	}

	existing, _, err := safefile.ReadRegular(d.Path)
	if err != nil {
		switch {
		case errors.Is(err, safefile.ErrSymlink):
			return fmt.Errorf("%s is a symlink, and keyto never writes through one. To use the @hemfrid package lane, add these lines to the file it points at yourself:\n%s",
				d.Path, lines.handWritten())
		case errors.Is(err, safefile.ErrNotRegular):
			return fmt.Errorf("%s is not a regular file, so keyto left it alone. To use the @hemfrid package lane, add these lines to your npm user config yourself:\n%s",
				d.Path, lines.handWritten())
		}
		return err
	}

	updated, shadowed, err := apply(string(existing), lines)
	if err != nil {
		return fmt.Errorf("%s: %w; fix it by hand and re-run", d.Path, err)
	}
	if updated == string(existing) {
		fmt.Fprintf(d.Out, "  %s already points @hemfrid at the Hub package lane\n", d.Path)
		noteShadowed(d, shadowed)
		return nil
	}
	if served, detail := d.Probe(ctx); !served {
		fmt.Fprintf(d.Out, "  this Hub doesn't serve the @hemfrid package lane yet (%s); %s left unchanged\n", detail, d.Path)
		return nil
	}
	if err := safefile.Write(d.Path, []byte(updated), filePerm); err != nil {
		return fmt.Errorf("could not update %s for the @hemfrid package lane: %w", d.Path, err)
	}
	fmt.Fprintf(d.Out, "  %s: @hemfrid now installs from %s with your keyto credential\n", d.Path, lines.registry)
	noteShadowed(d, shadowed)
	return nil
}

func noteShadowed(d Deps, lineNos []int) {
	if len(lineNos) == 0 {
		return
	}
	nums := make([]string, len(lineNos))
	for i, n := range lineNos {
		nums[i] = strconv.Itoa(n)
	}
	fmt.Fprintf(d.Out, "  note: line %s of %s set the same keys after keyto's block, and npm uses the last one — remove them to use the package lane\n",
		strings.Join(nums, ", "), d.Path)
}

// apply returns content with the managed block in place of the first existing
// one (dropping any others) or appended at the end, and the 1-based line
// numbers, in the result, of foreign lines after the block that set one of the
// managed keys (npm takes the last value, so those win over keyto's).
func apply(content string, l laneLines) (string, []int, error) {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	placed := false
	for i := 0; i < len(lines); i++ {
		if !isMarker(lines[i], beginPrefix) {
			out = append(out, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && !isMarker(lines[end], EndMarker) {
			end++
		}
		if end == len(lines) {
			return "", nil, fmt.Errorf("keyto's begin marker on line %d has no %q line after it", i+1, EndMarker)
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

// shadowedLines numbers the foreign lines after the block that set the scope
// registry or the lane's auth key. out holds the block as one element.
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
		if !after {
			continue
		}
		key, _, ok := strings.Cut(chunk, "=")
		if k := strings.TrimSpace(key); ok && (k == scopeKey || k == l.authKey) {
			nos = append(nos, lineNo)
		}
	}
	return nos
}

func isMarker(line, prefix string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), prefix)
}
