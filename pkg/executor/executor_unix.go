//go:build !windows

package executor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// processGroupAnchorArg is the hidden argument that makes the afm binary serve as a process group anchor.
const processGroupAnchorArg = "__afm-process-group-anchor"

// anchorExecutable is the binary afm starts as a process group anchor. Empty means none: the agent then leads
// its own group. ServeProcessGroupAnchor sets it once, at the start of main, before any agent runs.
var anchorExecutable string

// ServeProcessGroupAnchor must be the first call in main (and in TestMain of a test binary that runs agents).
// When args ask this process to be a process group anchor, it blocks until its stdin closes and returns true:
// the caller then exits. Otherwise it records that this binary can anchor agent groups and returns false.
func ServeProcessGroupAnchor(args []string) bool {
	if len(args) == 2 && args[1] == processGroupAnchorArg {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return true
	}
	if exe, err := os.Executable(); err == nil {
		anchorExecutable = exe
	}
	return false
}

// processGroup is the process group an agent and all its descendants run in.
//
// Why an anchor: a group id is a plain number. Once the group's last member is reaped, the kernel may hand the
// same number to an unrelated new group, and kill(-pgid) would then hit strangers. If the agent itself led the
// group, cmd.Wait reaping it could free the id while a descendant that moved to another group still holds the
// output pipes — and the kill afm sends afterwards could land on someone else.
//
// So afm owns the group's leader instead: a copy of the afm binary itself, started with processGroupAnchorArg,
// that only afm can reap. No external shell is needed. The agent joins the anchor's group. While the anchor is
// unreaped (alive or a zombie), its pid — the group id — cannot be reused, so every signal sent before release
// reaches only this group. release kills the group and only then reaps the anchor; no signal is sent after
// that. The anchor blocks reading a pipe afm holds, so it also exits on its own if afm dies. This works the
// same on macOS and Linux and needs no waitid/WNOWAIT or kqueue.
//
// Without an anchor (the binary did not call ServeProcessGroupAnchor, or the anchor failed to start) the agent
// leads its own group, as before anchors existed. Signals then go to the agent's pid as a group id: safe while
// the agent or any member of its group lives; once all are gone and the agent is reaped, the id may in theory
// be reused before release's final kill.
type processGroup struct {
	anchor   *exec.Cmd // nil without an anchor
	hold     *os.File  // write end of the anchor's stdin; closing it lets the anchor exit
	leader   *exec.Cmd // the agent, when it leads the group itself
	pgid     int       // the anchor's pid; 0 without an anchor
	kill     func(pid int, sig syscall.Signal) error
	released bool
}

// newProcessGroup starts an anchor from anchorExe, the afm binary, for a new process group. With an empty
// anchorExe, or when the anchor cannot start, it returns a group the agent leads itself; a start failure is
// reported on stderr. The caller must call release.
func newProcessGroup(anchorExe string, stderr io.Writer) *processGroup {
	g := &processGroup{kill: syscall.Kill}
	if anchorExe == "" {
		return g
	}
	anchor, hold, err := startAnchor(anchorExe)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "afm: process group anchor did not start, the agent leads its own group: %v\n", err)
		return g
	}
	g.anchor, g.hold, g.pgid = anchor, hold, anchor.Process.Pid
	return g
}

// startAnchor starts exe as an anchor leading a new process group, and returns it with the write end of its
// stdin.
func startAnchor(exe string) (*exec.Cmd, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer r.Close() //nolint:errcheck // the anchor holds its own copy after Start
	anchor := exec.Command(exe, processGroupAnchorArg)
	anchor.Stdin = r
	anchor.Env = []string{}
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	return anchor, w, nil
}

// join makes cmd start inside the group — as a member of the anchor's group, or as the leader of its own when
// there is no anchor. Call it before cmd.Start.
func (g *processGroup) join(cmd *exec.Cmd) {
	if g.anchor == nil {
		g.leader = cmd
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: g.pgid}
}

// signal sends sig to every process still in the group. It is a no-op after release.
func (g *processGroup) signal(sig syscall.Signal) {
	if g.released {
		return
	}
	pgid := g.pgid
	if g.anchor == nil {
		if g.leader == nil || g.leader.Process == nil {
			return
		}
		pgid = g.leader.Process.Pid
	}
	_ = g.kill(-pgid, sig) // ESRCH only means the group is already empty
}

// release kills whatever is left in the group, then reaps the anchor. Safe to call more than once.
func (g *processGroup) release() {
	if g.released {
		return
	}
	g.signal(syscall.SIGKILL)
	g.released = true
	if g.anchor != nil {
		_ = g.hold.Close()
		_ = g.anchor.Wait() // the anchor was just killed; its exit status says nothing
	}
}
