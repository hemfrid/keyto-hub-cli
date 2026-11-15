package ai

import (
	"context"
	"errors"
	"regexp"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// A bundle tag is an ai-capabilities release (vX.Y.Z) or a release candidate
// (rc/X.Y.Z-N). Anything else from the Hub is refused before it is used in a
// download, written to the pin or printed.
var bundleTagRe = regexp.MustCompile(`^(v[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}|rc/[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}-[0-9]{1,6})$`)

// UnrecognizedTag is shown in place of a pin tag that is not a bundle tag.
const UnrecognizedTag = "(unrecognized version)"

// ValidBundleTag reports whether tag is vX.Y.Z or rc/X.Y.Z-N.
func ValidBundleTag(tag string) bool { return bundleTagRe.MatchString(tag) }

// DisplayTag is a pin tag safe to print: as is when valid or empty (pins
// written by ai-capabilities' setup.sh carry none), else UnrecognizedTag.
func DisplayTag(tag string) string {
	if tag == "" || ValidBundleTag(tag) {
		return tag
	}
	return UnrecognizedTag
}

// fetchMeta gets the Hub's latest bundle metadata and refuses a tag that is
// not a bundle tag, without echoing it.
func fetchMeta(ctx context.Context, d Deps) (*hub.AIBundleMeta, error) {
	meta, err := d.Meta(ctx)
	if err != nil {
		return nil, err
	}
	if !ValidBundleTag(meta.Tag) {
		return nil, errors.New("the Hub's AI bundle release tag is not a version (vX.Y.Z or rc/X.Y.Z-N); nothing was installed")
	}
	return meta, nil
}
