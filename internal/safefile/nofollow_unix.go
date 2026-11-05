//go:build unix

package safefile

import "syscall"

// oNoFollow makes open fail on a symlink at the final path component.
const oNoFollow = syscall.O_NOFOLLOW
