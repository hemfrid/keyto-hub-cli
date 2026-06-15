package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type UpdateResult struct {
	FromTag         string
	Tag             string
	UpToDate        bool
	Updated         []string
	Added           []string
	SkippedModified []string // locally modified bundle files — left alone
	SkippedExisting []string // new upstream paths that already exist locally
	MissingLocal    []string // pinned files deleted locally — not resurrected
	RemovedUpstream []string // removed upstream + unmodified — deleted
	KeptModified    []string // removed upstream + modified — kept, now project-owned
}

// Update pulls the latest bundle release and applies it with the
// dpkg-conffile model: only files whose on-disk hash still matches the pinned
// (post-substitution) install hash are touched. Substitution reuses the
// PINNED inputs so hashes stay comparable across versions.
func Update(ctx context.Context, root string, d Deps) (*UpdateResult, error) {
	clean, err := IsClean(root)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, errors.New("working tree is not clean — commit or stash first so the update lands as one reviewable diff")
	}
	pin, err := LoadPin(root)
	if err != nil {
		return nil, err
	}
	if pin == nil {
		return nil, errors.New("no bundle installed here — run 'keyto ai init'")
	}

	meta, err := d.Meta(ctx)
	if err != nil {
		return nil, err
	}
	res := &UpdateResult{FromTag: pin.Tag, Tag: meta.Tag}
	if meta.Tag == pin.Tag {
		res.UpToDate = true
		return res, nil
	}

	tgz, err := d.Tarball(ctx, meta.Tag)
	if err != nil {
		return nil, err
	}
	files, err := ExtractVerify(tgz, &meta.Manifest)
	if err != nil {
		return nil, err
	}

	telemetry := pin.InstallInputs.TelemetryEndpoint
	base := pin.InstallInputs.BaseBranch

	pinIdx := make(map[string]string, len(pin.Files))
	for _, f := range pin.Files {
		pinIdx[f.Path] = f.SHA256
	}
	inManifest := make(map[string]bool, len(meta.Manifest.Files))

	paths := make([]string, 0, len(meta.Manifest.Files))
	for _, mf := range meta.Manifest.Files {
		paths = append(paths, mf.Path)
	}
	sort.Strings(paths)

	var newPinned []PinnedFile
	for _, p := range paths {
		inManifest[p] = true
		newContent := Substitute(files[p], telemetry, base)
		newHash := sha256Hex(newContent)
		dst := filepath.Join(root, filepath.FromSlash(p))
		pinnedHash, wasPinned := pinIdx[p]
		cur, readErr := os.ReadFile(dst)

		switch {
		case readErr != nil && os.IsNotExist(readErr) && wasPinned:
			// Dev deleted it deliberately — don't resurrect; keep the old
			// baseline so the deletion stays visible next time too.
			res.MissingLocal = append(res.MissingLocal, p)
			newPinned = append(newPinned, PinnedFile{Path: p, SHA256: pinnedHash})

		case readErr != nil && os.IsNotExist(readErr): // new upstream file
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, fmt.Errorf("ai: mkdir for %s: %w", p, err)
			}
			if err := os.WriteFile(dst, newContent, fileMode(p)); err != nil {
				return nil, fmt.Errorf("ai: write %s: %w", p, err)
			}
			res.Added = append(res.Added, p)
			newPinned = append(newPinned, PinnedFile{Path: p, SHA256: newHash})

		case readErr != nil:
			return nil, fmt.Errorf("ai: read %s: %w", p, readErr)

		case !wasPinned:
			// Exists locally but was never bundle-owned (skipped at init or
			// project-created). Stays project-local.
			res.SkippedExisting = append(res.SkippedExisting, p)

		case sha256Hex(cur) == pinnedHash:
			if newHash != pinnedHash {
				if err := os.WriteFile(dst, newContent, fileMode(p)); err != nil {
					return nil, fmt.Errorf("ai: write %s: %w", p, err)
				}
				res.Updated = append(res.Updated, p)
			}
			newPinned = append(newPinned, PinnedFile{Path: p, SHA256: newHash})

		default: // locally modified — never clobber; baseline stays at install hash
			res.SkippedModified = append(res.SkippedModified, p)
			newPinned = append(newPinned, PinnedFile{Path: p, SHA256: pinnedHash})
		}
	}

	// Pinned files that disappeared from the new manifest.
	removed := make([]string, 0)
	for p := range pinIdx {
		if !inManifest[p] {
			removed = append(removed, p)
		}
	}
	sort.Strings(removed)
	for _, p := range removed {
		dst := filepath.Join(root, filepath.FromSlash(p))
		cur, readErr := os.ReadFile(dst)
		switch {
		case readErr != nil: // already gone locally — drop silently
		case sha256Hex(cur) == pinIdx[p]:
			if err := os.Remove(dst); err != nil {
				return nil, fmt.Errorf("ai: remove %s: %w", p, err)
			}
			res.RemovedUpstream = append(res.RemovedUpstream, p)
		default:
			res.KeptModified = append(res.KeptModified, p) // dev owns it now
		}
	}

	pin.Tag = meta.Tag
	pin.SourceSHA = meta.Manifest.SourceSHA
	pin.ManifestCommit = meta.Manifest.ManifestCommit
	pin.FrameworkVersion = meta.Manifest.FrameworkVersion
	pin.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	pin.Files = newPinned
	if err := SavePin(root, pin); err != nil {
		return nil, err
	}
	return res, nil
}
