// Package ghname checks GitHub owner and repository names. keyto puts both
// into URLs (the git remote through the Hub), so text that only claims to be
// a name is refused first.
package ghname

import (
	"regexp"
	"strings"
)

const (
	maxOwner = 39
	maxRepo  = 100
)

// GitHub's rule for a user or organization login: letters and digits, with
// single hyphens between them.
var ownerRe = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// GitHub's rule for a repository name: letters, digits, dot, underscore and
// hyphen.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidOwner reports whether s is a GitHub user or organization login.
func ValidOwner(s string) bool {
	return len(s) <= maxOwner && ownerRe.MatchString(s)
}

// ValidRepo reports whether s is a GitHub repository name. "." and ".." are
// not, and neither is a name ending in ".git": the remote URL adds that
// suffix itself.
func ValidRepo(s string) bool {
	if len(s) > maxRepo || !repoRe.MatchString(s) {
		return false
	}
	return s != "." && s != ".." && !strings.HasSuffix(strings.ToLower(s), ".git")
}
