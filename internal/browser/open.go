// Package browser provides a minimal cross-platform helper for opening a URL
// in the system's default web browser.
package browser

import (
	"os/exec"
	"runtime"
)

// OpenURL opens u in the default system browser.
// It uses the platform-native launcher:
//   - macOS  : open
//   - Windows: rundll32 url.dll,FileProtocolHandler
//   - Other  : xdg-open
func OpenURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
