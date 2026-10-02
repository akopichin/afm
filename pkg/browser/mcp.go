package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// mcpExecutable is the pinned Chrome DevTools MCP binary name, resolved on PATH in
// both modes (installed in the image for Docker, already-installed on the host for
// native). Never npx, never an install.
const mcpExecutable = "chrome-devtools-mcp"

// PinnedChromeDevtoolsMCPVersion is the exact Chrome DevTools MCP version afm
// supports. It is pinned in Dockerfile.runtime (ARG CHROME_DEVTOOLS_MCP_VERSION)
// and required — same string — for the already-installed native executable.
// Never @latest, never a runtime install.
const PinnedChromeDevtoolsMCPVersion = "1.10.1"

// InvocationDescriptor is the per-invocation 0600 file the executor writes and
// `afm browser mcp --descriptor <path>` reads. It carries the secret token, so it
// is never placed in argv or env — only its path is non-secret.
type InvocationDescriptor struct {
	WSEndpoint string   `json:"wsEndpoint"`
	Token      string   `json:"token"`
	AppURL     string   `json:"appURL"`
	Roots      []string `json:"roots"`
}

// mcpPrivateConfig is the Chrome-DevTools-MCP --config file. wsHeaders is a
// JSON-encoded STRING (pinned 1.10.1 declares it as a string it JSON.parses), not
// an object — a bare object makes config parsing fail before the MCP starts.
type mcpPrivateConfig struct {
	WSEndpoint     string `json:"wsEndpoint"`
	WSHeaders      string `json:"wsHeaders"`
	FilesystemRoot string `json:"filesystemRoot"`
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

// ResolveMCP resolves the pinned Chrome DevTools MCP executable. Both modes look
// it up on PATH; a missing executable is a clear error naming the required pinned
// version (the Task 1 preflight blocks this earlier — this is defense in depth).
func ResolveMCP(mode BrowserMode) (cmd string, args []string, err error) {
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
		FilesystemRoot: lowestCommonAncestor(d.Roots),
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

// lowestCommonAncestor returns the deepest directory that is an ancestor of all
// paths, so a single filesystemRoot covers every requested root (safe even if the
// MCP accepts only one root).
func lowestCommonAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	lca := filepath.Clean(paths[0])
	for _, p := range paths[1:] {
		lca = commonPath(lca, filepath.Clean(p))
	}
	return lca
}

func commonPath(a, b string) string {
	sep := string(filepath.Separator)
	as := strings.Split(a, sep)
	bs := strings.Split(b, sep)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	var out []string
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			break
		}
		out = append(out, as[i])
	}
	res := strings.Join(out, sep)
	if res == "" {
		return sep
	}
	return res
}
