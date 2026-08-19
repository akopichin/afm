//go:build !windows

package executor

import (
	"os/exec"
	"syscall"
)

// killProcessGroup signals the entire process group (negative PID), not just
// the direct child. Found via a widespread test flake: e.cfg.Command can be a
// script whose last line spawns a child without exec'ing into it (e.g. a
// trailing `sleep N`) — that grandchild inherits the stdout pipe created in
// run(), and cmd.Process.Kill() only kills the direct child (bash). The
// orphaned grandchild survives, keeps the pipe's write end open, and
// lineReader never sees EOF — <-done blocks until the orphan exits on its
// own (observed as agent goroutines outliving ctx cancellation by up to the
// orphan's remaining lifetime, e.g. the rest of a `sleep 30`). setProcessGroup
// (called before Start) makes the child its own group leader so -pid reaches
// every descendant. Falls back to signaling just the process if the group
// kill fails (e.g. already reaped).
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// setProcessGroup makes cmd the leader of its own process group before Start,
// so killProcessGroup's negative-PID kill reaches every descendant instead of
// just the direct child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
