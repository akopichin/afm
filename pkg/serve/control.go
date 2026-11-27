package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/runregistry"
)

const (
	defaultStopGrace = 15 * time.Second
	stopPollEvery    = 100 * time.Millisecond
	killWait         = 2 * time.Second

	ActionCancel  = "cancel"
	ActionRelease = "release"

	StopStopped     = "stopped"
	StopKilled      = "killed"
	StopStopPending = "stop_pending"
)

// processStart reads a process's start marker; a variable so tests can describe the process table.
var processStart = runregistry.ProcessStart

// ErrNotManaged means the run was not started by this host: attached runs are never signalled.
var ErrNotManaged = errors.New("the run is attached, not started by this host; the host never signals it")

// PreconditionError means the run is in the wrong phase for the action.
type PreconditionError struct {
	Action    string
	RunStatus string
}

func (e *PreconditionError) Error() string {
	if e.Action == ActionCancel {
		return fmt.Sprintf("cancel needs an active workflow but the run is %s; use release for a finished run that is still held", e.RunStatus)
	}
	return fmt.Sprintf("release needs a finished workflow but the run is %s; use cancel to stop it", e.RunStatus)
}

// ControlResult says how a stop ended.
type ControlResult struct {
	Action string `json:"action"`
	RunID  string `json:"run_id"`
	// State is stopped, killed, or stop_pending for a Docker run that did not stop within the grace period.
	State string `json:"state"`
}

// runResolver and statusReader are what the controller needs from the inventory and the sessions.
type runResolver interface {
	Resolve(ctx context.Context, runID string) (RunView, error)
}

type statusReader interface {
	Status(ctx context.Context, v RunView) (ChildStatus, json.RawMessage, error)
}

// Controller stops managed runs: cancel for a workflow that is running, release for a finished one whose core
// runtime still holds the API. Both interrupt the run's leader process; a native run that ignores the interrupt
// is killed after the grace period, a Docker run never is.
type Controller struct {
	Host   *Host
	Runs   runResolver
	Status statusReader
	// Probe checks the endpoint's identity right before a signal; nil means ProbeRuntime.
	Probe func(ctx context.Context, endpoint string) (Runtime, error)
	// Signal sends a signal to a process whose start marker matches; nil means signalProcess.
	Signal func(pid int, marker string, sig syscall.Signal) error
	// Alive reports whether the process with this start marker still exists; nil reads the process table.
	Alive func(pid int, marker string) bool
	// Grace is how long a run may take to stop after the interrupt; zero means 15 seconds.
	Grace time.Duration
	Now   func() time.Time
	// ReleaseAfter releases a finished run that was held that long without a break; zero turns it off.
	ReleaseAfter time.Duration

	mu            sync.Mutex
	finishedSince map[string]time.Time
}

// Cancel stops a run whose workflow is active.
func (c *Controller) Cancel(ctx context.Context, runID string) (ControlResult, error) {
	return c.stop(ctx, runID, ActionCancel)
}

// Release stops a run whose workflow finished but whose runtime still holds the API.
func (c *Controller) Release(ctx context.Context, runID string) (ControlResult, error) {
	return c.stop(ctx, runID, ActionRelease)
}

func (c *Controller) stop(ctx context.Context, runID, action string) (ControlResult, error) {
	v, err := c.Runs.Resolve(ctx, runID)
	if err != nil {
		return ControlResult{}, err
	}
	if v.Ownership != OwnershipManaged {
		return ControlResult{}, ErrNotManaged
	}
	st, _, err := c.Status.Status(ctx, v)
	if err != nil {
		return ControlResult{}, err
	}
	if finished := runFinished(st.RunStatus); finished != (action == ActionRelease) {
		return ControlResult{}, &PreconditionError{Action: action, RunStatus: st.RunStatus}
	}
	if err := c.verifyIdentity(ctx, v); err != nil {
		return ControlResult{}, err
	}
	if action == ActionCancel && c.Host != nil && v.Launch != nil {
		receipt, err := c.Host.UpdateReceipt(v.Launch.LaunchID, time.Now(), func(r *Receipt) {
			if r.RunID == runID && !r.ProcessGone {
				r.CancelRequestedRunID = runID
			}
		})
		if err != nil {
			return ControlResult{}, err
		}
		if receipt.RunID != runID {
			return ControlResult{}, &RunChangedError{CurrentRunID: receipt.RunID}
		}
		if receipt.ProcessGone {
			return ControlResult{}, &RunUnavailableError{RunID: runID, Liveness: LiveExited}
		}
		if err := c.verifyIdentity(ctx, v); err != nil {
			return ControlResult{}, err
		}
	}
	// From here on the request's context no longer matters: a stop that started must reach its end.
	return c.interruptAndWait(v, action)
}

