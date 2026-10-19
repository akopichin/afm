//go:build windows

package executor

import (
	"io"
	"os/exec"
	"syscall"
)

// ServeProcessGroupAnchor is a no-op on Windows: there is no process group anchor.
func ServeProcessGroupAnchor([]string) bool { return false }

// anchorExecutable is unused on Windows; it keeps the call site shared with Unix.
const anchorExecutable = ""

// processGroup has no process-group equivalent wired up on Windows: signal reaches only the agent itself.
type processGroup struct {
	cmd *exec.Cmd
}

func newProcessGroup(string, io.Writer) *processGroup { return &processGroup{} }

func (g *processGroup) join(cmd *exec.Cmd) { g.cmd = cmd }

func (g *processGroup) signal(sig syscall.Signal) {
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Signal(sig)
	}
}

func (g *processGroup) release() {}
