//go:build windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// killProcessGroup: на Windows нет аналога отрицательного PID, поэтому сигналим только прямому потомку.
// Реальные цели деплоя afm (Docker/macOS/Linux) идут через verify_procgroup_unix.go.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// setProcessGroup — no-op на Windows: эквивалент не настроен.
func setProcessGroup(cmd *exec.Cmd) {}
