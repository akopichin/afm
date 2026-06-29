package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CreateShim creates a temp directory containing a "claude" shell script that
// overrides ANTHROPIC_BASE_URL to proxyAddr before invoking the real claude binary.
// The directory should be prepended to PATH in the agent's environment.
// Caller is responsible for defer os.RemoveAll(shimDir).
// Returns an error if claude is not found in the current PATH.
func CreateShim(proxyAddr string) (string, error) {
	realClaude, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude not found in PATH: %w", err)
	}

	dir, err := os.MkdirTemp("", "fm-proxy-shim-*")
	if err != nil {
		return "", fmt.Errorf("create shim dir: %w", err)
	}

	script := fmt.Sprintf("#!/bin/sh\nexec env ANTHROPIC_BASE_URL=%s %s \"$@\"\n",
		proxyAddr, realClaude)
	shimPath := filepath.Join(dir, "claude")
	if err := os.WriteFile(shimPath, []byte(script), 0755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("write shim: %w", err)
	}
	return dir, nil
}
