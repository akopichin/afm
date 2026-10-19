package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// pipeGracePeriod is how long an agent's output pipes may stay silent after the agent has exited before afm
// decides a leftover descendant, not the agent, holds them open. Silence means the reader waits for data that
// does not come: while data flows, or while the output callback still handles a line, the period does not run.
// Real time, not the injected clock: it bounds an OS-level drain, like exec.Cmd.WaitDelay.
const pipeGracePeriod = 2 * time.Second

// pipeDrainLimit bounds how long afm reads output after the agent has exited, however busy the pipes are: a
// descendant that writes without pause would otherwise keep the turn open forever. When it runs out, afm kills
// the agent's process group.
const pipeDrainLimit = 10 * time.Second

// pipeFlushPeriod is how long afm still reads after it killed the group: enough to hand the output already in
// the pipes to the readers. Whatever keeps the pipes open after that is outside the group; afm stops reading.
const pipeFlushPeriod = time.Second

// pipeSilencePoll is how often drain checks whether the output readers have gone silent.
const pipeSilencePoll = 50 * time.Millisecond

// agentPipes are the agent's stdin, its stdout and, unless the caller's stderr writer is a file, its stderr.
// afm owns every end — not exec.Cmd — so cmd.Wait returns as soon as the agent exits: there is no exec copy
// goroutine that a descendant holding an inherited pipe could block.
type agentPipes struct {
	stdinR, stdinW *os.File
	stdinWritten   chan struct{}
	outputs        []*outputPipe // stdout first, then stderr when it is a pipe
	finished       chan struct{} // one value per output reader that returned
	drainLimit     time.Duration // pipeDrainLimit; tests shorten it
}

// outputPipe is one output pipe and what its reader is doing.
type outputPipe struct {
	r, w *os.File
	err  error // the reader's own error; read it only after the reader reported on finished

	mu           sync.Mutex
	waitingSince time.Time // when the reader began waiting for data; zero while it handles data
	done         bool
}

