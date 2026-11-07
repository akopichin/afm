package afmsdk

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Config controls how a Client launches and manages afm runs.
type Config struct {
	// Binary is the path to the afm executable. A name without a path separator, and the empty default "afm",
	// is resolved from PATH in New.
	Binary string
	// MaxConcurrent caps the number of afm run subprocesses this Client will
	// have active at once. Zero means unlimited.
	MaxConcurrent int
	// Mode selects the afm launch mode. The zero value and ModeDashboard start the dashboard as before;
	// ModeCore starts the API-only core.
	Mode Mode
}

// Client launches and manages afm flow runs as subprocesses.
type Client struct {
	binary     string
	mode       Mode
	httpClient *http.Client
	sem        chan struct{}
}

// New builds a Client from cfg, resolving the afm binary from PATH if
// cfg.Binary is empty.
func New(cfg Config) (*Client, error) {
	if err := cfg.Mode.validate(); err != nil {
		return nil, err
	}
	binary := cfg.Binary
	if binary == "" {
		binary = "afm"
	}
	// Resolving here keeps Start free of the blocking PATH scan exec.Command would do after the last ctx check.
	if filepath.Base(binary) == binary {
		resolved, err := exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("afmsdk: afm binary %q not found on PATH: %w", binary, err)
		}
		binary = resolved
	}

	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}

	return &Client{
		binary:     binary,
		mode:       cfg.Mode,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		sem:        sem,
	}, nil
}

func pickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("afmsdk: pick free port: %w", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("afmsdk: pick free port: unexpected address type %T", ln.Addr())
	}
	return addr.Port, nil
}

func (c *Client) acquire(ctx context.Context) (func(), error) {
	if c.sem == nil {
		return func() {}, nil
	}
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runArgs passes --mode only for core so binaries without the flag still start in dashboard mode.
func (c *Client) runArgs(workDir, flowPath string, port int) []string {
	args := []string{"run", "--dir", workDir, "--port", strconv.Itoa(port)}
	if c.mode == ModeCore {
		args = append(args, "--mode", string(ModeCore))
	}
	return append(args, flowPath)
}

// Start launches afm and waits until the API is usable; ModeCore needs a ready core, never a silent dashboard.
// On failure the process is reaped and the slot freed before return, and workDir is kept.
func (c *Client) Start(ctx context.Context, flowPath, workDir string) (*Run, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		release()
		return nil, fmt.Errorf("afmsdk: create work dir %q: %w", workDir, err)
	}

	port, err := pickFreePort()
	if err != nil {
		release()
		return nil, err
	}

	cmd := exec.Command(c.binary, c.runArgs(workDir, flowPath, port)...)
	cmd.Dir = workDir
	out := &tailBuffer{max: maxErrorBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = processWaitDelay

	// Nothing that can block runs between this check and the spawn; a later cancel is handled by waitReady.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		release()
		return nil, fmt.Errorf("afmsdk: start %q: %w", c.binary, err)
	}

	run := &Run{
		cmd:        cmd,
		dir:        workDir,
		baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		httpClient: c.httpClient,
		out:        out,
		exited:     make(chan struct{}),
		port:       port,
		pid:        cmd.Process.Pid,
		mode:       ModeDashboard,
	}
	if c.mode == ModeCore {
		run.mode = ModeCore
	}
	go run.watchProcess(release)

	if err := run.waitReady(ctx); err != nil {
		return nil, err
	}
	return run, nil
}
