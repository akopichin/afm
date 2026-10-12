package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/akopichin/afm/pkg/browser"
)

// PinnedChromeDevtoolsMCPVersion re-exports the pinned Chrome DevTools MCP version
// from pkg/browser so config callers (preflight, messages) share one source.
const PinnedChromeDevtoolsMCPVersion = browser.PinnedChromeDevtoolsMCPVersion

// defaultBrowserAppPort is the port the tested app is served on by default.
const defaultBrowserAppPort = 18080

// defaultChromePath is the macOS Chrome location used when ExecutablePath is unset.
const defaultChromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// BrowserConfig configures host Chrome access through afm_chrome for native and Docker stage agents.
type BrowserConfig struct {
	// Enabled is a tri-state switch: nil → auto, true → on, false → fully off.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Headless, if true, launches Chrome without a visible window. Default visible.
	Headless *bool `yaml:"headless,omitempty"`
	// AppPort is the port the tested app is served on. Default 18080.
	AppPort int `yaml:"app_port,omitempty"`
	// ExecutablePath overrides the Chrome binary location (default: macOS Chrome).
	ExecutablePath string `yaml:"executable_path,omitempty"`
}

// EnableState is the resolved tri-state of the browser capability.
type EnableState int

const (
	// EnableAuto offers tools when the host supports them and degrades silently.
	EnableAuto EnableState = iota
	// EnableOn requires preflight to succeed or the run fails.
	EnableOn
	// EnableOff disables the capability entirely.
	EnableOff
)

// BrowserStatus is the outcome of a host preflight: whether the capability is
// available and, if not, a human-readable reason.
type BrowserStatus struct {
	Available bool
	Reason    string
}

// browserSeam injects host probes so preflight is testable without the real host.
type browserSeam struct {
	lookPath   func(string) (string, error)
	statFile   func(string) error
	mcpVersion func() (string, error)
}

// browserDefaultSeam is the production seam; tests swap it to avoid the real host.
var browserDefaultSeam = browserSeam{
	lookPath: exec.LookPath,
	// Reject directories and non-executable paths at startup, before the first browser tool call.
	statFile: func(p string) error {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory, not the Chrome executable", p)
		}
		if fi.Mode()&0o111 == 0 {
			return fmt.Errorf("%s is not executable", p)
		}
		return nil
	},
	mcpVersion: func() (string, error) {
		out, err := exec.Command("chrome-devtools-mcp", "--version").Output()
		return string(out), err
	},
}

// EffectiveEnable resolves the AFM_BROWSER env override over the yaml `enabled` tri-state.
func (b BrowserConfig) EffectiveEnable() EnableState {
	if v, set := envBool("AFM_BROWSER"); set {
		if v {
			return EnableOn
		}
		return EnableOff
	}
	if b.Enabled == nil {
		return EnableAuto
	}
	if *b.Enabled {
		return EnableOn
	}
	return EnableOff
}

// IsEnabled reports whether the capability offers tools at all (auto or on);
// preflight decides auto-vs-hard-fail.
func (b BrowserConfig) IsEnabled() bool {
	return b.EffectiveEnable() != EnableOff
}

// GetAppPort returns the configured app port or the default.
func (b BrowserConfig) GetAppPort() int {
	if b.AppPort == 0 {
		return defaultBrowserAppPort
	}
	return b.AppPort
}

// IsHeadless reports whether Chrome should launch headless (default visible).
func (b BrowserConfig) IsHeadless() bool {
	return b.Headless != nil && *b.Headless
}

// ChromePath returns the configured Chrome executable path or the macOS default.
func (b BrowserConfig) ChromePath() string {
	if b.ExecutablePath != "" {
		return b.ExecutablePath
	}
	return defaultChromePath
}

// BrowserSupportedFor reports whether a command can drive the afm_chrome MCP: true for claude-family
// agents and the codex adapter — those speak the MCP tool-loop.
func (c Config) BrowserSupportedFor(command string) bool {
	resolved, _ := ResolveAgentCommand(command, c)
	if recipe, ok := c.Docker.Agents[resolved]; ok {
		switch recipe.Type {
		case "", ClaudeCommand, RecipeTypeCodex:
			return true
		default:
			return false
		}
	}
	return resolved == ClaudeCommand || resolved == codexAdapterCommand
}

// ValidateBrowser checks only the always-safe static field sanity. It is a no-op when the capability is
// off.
func (c Config) ValidateBrowser() error {
	if c.Browser.EffectiveEnable() == EnableOff {
		return nil
	}
	if p := c.Browser.AppPort; p != 0 && (p < 1024 || p > 65535) {
		return fmt.Errorf("browser.app_port: must be an unprivileged port 1024..65535, got %d", p)
	}
	return nil
}

// BrowserPreflight checks host prerequisites using the production seam.
func (c Config) BrowserPreflight(mode browser.BrowserMode) (BrowserStatus, error) {
	return c.preflightWithSeam(mode, browserDefaultSeam)
}

// preflightWithSeam is the testable core.
func (c Config) preflightWithSeam(mode browser.BrowserMode, seam browserSeam) (BrowserStatus, error) {
	state := c.Browser.EffectiveEnable()
	if state == EnableOff {
		return BrowserStatus{Available: false, Reason: "browser disabled"}, nil
	}
	if reason := c.browserMissingPrereq(mode, seam); reason != "" {
		if state == EnableOn {
			return BrowserStatus{Available: false, Reason: reason}, fmt.Errorf("browser: %s", reason)
		}
		return BrowserStatus{Available: false, Reason: reason}, nil
	}
	return BrowserStatus{Available: true}, nil
}

// browserMissingPrereq returns a reason if a host prerequisite is missing, else "".
func (c Config) browserMissingPrereq(mode browser.BrowserMode, seam browserSeam) string {
	chromePath := c.Browser.ChromePath()
	if err := seam.statFile(chromePath); err != nil {
		return fmt.Sprintf("Chrome not found at %q", chromePath)
	}
	// Native: the pinned chrome-devtools-mcp must already be installed. Docker
	// provides it from the image, so no host MCP check there.
	if mode == browser.BrowserModeNative {
		if path, err := seam.lookPath("chrome-devtools-mcp"); err != nil || path == "" {
			return fmt.Sprintf("chrome-devtools-mcp not installed (required pinned version %s; no install is attempted)", PinnedChromeDevtoolsMCPVersion)
		}
		ver, err := seam.mcpVersion()
		if err != nil || !mcpVersionMatches(ver, PinnedChromeDevtoolsMCPVersion) {
			return fmt.Sprintf("chrome-devtools-mcp version %q does not match required pinned %s", strings.TrimSpace(ver), PinnedChromeDevtoolsMCPVersion)
		}
	}
	// Port collision checked only here (the manager would actually start).
	if c.Server.GetPort() == c.Browser.GetAppPort() {
		return fmt.Sprintf("server.port (%d) conflicts with browser.app_port (%d)", c.Server.GetPort(), c.Browser.GetAppPort())
	}
	return ""
}

// mcpVersionMatches reports whether a `--version` output names the wanted version
// as a whole token (so 1.10.1 never matches 1.10.10).
func mcpVersionMatches(out, want string) bool {
	for _, f := range strings.Fields(out) {
		if strings.TrimPrefix(f, "v") == want {
			return true
		}
	}
	return strings.TrimSpace(out) == want
}
