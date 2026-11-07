package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Variables rather than constants so unit tests can shorten them; they are not public settings.
var (
	// shutdownGrace exceeds afm's own drain (15s agents, 2s pipes, 5s Assistant) so SIGINT is not cut short.
	shutdownGrace      = 30 * time.Second
	waitInterruptGrace = 15 * time.Second
	// processWaitDelay stops a child that inherited afm's pipes from hanging cmd.Wait and the MaxConcurrent slot.
	processWaitDelay = 5 * time.Second
)

// tailBuffer keeps only the last max bytes written, so a chatty afm cannot grow client memory.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if extra := len(b.buf) - b.max; extra > 0 {
		b.buf = append(b.buf[:0], b.buf[extra:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// watchProcess is the only caller of cmd.Wait; it fixes the outcome and frees the slot before signalling exit.
func (r *Run) watchProcess(release func()) {
	err := r.cmd.Wait()
	// A child holding the pipes after a clean exit is not a failure of afm itself.
	if errors.Is(err, exec.ErrWaitDelay) && r.cmd.ProcessState.ExitCode() == 0 {
		err = nil
	}
	r.waitErr = err
	r.killMu.Lock()
	r.killedByStop = r.killSent && diedOfSIGKILL(r.cmd.ProcessState)
	r.killMu.Unlock()
	release()
	close(r.exited)
}

// diedOfSIGKILL tells our Kill apart from any other signal death (SIGTERM, an external signal).
func diedOfSIGKILL(state *os.ProcessState) bool {
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}

// kill records a Kill only when it reached a process that was not yet reaped, so killMu orders it before
// watchProcess classifies the exit.
func (r *Run) kill() {
	r.killMu.Lock()
	defer r.killMu.Unlock()
	if r.cmd.Process.Kill() == nil {
		r.killSent = true
	}
}

// stop sends SIGINT once, then kills afm if it is still running when ctx ends, and waits for the process to be reaped.
func (r *Run) stop(ctx context.Context) {
	r.interruptOnce.Do(func() { _ = r.cmd.Process.Signal(os.Interrupt) })
	select {
	case <-r.exited:
		return
	case <-ctx.Done():
	}
	r.kill()
	<-r.exited
}

// Shutdown stops the afm process started by this Client: SIGINT, then Kill once ctx ends (30s when ctx has
// no deadline). Repeated and concurrent calls return the same result.
func (r *Run) Shutdown(ctx context.Context) error {
	if r.cmd == nil {
		return fmt.Errorf("afmsdk: Shutdown: %w", ErrNotOwned)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shutdownGrace)
		defer cancel()
	}
	r.stop(ctx)
	if r.killedByStop {
		return errors.Join(ErrKilled, r.waitErr)
	}
	return r.waitErr
}
