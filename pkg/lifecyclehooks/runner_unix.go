//go:build !windows

package lifecyclehooks

import (
	"os/exec"
	"syscall"
)

// killProcessGroup сигналит всей группе процессов, а не только прямому потомку. setProcessGroup
// (вызывается перед Start) делает команду лидером своей группы, чтобы -pid дошёл до всех потомков.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// setProcessGroup делает cmd лидером собственной группы процессов перед
// Start, чтобы killProcessGroup достал всех потомков, а не только прямого.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
