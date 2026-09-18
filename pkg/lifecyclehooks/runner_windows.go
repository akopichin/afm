//go:build windows

package lifecyclehooks

import (
	"os/exec"
	"syscall"
)

// killProcessGroup: на Windows нет аналога отрицательного PID (нужен был бы
// CREATE_NEW_PROCESS_GROUP при Start), поэтому сигналим только прямому
// потомку. Реальные цели деплоя afm (Docker/macOS/Linux) идут через
// runner_unix.go.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}

// setProcessGroup — no-op на Windows: эквивалент не настроен.
func setProcessGroup(cmd *exec.Cmd) {}
