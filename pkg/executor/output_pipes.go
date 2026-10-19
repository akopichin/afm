package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// pipeGracePeriod is how long an agent's output pipes may stay open after the agent itself has exited. Only a
// descendant it left behind can still hold them; after this period the whole process group is killed, whatever
// the agent's exit code was. Real time, not the injected clock: it bounds an OS-level drain, like
// exec.Cmd.WaitDelay.
const pipeGracePeriod = 2 * time.Second

// outputPipes are an agent's stdout pipe and, when the caller's stderr writer is not a file, its stderr pipe.
// afm owns both ends — not exec.Cmd — so Wait reports the agent's own exit, and afm decides how long to keep
// reading what a leftover descendant may still write.
type outputPipes struct {
	stdoutR, stdoutW *os.File
	stderrR, stderrW *os.File // nil when stderr goes straight to a file
	stdoutDone       chan error
	stderrDone       chan struct{}
}

// attachOutputPipes connects cmd's stdout, and its stderr unless stderr is a file, to fresh pipes.
func attachOutputPipes(cmd *exec.Cmd, stderr io.Writer) (*outputPipes, error) {
	p := &outputPipes{stdoutDone: make(chan error, 1), stderrDone: make(chan struct{})}
	var err error
	if p.stdoutR, p.stdoutW, err = os.Pipe(); err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stdout = p.stdoutW
	if _, isFile := stderr.(*os.File); isFile {
		cmd.Stderr = stderr
		return p, nil
	}
	if p.stderrR, p.stderrW, err = os.Pipe(); err != nil {
		p.close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stderr = p.stderrW
	return p, nil
}

// startReading closes the parent's copies of the write ends — from now on only the agent and its descendants
// hold them — and starts reading stdout line by line into onLine and stderr into stderr. Call it after Start.
func (p *outputPipes) startReading(stderr io.Writer, onLine func(string) bool) {
	closeFile(&p.stdoutW)
	closeFile(&p.stderrW)
	stdoutR, stderrR := p.stdoutR, p.stderrR // drain may close and clear the fields while these goroutines read
	go func() { p.stdoutDone <- lineReader(stdoutR, onLine) }()
	if stderrR == nil {
		close(p.stderrDone)
		return
	}
	go func() {
		_, _ = io.Copy(stderr, stderrR)
		close(p.stderrDone)
	}()
}

// drain waits, after the agent has exited, until both pipes are closed. A descendant still holding one
// pipeGracePeriod later is killed with the agent's whole process group, and the read ends are closed so a
// descendant outside the group cannot hold the turn open either. It returns the stdout reader's own error;
// the read cut short by that forced close is not one.
func (p *outputPipes) drain(cmd *exec.Cmd) error {
	grace := time.NewTimer(pipeGracePeriod)
	defer grace.Stop()
	stdoutDone, stderrDone := p.stdoutDone, p.stderrDone
	var readErr error
	for stdoutDone != nil || stderrDone != nil {
		select {
		case readErr = <-stdoutDone:
			stdoutDone = nil
		case <-stderrDone:
			stderrDone = nil
		case <-grace.C:
			killAgent(cmd)
			closeFile(&p.stdoutR)
			closeFile(&p.stderrR)
			if stdoutDone != nil {
				readErr = <-stdoutDone
			}
			if stderrDone != nil {
				<-stderrDone
			}
			stdoutDone, stderrDone = nil, nil
		}
	}
	if errors.Is(readErr, os.ErrClosed) {
		return nil
	}
	return readErr
}

// close releases every pipe end still open. Safe to call more than once.
func (p *outputPipes) close() {
	closeFile(&p.stdoutR)
	closeFile(&p.stdoutW)
	closeFile(&p.stderrR)
	closeFile(&p.stderrW)
}

func closeFile(f **os.File) {
	if *f != nil {
		_ = (*f).Close()
		*f = nil
	}
}

// killAgent kills the agent and then its whole process group. The agent goes first, so it cannot report its
// children's death on the output afm still reads.
func killAgent(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	killProcessGroup(cmd, syscall.SIGKILL)
}
