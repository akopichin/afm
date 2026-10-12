package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// mcpExecutable is the pinned Chrome DevTools MCP binary name, resolved on PATH in both modes. Never npx,
// never an install.
const mcpExecutable = "chrome-devtools-mcp"

// PinnedChromeDevtoolsMCPVersion is the exact Chrome DevTools MCP version afm supports. Never @latest,
// never a runtime install.
const PinnedChromeDevtoolsMCPVersion = "1.10.1"

// InvocationDescriptor is the per-invocation 0600 file the executor writes and `afm browser mcp
// --descriptor <path>` reads.
type InvocationDescriptor struct {
	WSEndpoint string   `json:"wsEndpoint"` // full per-stage ws:// URL (Descriptor.StageEndpoint)
	Token      string   `json:"token"`
	AppURL     string   `json:"appURL"`
	Roots      []string `json:"roots"`
}

// mcpPrivateConfig is the Chrome-DevTools-MCP --config file.
type mcpPrivateConfig struct {
	WSEndpoint     string   `json:"wsEndpoint"`
	WSHeaders      string   `json:"wsHeaders"`
	FilesystemRoot []string `json:"filesystemRoot"`
}

// WriteInvocationDescriptor writes a 0600 descriptor file.
func WriteInvocationDescriptor(path string, d InvocationDescriptor) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadInvocationDescriptor reads a descriptor file.
func ReadInvocationDescriptor(path string) (*InvocationDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d InvocationDescriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("browser: parse descriptor: %w", err)
	}
	return &d, nil
}

// ResolveMCP resolves the pinned Chrome DevTools MCP executable.
func ResolveMCP(mode BrowserMode) (cmd string, args []string, err error) {
	_ = mode
	path, lerr := exec.LookPath(mcpExecutable)
	if lerr != nil || path == "" {
		return "", nil, fmt.Errorf("browser: %s not found on PATH (required pinned version %s; no install is attempted)", mcpExecutable, PinnedChromeDevtoolsMCPVersion)
	}
	return path, nil, nil
}

// WriteMCPConfig writes a private 0600 Chrome-DevTools-MCP config in a fresh 0700
// temp dir and returns its path plus a cleanup that removes the dir.
func WriteMCPConfig(d *InvocationDescriptor) (string, func(), error) {
	dir, err := os.MkdirTemp("", "afm-browser-mcp-")
	if err != nil {
		return "", nil, fmt.Errorf("browser: mcp config dir: %w", err)
	}
	cfg := mcpPrivateConfig{
		WSEndpoint:     d.WSEndpoint,
		WSHeaders:      fmt.Sprintf(`{"Authorization":"Bearer %s"}`, d.Token),
		FilesystemRoot: d.Roots,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// mcpSeams injects the resolver, child factory, and a config-written hook for tests.
type mcpSeams struct {
	resolve         func(BrowserMode) (string, []string, error)
	child           func(ctx context.Context, name string, args []string) *exec.Cmd
	onConfigWritten func(string)
}

func defaultMCPChild(ctx context.Context, name string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// RunMCP reads the descriptor, launches the pinned MCP as a stdio child with a
// private config, and removes BOTH the private config and the descriptor on exit.
func RunMCP(ctx context.Context, descriptorPath string, mode BrowserMode) error {
	return runMCPSeamed(ctx, descriptorPath, mode, mcpSeams{resolve: ResolveMCP, child: defaultMCPChild})
}

func runMCPSeamed(ctx context.Context, descriptorPath string, mode BrowserMode, s mcpSeams) error {
	desc, err := ReadInvocationDescriptor(descriptorPath)
	if err != nil {
		return err
	}
	cmdPath, baseArgs, err := s.resolve(mode)
	if err != nil {
		return err
	}
	cfgPath, cleanup, err := WriteMCPConfig(desc)
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() { _ = os.Remove(descriptorPath) }()
	if s.onConfigWritten != nil {
		s.onConfigWritten(cfgPath)
	}
	args := append(append([]string{}, baseArgs...), "--config="+cfgPath)
	c := s.child(ctx, cmdPath, args)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
