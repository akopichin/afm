package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/akopichin/afm/pkg/browser"
)

// BrowserMCP describes the managed Chrome DevTools MCP to inject for an author runner.
type BrowserMCP struct {
	ServerName   string   // MCP server name, e.g. "afm_chrome"
	LauncherPath string   // resolved absolute afm path (MCP command for both claude and codex)
	WSEndpoint   string   // per-stage ws:// endpoint
	Token        string   // transport token (secret)
	AppURL       string   // tested-app URL
	AppPort      int      // tested-app port
	Roots        []string // filesystem roots the MCP may read/write
	// Codex is true when the agent is the codex adapter: it reads AFM_BROWSER_DESCRIPTOR from env and builds
	// its own -c overrides, so no claude-style .mcp.json / --mcp-config is written.
	Codex bool
}

// BrowserMCPConfig returns the configured BrowserMCP (nil = capability off). It
// lets callers/tests confirm whether this runner received the browser capability.
func (e *Executor) BrowserMCPConfig() *BrowserMCP { return e.cfg.BrowserMCP }

// ReasoningConfig returns the configured reasoning level ("" = none). It lets tests confirm whether a
// runner received client.reasoning through to its executor.Config.
func (e *Executor) ReasoningConfig() string { return e.cfg.Reasoning }

// ProviderConfig returns the configured usage-limit provider ("" = unknown). It lets tests confirm that a
// factory stamped the provider into its executor.Config.
func (e *Executor) ProviderConfig() string { return e.cfg.Provider }

// IsBrowserTransportEnv reports whether an env entry is a managed-browser transport variable.
func IsBrowserTransportEnv(kv string) bool {
	return browser.IsTransportEnv(kv)
}

// setupBrowserMCP writes the per-invocation 0600 descriptor file in a fresh temp dir.
func (e *Executor) setupBrowserMCP() (args []string, env []string, cleanup func(), err error) {
	b := e.cfg.BrowserMCP
	dir, err := os.MkdirTemp("", "afm-browser-inv-")
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	descPath := filepath.Join(dir, "descriptor.json")
	if werr := browser.WriteInvocationDescriptor(descPath, browser.InvocationDescriptor{
		WSEndpoint: b.WSEndpoint,
		Token:      b.Token,
		AppURL:     b.AppURL,
		Roots:      b.Roots,
	}); werr != nil {
		cleanup()
		return nil, nil, nil, werr
	}

	// Non-secret env, re-added only for author runners. The token stays in the
	// 0600 descriptor file; only its PATH is exposed (AFM_BROWSER_DESCRIPTOR).
	env = []string{
		"AFM_BROWSER_APP_URL=" + b.AppURL,
		"AFM_BROWSER_APP_PORT=" + strconv.Itoa(b.AppPort),
		"AFM_BROWSER_DESCRIPTOR=" + descPath,
		"AFM_BROWSER_LAUNCHER=" + b.LauncherPath,
	}

	if !b.Codex {
		mcpPath := filepath.Join(dir, "mcp.json")
		cfg := map[string]any{
			"mcpServers": map[string]any{
				b.ServerName: map[string]any{
					"command": b.LauncherPath,
					"args":    []string{"browser", "mcp", "--descriptor", descPath},
				},
			},
		}
		data, merr := json.Marshal(cfg)
		if merr != nil {
			cleanup()
			return nil, nil, nil, merr
		}
		if werr := os.WriteFile(mcpPath, data, 0o600); werr != nil {
			cleanup()
			return nil, nil, nil, werr
		}
		args = []string{"--mcp-config", mcpPath}
	}
	return args, env, cleanup, nil
}
