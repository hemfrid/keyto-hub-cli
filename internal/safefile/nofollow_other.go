//go:build !unix

package safefile

// oNoFollow is 0 where the platform has no O_NOFOLLOW; the Lstat checks and
// O_EXCL temp files still apply.
const oNoFollow = 0
