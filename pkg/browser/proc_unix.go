//go:build unix

package browser

import (
	"os/exec"
	"syscall"
)

// setPgid puts the child in its own process group so the whole subtree can be
// killed on teardown (a direct Process.Kill would miss grandchildren).
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the whole group of a started command.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative pid targets the process group (set up by setPgid).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill() // belt-and-suspenders if the group was not set up
}

// processAlive reports whether a pid is a live process (signal 0 probe). ESRCH →
// dead; EPERM → alive but owned by another user (treated as alive, conservative).
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
