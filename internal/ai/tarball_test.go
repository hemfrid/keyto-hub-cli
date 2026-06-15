package ai

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// buildTarball produces a gzip tarball with "./"-prefixed entries, the way
// the ai-capabilities builder (`tar -C stage .`) emits them.
func buildTarball(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + path, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func manifestFor(files map[string][]byte) *hub.AIBundleManifest {
	m := &hub.AIBundleManifest{Tag: "v0.2.0"}
	for path, content := range files {
		m.Files = append(m.Files, hub.AIBundleManifestFile{Path: path, SHA256: sha(content)})
	}
	return m
}

func TestExtractVerify(t *testing.T) {
	files := map[string][]byte{
		".claude/agents/x.md":            []byte("# x\n"),
		".claude/hooks/session-start.sh": []byte("#!/bin/bash\necho hi\n"),
		"CLAUDE.md":                      []byte("# project\n"),
	}
	got, err := ExtractVerify(buildTarball(t, files), manifestFor(files))
	if err != nil {
		t.Fatalf("ExtractVerify: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("extracted %d files, want 3", len(got))
	}
	if string(got[".claude/agents/x.md"]) != "# x\n" {
		t.Errorf("content mismatch: %q", got[".claude/agents/x.md"])
	}
}

func TestExtractVerifyRejectsHashMismatch(t *testing.T) {
	files := map[string][]byte{"CLAUDE.md": []byte("good")}
	m := manifestFor(map[string][]byte{"CLAUDE.md": []byte("DIFFERENT")})
	if _, err := ExtractVerify(buildTarball(t, files), m); err == nil {
		t.Error("expected hash-mismatch error")
	}
}

func TestExtractVerifyRejectsMissingManifestFile(t *testing.T) {
	tgz := buildTarball(t, map[string][]byte{"CLAUDE.md": []byte("x")})
	m := manifestFor(map[string][]byte{"CLAUDE.md": []byte("x"), ".claude/missing.md": []byte("y")})
	if _, err := ExtractVerify(tgz, m); err == nil {
		t.Error("expected missing-file error")
	}
}

func TestExtractVerifyRejectsUnlistedFile(t *testing.T) {
	tgz := buildTarball(t, map[string][]byte{"CLAUDE.md": []byte("x"), "sneaky.sh": []byte("rm -rf /")})
	m := manifestFor(map[string][]byte{"CLAUDE.md": []byte("x")})
	if _, err := ExtractVerify(tgz, m); err == nil {
		t.Error("expected unlisted-file error")
	}
}

func TestExtractVerifyRejectsTraversal(t *testing.T) {
	for _, evil := range []string{"../outside.md", "/abs.md"} {
		files := map[string][]byte{evil: []byte("x")}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: evil, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_ = gz.Close()
		m := manifestFor(files)
		if _, err := ExtractVerify(buf.Bytes(), m); err == nil {
			t.Errorf("expected traversal rejection for %q", evil)
		}
	}
}

func TestExtractVerifyRejectsCorruptGzip(t *testing.T) {
	if _, err := ExtractVerify([]byte("not gzip"), &hub.AIBundleManifest{}); err == nil {
		t.Error("expected gzip error")
	}
}
