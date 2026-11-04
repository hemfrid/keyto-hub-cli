package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  Kind
	}{
		{"empty dir is Next.js", nil, NextJS},
		{"package.json only is Next.js", []string{"package.json"}, NextJS},
		{"wrangler.json is Workers", []string{"package.json", "wrangler.json"}, Workers},
		{"wrangler.jsonc is Workers", []string{"wrangler.jsonc"}, Workers},
		{"wrangler.toml is not the template's config", []string{"wrangler.toml"}, NextJS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := Detect(dir); got != c.want {
				t.Fatalf("Detect = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetect_DirectoryNamedWranglerJSONIsNotWorkers(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "wrangler.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Detect(dir); got != NextJS {
		t.Fatalf("Detect = %q, want %q", got, NextJS)
	}
}
