package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Run is a handle to a single afm flow run, started as a subprocess by Client.Start.
type Run struct {
	cmd        *exec.Cmd
	dir        string
	baseURL    string
	httpClient *http.Client
	out        *tailBuffer
	exited     chan struct{}
	waitErr    error // written before exited is closed
	port       int
	pid        int
	mode       Mode

	interruptOnce sync.Once
	killMu        sync.Mutex // orders a Kill against watchProcess deciding whether that Kill ended afm
	killSent      bool       // guarded by killMu: a Kill reached a live afm
	killedByStop  bool       // written before exited is closed
}

// Dir returns the isolated directory (--dir) this run's afm state lives in.
func (r *Run) Dir() string {
	return r.dir
}

// Port returns the TCP port this run's dashboard API listens on.
func (r *Run) Port() int {
	return r.port
}

// PID returns the OS process id of this run's afm subprocess.
func (r *Run) PID() int {
	return r.pid
}

// readyTimeout is a variable so unit tests can shorten it.
var readyTimeout = 30 * time.Second

// waitReady polls until the run is controllable, so Client.Start returns only a usable handle. On any
// failure the process is stopped and reaped before returning.
func (r *Run) waitReady(ctx context.Context) error {
	timeout := time.NewTimer(readyTimeout)
	defer timeout.Stop()
	problem := ""
	for {
		select {
		case <-r.exited:
			return fmt.Errorf("afmsdk: afm exited before %s became ready: %s", r.mode, r.out.String())
		default:
		}
		ready, fatal := r.probeReady(ctx, &problem)
		if fatal != nil {
			return r.abortStart(fatal)
		}
		if ready {
			return nil
		}

		select {
		case <-r.exited:
		case <-ctx.Done():
			return r.abortStart(ctx.Err())
		case <-timeout.C:
			return r.abortStart(fmt.Errorf("afmsdk: %s did not become ready within %s: %s", r.mode, readyTimeout, problem))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// abortStart stops the process and reaps it so the MaxConcurrent slot is free when Start returns.
func (r *Run) abortStart(cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), waitInterruptGrace)
	defer cancel()
	r.stop(ctx)
	if tail := r.out.String(); tail != "" {
		return fmt.Errorf("%w\nafm output: %s", cause, tail)
	}
	return cause
}

// probeReady reports whether the API is usable. Only a runtime this SDK cannot drive is fatal; anything else
// may still resolve while afm starts, and is recorded in problem for the timeout message.
func (r *Run) probeReady(ctx context.Context, problem *string) (ready bool, fatal error) {
	if r.mode != ModeCore {
		_, err := r.request(ctx, http.MethodGet, "/api/status", nil, maxJSONBytes)
		return err == nil, nil
	}
	info, found, err := r.fetchRuntime(ctx)
	if err != nil {
		*problem = err.Error()
		return false, nil
	}
	if !found {
		return false, fmt.Errorf("%w: /api/runtime not served, afm is too old for core mode", ErrIncompatibleRuntime)
	}
	if err := info.check(); err != nil {
		return false, err
	}
	if Mode(info.Mode) != ModeCore {
		return false, fmt.Errorf("%w: afm runs in %q mode, want core", ErrIncompatibleRuntime, info.Mode)
	}
	if !info.Ready || info.RunID == "" {
		*problem = "runtime not ready"
		return false, nil
	}
	status, err := r.Status(ctx)
	if err != nil {
		*problem = err.Error()
		return false, nil
	}
	if status.RunID != info.RunID {
		*problem = fmt.Sprintf("status run_id %q differs from runtime run_id %q", status.RunID, info.RunID)
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".afm", "runs", info.RunID)); err != nil {
		*problem = "run directory missing: " + err.Error()
		return false, nil
	}
	return true, nil
}

// Mode returns the afm runtime mode of this run; empty when the afm predates /api/runtime.
func (r *Run) Mode() Mode {
	return r.mode
}

// Wait blocks until the afm subprocess exits. If ctx ends first it sends SIGINT, then kills afm after 15s.
func (r *Run) Wait(ctx context.Context) error {
	if r.cmd == nil {
		return fmt.Errorf("afmsdk: Wait: not supported for a Run obtained via Attach: %w", ErrNotOwned)
	}
	select {
	case <-r.exited:
		return r.waitErr
	case <-ctx.Done():
	}

	graceCtx, cancel := context.WithTimeout(context.Background(), waitInterruptGrace)
	defer cancel()
	r.stop(graceCtx)
	if r.waitErr != nil {
		return r.waitErr
	}
	return ctx.Err()
}

// Cleanup removes this run's isolated state directory.
func (r *Run) Cleanup() error {
	if r.cmd == nil {
		return fmt.Errorf("afmsdk: Cleanup: not supported for a Run obtained via Attach: %w", ErrNotOwned)
	}
	select {
	case <-r.exited:
	default:
		return errors.New("afmsdk: Cleanup called before Wait returned")
	}
	return os.RemoveAll(r.dir)
}
