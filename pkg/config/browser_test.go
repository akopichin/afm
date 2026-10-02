package config

import (
	"os/exec"
	"testing"

	"github.com/akopichin/afm/pkg/browser"
)

// chromeOK is a statFile seam that reports Chrome present.
var chromeOK = func(string) error { return nil }

func seamWithMCPVersion(ver string, statFile func(string) error) browserSeam {
	return browserSeam{
		lookPath:   func(string) (string, error) { return "/usr/local/bin/chrome-devtools-mcp", nil },
		statFile:   statFile,
		mcpVersion: func() (string, error) { return ver, nil },
	}
}

func seamNoMCP(statFile func(string) error) browserSeam {
	return browserSeam{
		lookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		statFile:   statFile,
		mcpVersion: func() (string, error) { return "", exec.ErrNotFound },
	}
}

// stubMCPVersionProbe swaps the package default seam so the public
// BrowserPreflight does not touch the real host. ver=="" means no MCP installed.
func stubMCPVersionProbe(t *testing.T, ver string) {
	t.Helper()
	prev := browserDefaultSeam
	t.Cleanup(func() { browserDefaultSeam = prev })
	if ver == "" {
		browserDefaultSeam = seamNoMCP(chromeOK)
	} else {
		browserDefaultSeam = seamWithMCPVersion(ver, chromeOK)
	}
}

func TestBrowserConfig_EffectiveEnable(t *testing.T) {
	tr := true
	fa := false
	if (BrowserConfig{}).EffectiveEnable() != EnableAuto {
		t.Fatal("nil enabled should be auto")
	}
	if (BrowserConfig{Enabled: &fa}).EffectiveEnable() != EnableOff {
		t.Fatal("explicit false disables")
	}
	t.Setenv("AFM_BROWSER", "0")
	if (BrowserConfig{Enabled: &tr}).EffectiveEnable() != EnableOff {
		t.Fatal("AFM_BROWSER=0 overrides explicit true")
	}
}

func TestBrowserConfig_EnvOnWithNilEnabled_NoNilDeref(t *testing.T) {
	t.Setenv("AFM_BROWSER", "1")
	if (BrowserConfig{}).EffectiveEnable() != EnableOn { // Enabled is nil
		t.Fatal("AFM_BROWSER=1 with nil enabled must resolve to EnableOn without nil-deref")
	}
}

func TestBrowserConfig_DefaultAppPort(t *testing.T) {
	if (BrowserConfig{}).GetAppPort() != 18080 {
		t.Fatalf("want 18080, got %d", (BrowserConfig{}).GetAppPort())
	}
}

// Port collision is a *preflight* concern, gated on the browser actually starting —
// not an unconditional static-config error (that would break disabled/degraded flows).
func TestBrowserPreflight_PortConflict_OnlyWhenStarting(t *testing.T) {
	p := 18080
	tr, fa := true, false
	// EnableOn + same port → hard error.
	on := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{Enabled: &tr, AppPort: 18080}}
	if _, err := on.preflightWithSeam(browser.BrowserModeNative, seamWithMCPVersion(PinnedChromeDevtoolsMCPVersion, chromeOK)); err == nil {
		t.Fatal("EnableOn + server.port==app_port must be a hard preflight error")
	}
	// EnableOff + same port → fully off, NO error (CLI/API-only project stays valid).
	off := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{Enabled: &fa, AppPort: 18080}}
	if _, err := off.BrowserPreflight(browser.BrowserModeNative); err != nil {
		t.Fatalf("EnableOff must never fail on port collision, got %v", err)
	}
	// EnableAuto but deps unavailable → degrade (Available:false), NO error, even on collision.
	stubMCPVersionProbe(t, "" /* no chrome-devtools-mcp on PATH */)
	auto := Config{Server: ServerConfig{Port: &p}, Browser: BrowserConfig{AppPort: 18080}} // nil → auto
	st, err := auto.BrowserPreflight(browser.BrowserModeNative)
	if err != nil || st.Available {
		t.Fatalf("auto-degraded must not fail the run on port collision: st=%+v err=%v", st, err)
	}
}

func TestBrowserPreflight_AutoMissingChromeIsDegradedNotError(t *testing.T) {
	c := Config{Browser: BrowserConfig{ExecutablePath: "/nonexistent/chrome"}}
	st, err := c.BrowserPreflight(browser.BrowserModeNative)
	if err != nil || st.Available {
		t.Fatalf("auto+missing: want degraded nil-err, got avail=%v err=%v", st.Available, err)
	}
}

func TestBrowserPreflight_ExplicitMissingChromeIsError(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr, ExecutablePath: "/nonexistent/chrome"}}
	if _, err := c.BrowserPreflight(browser.BrowserModeNative); err == nil {
		t.Fatal("explicit enable + missing chrome must error")
	}
}

func TestBrowserPreflight_NativeWrongMCPVersionIsMissing(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr}}
	// seam reports chrome-devtools-mcp present but version 1.9.0
	st, err := c.preflightWithSeam(browser.BrowserModeNative, seamWithMCPVersion("1.9.0", chromeOK))
	if err == nil || st.Available {
		t.Fatal("wrong pinned MCP version must count as a missing prerequisite (hard-fail when explicit)")
	}
}

func TestBrowserPreflight_DockerSkipsHostMCPCheck(t *testing.T) {
	tr := true
	c := Config{Browser: BrowserConfig{Enabled: &tr}}
	// no host MCP at all, but Docker mode provides it from the image
	st, err := c.preflightWithSeam(browser.BrowserModeDocker, seamNoMCP(chromeOK))
	if err != nil || !st.Available {
		t.Fatalf("docker must not require host MCP; got avail=%v err=%v", st.Available, err)
	}
}

func TestBrowserSupportedFor(t *testing.T) {
	c := Config{Docker: DockerConfig{Agents: map[string]AgentRecipe{
		"glm51":           {Type: ClaudeCommand},
		"codexrec":        {Type: RecipeTypeCodex},
		"deepseek-openai": {Type: RecipeTypeOpenAI},
	}}}
	if !c.BrowserSupportedFor("claude") {
		t.Fatal("claude must be supported")
	}
	if !c.BrowserSupportedFor("") {
		t.Fatal("empty (default claude) must be supported")
	}
	if !c.BrowserSupportedFor("glm51") {
		t.Fatal("claude-family recipe must be supported")
	}
	if !c.BrowserSupportedFor("codexrec") {
		t.Fatal("codex recipe must be supported")
	}
	if !c.BrowserSupportedFor("codex-as-claude") {
		t.Fatal("bare codex adapter must be supported")
	}
	if c.BrowserSupportedFor("deepseek-openai") {
		t.Fatal("openai recipe (no MCP tool-loop) must NOT be supported")
	}
}

func TestStage_BrowserDisabled_viaFlowConfig(t *testing.T) {
	// Covered in pkg/flow; here only confirm the config contract compiles.
	_ = BrowserStatus{}
}
