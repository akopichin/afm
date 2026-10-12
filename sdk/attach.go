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

// Attach reconnects to a previous run by directory, port, and PID. Wait/Cleanup are unavailable because
// this client does not own the subprocess.
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

// processAlive reports whether a process with the given pid is currently running.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
