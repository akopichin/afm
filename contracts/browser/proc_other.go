//go:build !unix

package browser

import "os/exec"

// setPgid is a no-op on non-unix platforms (the browser capability targets
// macOS/Linux; Windows is build-compatible but unsupported for live Chrome).
func setPgid(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing the direct process on non-unix.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// processAlive cannot probe reliably without unix signals; assume alive so the
// conservative sweep never deletes a possibly-live profile.
var processAlive = func(pid int) bool { return pid > 0 }
