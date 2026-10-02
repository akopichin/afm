package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/browser"
)

// BrowserMCP describes the managed Chrome DevTools MCP to inject for an author
// runner. The Token is secret and reaches the MCP only through a per-invocation
// 0600 descriptor file — never argv, never the agent's own env.
type BrowserMCP struct {
	ServerName   string   // MCP server name, e.g. "afm_chrome"
	LauncherPath string   // resolved absolute afm path (MCP command for both claude and codex)
	WSEndpoint   string   // per-stage ws:// endpoint
	Token        string   // transport token (secret)
	AppURL       string   // tested-app URL
	AppPort      int      // tested-app port
	Roots        []string // filesystem roots the MCP may read/write
	// Codex is true when the agent is the codex adapter: it reads
	// AFM_BROWSER_DESCRIPTOR from env and builds its own -c overrides, so no
	// claude-style .mcp.json / --mcp-config is written.
	Codex bool
}

// BrowserMCPConfig returns the configured BrowserMCP (nil = capability off). It
// lets callers/tests confirm whether this runner received the browser capability.
func (e *Executor) BrowserMCPConfig() *BrowserMCP { return e.cfg.BrowserMCP }

// IsBrowserTransportEnv reports whether an env entry is a managed-browser
// transport variable (AFM_BROWSER_*). It is the single predicate used to
// default-deny the browser transport from every child process environment
// (author runners re-add only the non-secret subset afterwards).
func IsBrowserTransportEnv(kv string) bool {
	return strings.HasPrefix(kv, "AFM_BROWSER_")
}

// setupBrowserMCP writes the per-invocation 0600 descriptor file (and, for a
// claude-family command, the private .mcp.json) in a fresh temp dir. It returns
// the extra claude args (--mcp-config ...; empty for codex), the non-secret env
// to re-add for the author runner, and a cleanup removing the temp dir.
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
