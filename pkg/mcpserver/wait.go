package mcpserver

import (
	"context"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/clock"
)

const (
	defaultWaitSeconds = 30
	maxWaitSeconds     = 60
	// maxWaits bounds the requests parked at once; each one holds a goroutine and an HTTP stream.
	maxWaits = 8

	codeWaitCapacity = "wait_capacity"
)

// runWaitInput is the argument of afm_run_wait.
type runWaitInput struct {
	RunID          string `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"cursor of the last status you saw; empty returns at once"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to block, default 30, at most 60"`
}

// waitResult is the answer of afm_run_wait: the fresh status plus how the wait ended.
type waitResult struct {
	// Changed is true when the attention model differs from the cursor the client sent.
	Changed bool `json:"changed"`
	// Reset is true when the cursor was empty-of-meaning (another run, unreadable): start over from this status.
	Reset    bool `json:"reset"`
	TimedOut bool `json:"timed_out"`
	statusResult
}

func (s *Service) acquireWait() (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiting >= maxWaits {
		return nil, &ToolError{Code: codeWaitCapacity, Kind: codeConflict, Message: "too many waits are open; retry shortly"}
	}
	s.waiting++
	return func() {
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}, nil
}

// wait blocks until the attention model differs from the client's cursor, the timeout passes, the request is
// cancelled or the service closes. It never touches the run's work: giving up a wait changes nothing.
func (s *Service) wait(ctx context.Context, in runWaitInput) (waitResult, error) {
	if err := s.ensureNotifier(); err != nil {
		return waitResult{}, err
	}
	releaseWait, err := s.acquireWait()
	if err != nil {
		return waitResult{}, err
	}
	defer releaseWait()

	if in.RunID != s.runID || (in.Cursor != "" && !s.cursorBelongs(in.Cursor)) {
		return waitResult{Reset: true, Changed: true, statusResult: s.buildStatus(ctx)}, nil
	}
	seconds := in.TimeoutSeconds
	if seconds <= 0 {
		seconds = defaultWaitSeconds
	}
	var timer clock.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		changed := s.changedChan()
		status := s.buildStatus(ctx)
		if in.Cursor == "" || status.Cursor != in.Cursor {
			return waitResult{Changed: true, statusResult: status}, nil
		}
		if timer == nil {
			// Started after the first read, so a started timer means the wait is parked on a known state.
			timer = s.clk().NewTimer(time.Duration(min(seconds, maxWaitSeconds)) * time.Second)
		}
		select {
		case <-changed:
		case <-timer.C():
			// A change without a bus impulse may land before the next notifier tick, so read again.
			status = s.buildStatus(ctx)
			return waitResult{Changed: status.Cursor != in.Cursor, TimedOut: true, statusResult: status}, nil
		case <-ctx.Done():
			return waitResult{}, ctx.Err()
		case <-s.done:
			return waitResult{}, errClosed
		}
	}
}

func (s *Service) registerWaitTool() {
	addTool(s, toolRunWait, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runWaitInput) (*sdkmcp.CallToolResult, any, error) {
		release, err := s.enter()
		if err != nil {
			return errorResult(err), nil, nil
		}
		defer release()
		res, err := s.wait(ctx, in)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return errorResult(err), nil, nil
		}
		return okResult(res), nil, nil
	})
}
