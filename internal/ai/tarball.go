package ai

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ExtractVerify unpacks the bundle tarball into memory and verifies it
// matches the release manifest exactly: every manifest file present with the
// listed sha256, and no unlisted files. A corrupt or tampered download is a
// no-op — nothing touches the working tree until verification passes.
func ExtractVerify(tgz []byte, manifest *hub.AIBundleManifest) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("ai: bundle is not valid gzip: %w", err)
	}
	defer gz.Close()

	files := make(map[string][]byte)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ai: read bundle tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories etc.
		}
		// The builder emits "./"-prefixed entries (tar -C stage .).
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if path.IsAbs(name) || strings.HasPrefix(name, "..") || strings.Contains(name, "/../") {
			return nil, fmt.Errorf("ai: bundle entry escapes the repo: %q", hdr.Name)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("ai: read bundle entry %q: %w", name, err)
		}
		files[name] = content
	}

	listed := make(map[string]string, len(manifest.Files))
	for _, mf := range manifest.Files {
		listed[mf.Path] = mf.SHA256
	}
	for p, want := range listed {
		content, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("ai: bundle missing manifest file %q", p)
		}
		if got := sha256Hex(content); got != want {
			return nil, fmt.Errorf("ai: hash mismatch for %q (corrupt download?)", p)
		}
	}
	for p := range files {
		if _, ok := listed[p]; !ok {
			return nil, fmt.Errorf("ai: bundle contains unlisted file %q", p)
		}
	}
	return files, nil
}
