//go:build !windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// killProcessGroup сигналит всей группе, чтобы потомки не держали stdout/stderr открытыми; при ошибке
// сигналит самому процессу.
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
