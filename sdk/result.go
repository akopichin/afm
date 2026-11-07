package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"
)

// Variables rather than constants so unit tests can shorten them; they are not public settings.
var (
	pollInterval = 200 * time.Millisecond
	// connectOutageWindow bounds how long a Connect handle, which cannot see the process, tolerates an unreachable API.
	connectOutageWindow = 30 * time.Second
)

// resultPoll holds what WaitResult has learned about the run between polls.
type resultPoll struct {
	run   *Run
	runID string
	ready bool
}

// WaitResult returns the final status (finished, failed or interrupted) of this run's workflow, not of the
// process. A failed stage under a running workflow is not final; another run yields *RunChangedError, and an
// API lost before a final status wraps ErrResultUnavailable (plus the exit error for an owned process).
func (r *Run) WaitResult(ctx context.Context) (RunStatus, error) {
	poll := &resultPoll{run: r}
	var outageSince time.Time
	for {
		status, done, err := poll.step(ctx)
		if done {
			return status, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return RunStatus{}, ctx.Err()
			}
			if !isNetworkError(err) {
				return RunStatus{}, err
			}
			if outageSince.IsZero() {
				outageSince = time.Now()
			}
			if r.cmd == nil && r.pid == 0 && time.Since(outageSince) > connectOutageWindow {
				return RunStatus{}, fmt.Errorf("%w: %w", ErrResultUnavailable, err)
			}
		} else {
			outageSince = time.Time{}
		}
		if gone := r.processGone(); gone != nil {
			return RunStatus{}, gone
		}

		select {
		case <-ctx.Done():
			return RunStatus{}, ctx.Err()
		case <-r.exited: // nil for handles that do not own the process
		case <-time.After(pollInterval):
		}
	}
}

// step reads the run once. Waiting for ready first means a final status left by an earlier process on the
// same directory is never mistaken for this run's result.
func (p *resultPoll) step(ctx context.Context) (RunStatus, bool, error) {
	if !p.ready {
		info, found, err := p.run.fetchRuntime(ctx)
		if err != nil {
			return RunStatus{}, false, err
		}
		if found {
			if err := info.check(); err != nil {
				return RunStatus{}, false, err
			}
			if !info.Ready {
				return RunStatus{}, false, nil
			}
		}
		p.ready = true
	}
	status, err := p.run.Status(ctx)
	if err != nil {
		return RunStatus{}, false, err
	}
	switch {
	case p.runID == "" && status.RunID == "":
		return RunStatus{}, false, fmt.Errorf("%w: status has no run_id", ErrIncompatibleRuntime)
	case p.runID == "":
		p.runID = status.RunID
	case status.RunID != p.runID:
		return RunStatus{}, false, &RunChangedError{Expected: p.runID, Current: status.RunID}
	default:
	}
	return status, status.EndedAt != nil && isTerminalRunState(status.RunState), nil
}

func isTerminalRunState(state string) bool {
	return state == RunStateFinished || state == RunStateFailed || state == RunStateInterrupted
}

// isNetworkError separates a broken connection, at dial time or mid-body (retried), from a reachable API that
// answered badly (returned).
func isNetworkError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.As(err, &urlErr) || errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
}

// processGone reports ErrResultUnavailable once the afm process behind this handle is known to be dead.
func (r *Run) processGone() error {
	if r.cmd != nil {
		select {
		case <-r.exited:
			if r.waitErr != nil {
				return fmt.Errorf("%w: afm exited: %w", ErrResultUnavailable, r.waitErr)
			}
			return fmt.Errorf("%w: afm exited before the run ended", ErrResultUnavailable)
		default:
			return nil
		}
	}
	if r.pid != 0 && !processAlive(r.pid) {
		return fmt.Errorf("%w: %w", ErrResultUnavailable, ErrProcessDead)
	}
	return nil
}