// runFinished tells a run whose workflow ended from one that is still working or has not started.
func runFinished(status string) bool {
	return slices.Contains([]string{string(state.RunStatusFinished), string(state.RunStatusFailed), string(state.RunStatusInterrupted)}, status)
}

// verifyIdentity proves the endpoint still belongs to the process, and still serves the run, that is about to
// be signalled: a repeat that moved on since the status was read must not be stopped under the old run id.
func (c *Controller) verifyIdentity(ctx context.Context, v RunView) error {
	probe := c.Probe
	if probe == nil {
		probe = ProbeRuntime
	}
	rt, err := probe(ctx, v.Endpoint)
	if err != nil {
		return remoteFailure("verify the run", err)
	}
	if rt.InstanceID != v.InstanceID {
		return fmt.Errorf("verify the run: %w", errIdentityChanged)
	}
	if rt.RunID != v.RunID {
		return &RunChangedError{CurrentRunID: rt.RunID}
	}
	return nil
}

func (c *Controller) interruptAndWait(v RunView, action string) (ControlResult, error) {
	result := ControlResult{Action: action, RunID: v.RunID}
	if err := c.signal(v, syscall.SIGINT); err != nil {
		return result, err
	}
	grace := c.Grace
	if grace == 0 {
		grace = defaultStopGrace
	}
	if c.waitGone(v, grace) {
		result.State = StopStopped
		return result, nil
	}
	if v.Docker {
		result.State = StopStopPending
		return result, nil
	}
	// The marker is checked again by signal: a process that exited and whose PID was reused is not killed.
	if err := c.signal(v, syscall.SIGKILL); err != nil {
		if !c.alive(v) {
			result.State = StopStopped
			return result, nil
		}
		return result, err
	}
	c.waitGone(v, killWait)
	result.State = StopKilled
	return result, nil
}

func (c *Controller) signal(v RunView, sig syscall.Signal) error {
	send := c.Signal
	if send == nil {
		send = signalProcess
	}
	return send(v.PID, v.ProcessStart, sig)
}

func (c *Controller) alive(v RunView) bool {
	if c.Alive != nil {
		return c.Alive(v.PID, v.ProcessStart)
	}
	marker, err := processStart(v.PID)
	return err == nil && marker == v.ProcessStart
}

func (c *Controller) waitGone(v RunView, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for c.alive(v) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(stopPollEvery)
	}
	return true
}

// Sweep releases finished runs that were held longer than ReleaseAfter. The time counts only while the run was
// seen finished on every sweep, so a host that was away does not release a run on stale evidence.
func (c *Controller) Sweep(ctx context.Context, views []RunView) []string {
	if c.ReleaseAfter <= 0 {
		return nil
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	seen := map[string]bool{}
	var released []string
	for _, v := range views {
		if v.Ownership != OwnershipManaged || (v.Liveness != LiveActive && v.Liveness != LiveActiveOrphaned) {
			continue
		}
		key := v.InstanceID + "/" + v.RunID
		st, _, err := c.Status.Status(ctx, v)
		if err != nil || !runFinished(st.RunStatus) {
			continue
		}
		seen[key] = true
		c.mu.Lock()
		if c.finishedSince == nil {
			c.finishedSince = map[string]time.Time{}
		}
		since, ok := c.finishedSince[key]
		if !ok {
			since = now
			c.finishedSince[key] = since
		}
		c.mu.Unlock()
		if now.Sub(since) < c.ReleaseAfter {
			continue
		}
		if _, err := c.Release(ctx, v.RunID); err == nil {
			released = append(released, v.RunID)
		}
	}
	c.mu.Lock()
	for key := range c.finishedSince {
		if !seen[key] {
			delete(c.finishedSince, key)
		}
	}
	c.mu.Unlock()
	return released
}
