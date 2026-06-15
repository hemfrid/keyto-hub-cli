package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// Deps injects the Hub calls so flows are testable offline (start.go pattern).
type Deps struct {
	Meta    func(ctx context.Context) (*hub.AIBundleMeta, error)
	Tarball func(ctx context.Context, tag string) ([]byte, error)
	HubURL  string
}

type InitResult struct {
	Tag     string
	Written []string
	Skipped []string // pre-existing paths left untouched (project-local by definition)
}

func telemetryEndpoint(hubURL string) string {
	return strings.TrimRight(hubURL, "/") + "/api/telemetry/events"
}

func fileMode(path string) os.FileMode {
	if strings.HasSuffix(path, ".sh") {
		return 0o755
	}
	return 0o644
}

// Init installs the general AI-capabilities bundle into the repo at root:
// clean tree required, never overwrites existing files, substitutes the two
// CLI-known placeholders, writes the .keyto pin with post-substitution hashes.
func Init(ctx context.Context, root string, d Deps) (*InitResult, error) {
	clean, err := IsClean(root)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, errors.New("working tree is not clean — commit or stash first so the install lands as one reviewable diff")
	}
	existing, err := LoadPin(root)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errors.New("bundle already installed (.keyto/skills-bundle.yaml exists) — run 'keyto ai update'")
	}

	meta, err := d.Meta(ctx)
	if err != nil {
		return nil, err
	}
	tgz, err := d.Tarball(ctx, meta.Tag)
	if err != nil {
		return nil, err
	}
	files, err := ExtractVerify(tgz, &meta.Manifest)
	if err != nil {
		return nil, err
	}

	telemetry := telemetryEndpoint(d.HubURL)
	base := DefaultBranch(root)

	// Deterministic order for output and pin.
	paths := make([]string, 0, len(meta.Manifest.Files))
	for _, mf := range meta.Manifest.Files {
		paths = append(paths, mf.Path)
	}
	sort.Strings(paths)

	res := &InitResult{Tag: meta.Tag}
	var pinned []PinnedFile
	for _, p := range paths {
		dst := filepath.Join(root, filepath.FromSlash(p))
		if _, err := os.Lstat(dst); err == nil {
			res.Skipped = append(res.Skipped, p)
			continue
		}
		content := Substitute(files[p], telemetry, base)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("ai: mkdir for %s: %w", p, err)
		}
		if err := os.WriteFile(dst, content, fileMode(p)); err != nil {
			return nil, fmt.Errorf("ai: write %s: %w", p, err)
		}
		pinned = append(pinned, PinnedFile{Path: p, SHA256: sha256Hex(content)})
		res.Written = append(res.Written, p)
	}

	pin := &Pin{
		SourceRepo:       meta.SourceRepo,
		Tag:              meta.Tag,
		SourceSHA:        meta.Manifest.SourceSHA,
		ManifestCommit:   meta.Manifest.ManifestCommit,
		FrameworkVersion: meta.Manifest.FrameworkVersion,
		FetchedAt:        time.Now().UTC().Format(time.RFC3339),
		InstallChannel:   "cli",
		InstallInputs: Inputs{
			TelemetryEndpoint: telemetry,
			BaseBranch:        base,
		},
		Files: pinned,
	}
	if err := SavePin(root, pin); err != nil {
		return nil, err
	}
	return res, nil
}
