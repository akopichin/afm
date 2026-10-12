//go:build !windows

package executor

import (
	"os/exec"
	"syscall"
)

// killProcessGroup signals the entire process group, not just the direct child. setProcessGroup (called
// before Start) makes the child its own group leader so -pid reaches every descendant.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// setProcessGroup makes cmd the leader of its own process group before Start, so killProcessGroup's
// negative-PID kill reaches every descendant instead of just the direct child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
