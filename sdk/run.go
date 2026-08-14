package afmsdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Run is a handle to a single afm flow run, started as a subprocess by
// Client.Start. All fields are populated by Start; a zero-value Run is only
// useful in tests that exercise a single method directly.
type Run struct {
	cmd        *exec.Cmd
	dir        string
	baseURL    string
	httpClient *http.Client
	out        *bytes.Buffer
	exited     chan struct{}
	waitErr    error
	port       int
	pid        int
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

const dashboardReadyTimeout = 30 * time.Second

// waitReady polls the run's dashboard API until it responds or the process
// exits or times out, so Client.Start only returns once the run is actually
// controllable.
func (r *Run) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(dashboardReadyTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/api/status", nil)
		if err == nil {
			if resp, err := r.httpClient.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		select {
		case <-r.exited:
			return fmt.Errorf("afmsdk: afm exited before dashboard became ready: %s", strings.TrimSpace(r.out.String()))
		case <-ctx.Done():
			_ = r.cmd.Process.Kill()
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}

		if time.Now().After(deadline) {
			_ = r.cmd.Process.Kill()
			return fmt.Errorf("afmsdk: dashboard did not become ready within %s", dashboardReadyTimeout)
		}
	}
}

// Wait blocks until the afm subprocess exits. If ctx is cancelled first, Wait
// sends SIGINT (afm only traps SIGINT for graceful shutdown — see
// cmd/afm/run.go's signal.NotifyContext(ctx, os.Interrupt); SIGTERM would hit
// the OS default "terminate immediately" and skip that shutdown entirely)
// and waits up to 15s for it to exit cleanly before killing it.
func (r *Run) Wait(ctx context.Context) error {
	if r.cmd == nil {
		return errors.New("afmsdk: Wait: not supported for a Run obtained via Attach")
	}
	select {
	case <-r.exited:
		return r.waitErr
	case <-ctx.Done():
	}

	_ = r.cmd.Process.Signal(os.Interrupt)
	select {
	case <-r.exited:
	case <-time.After(15 * time.Second):
		_ = r.cmd.Process.Kill()
		<-r.exited
	}
	if r.waitErr != nil {
		return r.waitErr
	}
	return ctx.Err()
}

// Cleanup removes this run's isolated state directory. It must be called
// after Wait has returned; calling it while the subprocess may still be
// running returns an error instead of deleting files out from under a live
// afm process.
func (r *Run) Cleanup() error {
	if r.cmd == nil {
		return errors.New("afmsdk: Cleanup: not supported for a Run obtained via Attach")
	}
	select {
	case <-r.exited:
	default:
		return errors.New("afmsdk: Cleanup called before Wait returned")
	}
	return os.RemoveAll(r.dir)
}