// attachAgentPipes connects cmd's stdin, stdout and, unless stderr is a file, its stderr to fresh pipes.
func attachAgentPipes(cmd *exec.Cmd, stderr io.Writer) (*agentPipes, error) {
	p := &agentPipes{stdinWritten: make(chan struct{}), drainLimit: pipeDrainLimit}
	var err error
	if p.stdinR, p.stdinW, err = os.Pipe(); err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	cmd.Stdin = p.stdinR
	if cmd.Stdout, err = p.addOutput(); err != nil {
		p.close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if _, isFile := stderr.(*os.File); isFile {
		cmd.Stderr = stderr
	} else if cmd.Stderr, err = p.addOutput(); err != nil {
		p.close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	p.finished = make(chan struct{}, len(p.outputs))
	return p, nil
}

// addOutput creates an output pipe and returns its write end for the agent.
func (p *agentPipes) addOutput() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.outputs = append(p.outputs, &outputPipe{r: r, w: w})
	return w, nil
}

// start closes the parent's copies of the agent's pipe ends — from now on only the agent and its descendants
// hold them — writes prompt to stdin in the background and starts reading stdout line by line into onLine and
// stderr into stderr. Call it after cmd.Start.
func (p *agentPipes) start(prompt string, stderr io.Writer, onLine func(string) bool) {
	_ = p.stdinR.Close()
	for _, o := range p.outputs {
		_ = o.w.Close()
	}
	go func() {
		defer close(p.stdinWritten)
		// The agent may exit or close stdin without reading the whole prompt; the write then fails, and the
		// agent's own exit status and output report the outcome. drain closes stdinW to stop a blocked write.
		_, _ = io.WriteString(p.stdinW, prompt)
		_ = p.stdinW.Close()
	}()
	p.read(p.outputs[0], func(r io.Reader) error { return lineReader(r, onLine) })
	if len(p.outputs) > 1 {
		p.read(p.outputs[1], func(r io.Reader) error {
			_, err := io.Copy(stderr, r)
			return err
		})
	}
}

func (p *agentPipes) read(o *outputPipe, consume func(io.Reader) error) {
	go func() {
		o.err = consume(o)
		o.mu.Lock()
		o.done = true
		o.mu.Unlock()
		p.finished <- struct{}{}
	}()
}

// drain runs after the agent has exited. It stops the prompt write and reads every output pipe to its end.
// It kills the agent's process group when the pipes all stay silent for pipeGracePeriod (a leftover
// descendant holds them), when drainLimit has passed since the agent exited (a descendant keeps writing), when
// ctx is cancelled or when idle fires. After the kill it reads for pipeFlushPeriod more; pipes still open then
// are held by a process outside the group: drain closes the read ends and reports the truncated output on
// stderr. It returns ctx's error or ErrIdleTimeout when they stopped the drain, otherwise the stdout reader's
// own error.
func (p *agentPipes) drain(ctx context.Context, idle <-chan time.Time, group *processGroup, stderr io.Writer) error {
	_ = p.stdinW.Close()
	<-p.stdinWritten

	poll := time.NewTicker(pipeSilencePoll)
	defer poll.Stop()
	exitedAt := time.Now()
	var killedAt time.Time
	kill := func() {
		if killedAt.IsZero() {
			group.signal(syscall.SIGKILL)
			killedAt = time.Now()
		}
	}
	done := ctx.Done()
	var stopErr error
	truncated := false
	for remaining := len(p.outputs); remaining > 0; {
		select {
		case <-p.finished:
			remaining--
		case <-done:
			stopErr, done = ctx.Err(), nil
			kill()
		case <-idle:
			stopErr, idle = ErrIdleTimeout, nil
			kill()
		case <-poll.C:
			if killedAt.IsZero() {
				if p.silentFor(pipeGracePeriod, exitedAt) || time.Since(exitedAt) >= p.drainLimit {
					kill()
				}
			} else if !truncated && time.Since(killedAt) >= pipeFlushPeriod {
				truncated = true
				for _, o := range p.outputs {
					_ = o.r.Close() // unblocks the reader with os.ErrClosed
				}
			}
		}
	}
	readErr := p.outputs[0].err
	if truncated {
		// Every reader has returned, so nothing else writes to stderr now.
		_, _ = fmt.Fprintf(stderr, "afm: agent output truncated: its output pipes were still open %s after the "+
			"agent exited and its process group was killed\n", time.Since(exitedAt).Round(100*time.Millisecond))
		if errors.Is(readErr, os.ErrClosed) {
			readErr = nil
		}
	}
	if stopErr != nil {
		return stopErr
	}
	return readErr
}

// silentFor reports whether every output reader still running has waited for data for at least d, not counting
// time before from.
func (p *agentPipes) silentFor(d time.Duration, from time.Time) bool {
	for _, o := range p.outputs {
		if !o.silentFor(d, from) {
			return false
		}
	}
	return true
}

// Read records when the reader starts waiting for data, so drain can tell a silent pipe from a slow consumer.
func (o *outputPipe) Read(b []byte) (int, error) {
	o.setWaitingSince(time.Now())
	n, err := o.r.Read(b)
	o.setWaitingSince(time.Time{})
	return n, err
}

func (o *outputPipe) setWaitingSince(t time.Time) {
	o.mu.Lock()
	o.waitingSince = t
	o.mu.Unlock()
}

func (o *outputPipe) silentFor(d time.Duration, from time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return true
	}
	if o.waitingSince.IsZero() {
		return false
	}
	return time.Since(latest(o.waitingSince, from)) >= d
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// close releases every pipe end still open. Safe to call more than once.
func (p *agentPipes) close() {
	for _, f := range []*os.File{p.stdinR, p.stdinW} {
		if f != nil {
			_ = f.Close()
		}
	}
	for _, o := range p.outputs {
		_ = o.r.Close()
		_ = o.w.Close()
	}
}

// killAgent kills the agent and then its whole process group. The agent goes first, so it cannot report its
// children's death on the output afm still reads.
func killAgent(cmd *exec.Cmd, group *processGroup) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	group.signal(syscall.SIGKILL)
}
