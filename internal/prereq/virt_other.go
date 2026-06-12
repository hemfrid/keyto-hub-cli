//go:build !windows

package prereq

import "context"

// VirtualizationOK is the non-Windows stub. The virtualization probe only feeds
// the Windows daemon-down diagnosis (Docker Desktop/WSL2 need a hypervisor); on
// other platforms Diagnose never consults it. It returns true so any accidental
// call is a no-op.
func VirtualizationOK(_ context.Context) bool { return true }
