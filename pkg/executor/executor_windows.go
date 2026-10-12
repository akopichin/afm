//go:build windows

package executor

import (
	"os/exec"
	"syscall"
)

// killProcessGroup has no process-group/negative-PID equivalent wired up on Windows, so it just signals
// the direct child.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// setProcessGroup is a no-op on Windows — there's no equivalent set up.
func setProcessGroup(cmd *exec.Cmd) {}
