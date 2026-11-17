//go:build unix

package serve

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session: it must survive the host and must not receive the terminal's signals.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
