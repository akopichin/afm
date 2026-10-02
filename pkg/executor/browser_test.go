package executor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// authorBrowserMCP is a representative claude-family BrowserMCP.
func authorBrowserMCP() *BrowserMCP {
	return &BrowserMCP{
		ServerName:   "afm_chrome",
		LauncherPath: "/usr/local/bin/afm",
		WSEndpoint:   "ws://host.docker.internal:1/session/ep1/build",
		Token:        "tok-SECRET",
		AppURL:       "http://127.0.0.1:54321",
		AppPort:      18080,
		Roots:        []string{"/proj", "/afm/.afm/runs/r/build"},
	}
}

func TestSetupBrowserMCP_Claude_ArgsDescriptorEnv(t *testing.T) {
	e := New(Config{Command: "claude", BrowserMCP: authorBrowserMCP()})
	args, env, cleanup, err := e.setupBrowserMCP()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// claude gets --mcp-config
	if !hasFlagValue(args, "--mcp-config") {
		t.Fatalf("claude must get --mcp-config, args=%v", args)
	}
	// token never in argv
	for _, a := range args {
		if strings.Contains(a, "tok-SECRET") {
			t.Fatalf("token must not be in argv: %q", a)
		}
	}
	// descriptor path env present + is a 0600 file carrying token + roots
	descPath := envValue(env, "AFM_BROWSER_DESCRIPTOR")
	if descPath == "" {
		t.Fatal("AFM_BROWSER_DESCRIPTOR must be set")
	}
	info, err := os.Stat(descPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor must be 0600, got %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(descPath)
	var d struct {
		Token string   `json:"token"`
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Token != "tok-SECRET" || len(d.Roots) != 2 {
		t.Fatalf("descriptor must carry token + roots, got %+v", d)
	}
	// .mcp.json references --descriptor and does NOT contain the token
	mcpPath := flagValue(args, "--mcp-config")
	mcp, _ := os.ReadFile(mcpPath)
	if strings.Contains(string(mcp), "tok-SECRET") {
		t.Fatal("token must not be in the .mcp.json")
	}
	if !strings.Contains(string(mcp), "--descriptor") {
		t.Fatal(".mcp.json server args must pass --descriptor <path>")
	}
	// non-secret env re-add
	if envValue(env, "AFM_BROWSER_APP_URL") != "http://127.0.0.1:54321" {
		t.Fatal("must re-add AFM_BROWSER_APP_URL")
	}
	if envValue(env, "AFM_BROWSER_APP_PORT") != "18080" {
		t.Fatal("must re-add AFM_BROWSER_APP_PORT")
	}
	if envValue(env, "AFM_BROWSER_LAUNCHER") != "/usr/local/bin/afm" {
		t.Fatal("must re-add AFM_BROWSER_LAUNCHER")
	}
	// token must NOT be in the re-added env
	for _, kv := range env {
		if strings.Contains(kv, "tok-SECRET") {
			t.Fatalf("token must never be in the agent env: %q", kv)
		}
	}
}

func TestSetupBrowserMCP_Codex_NoMCPConfig(t *testing.T) {
	b := authorBrowserMCP()
	b.Codex = true
	e := New(Config{Command: "codex-as-claude", BrowserMCP: b})
	args, env, cleanup, err := e.setupBrowserMCP()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if hasFlagValue(args, "--mcp-config") {
		t.Fatal("codex adapter must NOT get a claude .mcp.json / --mcp-config")
	}
	if envValue(env, "AFM_BROWSER_DESCRIPTOR") == "" || envValue(env, "AFM_BROWSER_LAUNCHER") == "" {
		t.Fatal("codex still needs the descriptor path + launcher in env")
	}
}

func TestFilterInheritedEnv_StripsBrowserTransport(t *testing.T) {
	ambient := []string{"AFM_BROWSER_TRANSPORT_TOKEN=x", "AFM_BROWSER_ENDPOINT=y", "AFM_BROWSER_EPOCH=ep1", "PATH=/bin"}
	for _, cfg := range []Config{
		{VerifyMode: true}, // AI verify
		{},                 // JSON-fix / memory / plain author with no BrowserMCP
	} {
		e := New(cfg)
		out := e.filterInheritedEnv(ambient)
		for _, kv := range out {
			if IsBrowserTransportEnv(kv) {
				t.Fatalf("every child env must be stripped of AFM_BROWSER_* (cfg=%+v): %q", cfg, kv)
			}
		}
	}
}

func TestJSONQueryEnv_StripsBrowserTransport(t *testing.T) {
	e := New(Config{})
	ambient := []string{"AFM_BROWSER_TRANSPORT_TOKEN=x", "AFM_BROWSER_ENDPOINT=y", "PATH=/bin"}
	out := e.jsonQueryEnv(ambient)
	for _, kv := range out {
		if IsBrowserTransportEnv(kv) {
			t.Fatalf("RunJSONQuery (separate env path) must also strip browser transport: %q", kv)
		}
	}
}

// --- small arg/env helpers ---

func hasFlagValue(args []string, flag string) bool {
	return flagValue(args, flag) != ""
}

func flagValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func envValue(env []string, name string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return strings.TrimPrefix(kv, name+"=")
		}
	}
	return ""
}
