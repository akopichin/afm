package afmsdk

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Config controls how a Client launches and manages afm runs.
type Config struct {
	// Binary is the path to the afm executable. Empty resolves "afm" from PATH.
	Binary string
	// BaseDir is the parent directory for auto-generated, per-run isolated
	// state directories (passed to afm as --dir). Empty defaults to os.TempDir().
	BaseDir string
	// MaxConcurrent caps the number of afm run subprocesses this Client will
	// have active at once. Zero means unlimited.
	MaxConcurrent int
}

// Client launches and manages afm flow runs as subprocesses.
type Client struct {
	binary     string
	baseDir    string
	httpClient *http.Client
	sem        chan struct{}
}

// New builds a Client from cfg, resolving the afm binary from PATH if
// cfg.Binary is empty.
func New(cfg Config) (*Client, error) {
	binary := cfg.Binary
	if binary == "" {
		resolved, err := exec.LookPath("afm")
		if err != nil {
			return nil, fmt.Errorf("afmsdk: afm binary not found on PATH: %w", err)
		}
		binary = resolved
	}

	baseDir := cfg.BaseDir
	if baseDir == "" {
		baseDir = os.TempDir()
	}

	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}

	return &Client{
		binary:     binary,
		baseDir:    baseDir,
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

func newRunDir(base string) (string, error) {
	dir, err := os.MkdirTemp(base, "afm-run-*")
	if err != nil {
		return "", fmt.Errorf("afmsdk: create run dir under %q: %w", base, err)
	}
	return dir, nil
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

// Start launches "<afm> run --dir <isolated> --port <picked> <flowPath>" as a
// subprocess with its working directory set to workDir (where the flow's
// agents actually operate), and waits for its dashboard API to become
// reachable before returning.
func (c *Client) Start(ctx context.Context, flowPath, workDir string) (*Run, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}

	runDir, err := newRunDir(c.baseDir)
	if err != nil {
		release()
		return nil, err
	}

	port, err := pickFreePort()
	if err != nil {
		release()
		_ = os.RemoveAll(runDir)
		return nil, err
	}

	cmd := exec.Command(c.binary, "run", "--dir", runDir, "--port", strconv.Itoa(port), flowPath)
	cmd.Dir = workDir
	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		release()
		_ = os.RemoveAll(runDir)
		return nil, fmt.Errorf("afmsdk: start %q: %w", c.binary, err)
	}

	run := &Run{
		cmd:        cmd,
		dir:        runDir,
		baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		httpClient: c.httpClient,
		out:        out,
		exited:     make(chan struct{}),
	}
	go func() {
		run.waitErr = cmd.Wait()
		release()
		close(run.exited)
	}()

	if err := run.waitReady(ctx); err != nil {
		_ = os.RemoveAll(runDir)
		return nil, err
	}
	return run, nil
}
