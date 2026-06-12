//go:build windows

package prereq

import (
	"context"
	"os/exec"
	"strings"
)

// VirtualizationOK reports whether CPU virtualization is available on this
// Windows machine. Docker Desktop and WSL2 need a hypervisor; if virtualization
// is disabled in the BIOS/UEFI they cannot start. We treat either signal as
// "virtualization usable":
//   - HypervisorPresent: a hypervisor is already running (Hyper-V / WSL2).
//   - VirtualizationFirmwareEnabled: the CPU feature is enabled in firmware even
//     if no hypervisor is currently active.
//
// Any PowerShell/exec failure is treated as "unknown" → returns true so we never
// surface a false BIOS-disabled diagnosis when we simply couldn't probe.
func VirtualizationOK(ctx context.Context) bool {
	const script = `$h=(Get-CimInstance Win32_ComputerSystem).HypervisorPresent; ` +
		`$v=(Get-CimInstance Win32_Processor | Select-Object -First 1).VirtualizationFirmwareEnabled; ` +
		`if ($h -or $v) { 'true' } else { 'false' }`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return true // couldn't probe → don't claim virtualization is off
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "true")
}
