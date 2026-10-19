//go:build !windows

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// processGroup is the process group an agent and all its descendants run in.
//
// Why an anchor: a group id is a plain number. Once the group's last member is reaped, the kernel may hand the
// same number to an unrelated new group, and kill(-pgid) would then hit strangers. If the agent itself led the
// group, cmd.Wait reaping it could free the id while a descendant that moved to another group still holds the
// output pipes — and the kill afm sends afterwards could land on someone else.
//
// So afm owns the group's leader instead: a tiny "/bin/sh -c 'read _'" anchor that only afm can reap. The agent
// joins the anchor's group. While the anchor is unreaped (alive or a zombie), its pid — the group id — cannot be
// reused, so every signal sent before release reaches only this group. release kills the group and only then
// reaps the anchor; no signal is sent after that. The anchor blocks reading a pipe afm holds, so it also exits
// on its own if afm dies. This works the same on macOS and Linux and needs no waitid/WNOWAIT or kqueue.
type processGroup struct {
	anchor   *exec.Cmd
	hold     *os.File // write end of the anchor's stdin; closing it lets the anchor exit
	pgid     int
	kill     func(pid int, sig syscall.Signal) error
	released bool
}

// newProcessGroup starts the anchor of a new process group. The caller must call release.
func newProcessGroup() (*processGroup, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("process group anchor pipe: %w", err)
	}
	defer r.Close() //nolint:errcheck // the anchor holds its own copy after Start
	anchor := exec.Command("/bin/sh", "-c", "read _")
	anchor.Stdin = r
	anchor.Env = []string{}
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("process group anchor: %w", err)
	}
	return &processGroup{anchor: anchor, hold: w, pgid: anchor.Process.Pid, kill: syscall.Kill}, nil
}

// join makes cmd start inside the group. Call it before cmd.Start.
func (g *processGroup) join(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: g.pgid}
}

// signal sends sig to every process still in the group. It is a no-op after release.
func (g *processGroup) signal(sig syscall.Signal) {
	if g.released {
		return
	}
	_ = g.kill(-g.pgid, sig) // ESRCH only means the group is already empty
}

// release kills whatever is left in the group, then reaps the anchor. Safe to call more than once.
func (g *processGroup) release() {
	if g.released {
		return
	}
	g.signal(syscall.SIGKILL)
	g.released = true
	_ = g.hold.Close()
	_ = g.anchor.Wait() // the anchor was just killed; its exit status says nothing
}
