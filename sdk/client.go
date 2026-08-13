package afmsdk

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
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
