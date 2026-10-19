//go:build windows

package executor

import (
	"os/exec"
	"syscall"
)

// processGroup has no process-group equivalent wired up on Windows: signal reaches only the agent itself.
type processGroup struct {
	cmd *exec.Cmd
}

func newProcessGroup() (*processGroup, error) { return &processGroup{}, nil }

func (g *processGroup) join(cmd *exec.Cmd) { g.cmd = cmd }

func (g *processGroup) signal(sig syscall.Signal) {
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Signal(sig)
	}
}

func (g *processGroup) release() {}
