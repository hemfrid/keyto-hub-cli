package ai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type Status struct {
	InstalledTag string
	LatestTag    string
	UpToDate     bool
	Modified     []string // bundle files locally edited since install
	Missing      []string // bundle files locally deleted since install
}

// GetStatus compares the pin against the latest release tag and against the
// working tree (post-substitution baseline hashes).
func GetStatus(ctx context.Context, root string, d Deps) (*Status, error) {
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

	st := &Status{
		InstalledTag: pin.Tag,
		LatestTag:    meta.Tag,
		UpToDate:     pin.Tag == meta.Tag,
	}
	for _, f := range pin.Files {
		cur, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		switch {
		case readErr != nil && os.IsNotExist(readErr):
			st.Missing = append(st.Missing, f.Path)
		case readErr != nil:
			return nil, readErr
		case sha256Hex(cur) != f.SHA256:
			st.Modified = append(st.Modified, f.Path)
		}
	}
	sort.Strings(st.Modified)
	sort.Strings(st.Missing)
	return st, nil
}
