//go:build !windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// killProcessGroup сигналит всей группе процессов (отрицательный PID), а не
// только прямому потомку — тот же урок, что pkg/executor.killProcessGroup и
// pkg/lifecyclehooks.killProcessGroup: shell-verify команда (st.Run) может
// быть скриптом, чей внук не exec'нулся в родителя (например, хвостовой
// `sleep N &`) — внук наследует stdout/stderr-пайпы, и обычный
// cmd.Process.Kill()/SIGKILL прямому потомку убивает только "sh", а внук
// держит пайп открытым, и cmd.Wait()/CombinedOutput() висит до его конца.
// setProcessGroup (вызывается перед Start) делает команду лидером своей
// группы, чтобы -pid дошёл до всех потомков. При ошибке килла группы
// (например, процесс уже пожат) — фолбэк на сигнал самому процессу.
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
