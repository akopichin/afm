//go:build windows

package executor

import (
	"os/exec"
	"syscall"
)

// killProcessGroup has no process-group/negative-PID equivalent wired up on
// Windows (would need CREATE_NEW_PROCESS_GROUP at Start time), so it just
// signals the direct child. afm's supported deployment targets
// (Docker/macOS/Linux) hit the real group-kill path in executor_unix.go.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// setProcessGroup is a no-op on Windows — there's no equivalent set up.
func setProcessGroup(cmd *exec.Cmd) {}
