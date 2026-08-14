package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"syscall"
)

// ErrProcessDead is returned by Attach when no process is running at the
// given pid.
var ErrProcessDead = errors.New("afmsdk: process is not running")

// ErrPortUnreachable is returned by Attach when the process is alive but its
// dashboard API does not respond on the given port.
var ErrPortUnreachable = errors.New("afmsdk: dashboard port is not reachable")

// Attach reconstructs a Run for an afm subprocess started by a previous
// Start call — typically in a previous process, e.g. before this one
// restarted — given the run's working directory, dashboard port, and pid
// (the same values Run.Dir, Run.Port, and Run.PID reported for it before the
// restart). Status, Approve, Retry, and Revise work exactly as they would on
// the original Run; Wait and Cleanup return an error, since this Client
// never held the underlying *exec.Cmd needed to manage the process's
// lifecycle.
func (c *Client) Attach(ctx context.Context, dir string, port int, pid int) (*Run, error) {
	if !processAlive(pid) {
		return nil, ErrProcessDead
	}

	run := &Run{
		dir:        dir,
		baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		httpClient: c.httpClient,
		port:       port,
		pid:        pid,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, run.baseURL+"/api/status", nil)
	if err != nil {
		return nil, fmt.Errorf("afmsdk: attach: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ErrPortUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrPortUnreachable
	}

	return run, nil
}

// processAlive reports whether a process with the given pid is currently
// running. Sending signal 0 performs no action on the target process but
// still fails if it doesn't exist (or isn't ours), which is enough to probe
// liveness without disturbing it.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
