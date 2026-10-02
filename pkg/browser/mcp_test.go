package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMCPConfig_WSHeadersIsString_And0600(t *testing.T) {
	d := &InvocationDescriptor{
		WSEndpoint: "ws://host.docker.internal:1/session/ep/build",
		Token:      "tok-SECRET",
		Roots:      []string{"/a/b", "/a/c"},
	}
	cfgPath, cleanup, err := WriteMCPConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config must be 0600, got %v", info.Mode().Perm())
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// wsHeaders MUST be a JSON string, not an object (pinned MCP JSON.parses it).
	var hdrs string
	if err := json.Unmarshal(raw["wsHeaders"], &hdrs); err != nil {
		t.Fatalf("wsHeaders must be a JSON string, got %s", raw["wsHeaders"])
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(hdrs), &obj); err != nil {
		t.Fatalf("wsHeaders string must itself be a JSON object: %v", err)
	}
	if obj["Authorization"] != "Bearer tok-SECRET" {
		t.Fatalf("Authorization header wrong: %q", obj["Authorization"])
	}
	// filesystemRoot is the lowest common ancestor of the roots.
	var fr string
	if err := json.Unmarshal(raw["filesystemRoot"], &fr); err != nil {
		t.Fatal(err)
	}
	if fr != "/a" {
		t.Fatalf("filesystemRoot LCA wrong: %q", fr)
	}
}

func TestRunMCP_RemovesPrivateConfigAndDescriptorOnExit(t *testing.T) {
	dir := t.TempDir()
	descPath := filepath.Join(dir, "desc.json")
	if err := WriteInvocationDescriptor(descPath, InvocationDescriptor{
		WSEndpoint: "ws://x/session/ep/s", Token: "tok", AppURL: "http://127.0.0.1:18080", Roots: []string{dir},
	}); err != nil {
		t.Fatal(err)
	}
	var cfgPath string
	err := runMCPSeamed(context.Background(), descPath, BrowserModeNative, mcpSeams{
		// Trivial child that exits immediately — no real MCP on PATH needed.
		resolve:         func(BrowserMode) (string, []string, error) { return "sh", []string{"-c", "exit 0"}, nil },
		child:           defaultMCPChild,
		onConfigWritten: func(p string) { cfgPath = p },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if cfgPath == "" {
		t.Fatal("config was not written")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("private MCP config must be deleted on exit, stat err=%v", err)
	}
	if _, err := os.Stat(descPath); !os.IsNotExist(err) {
		t.Fatalf("descriptor file must be deleted on exit, stat err=%v", err)
	}
}

func TestLowestCommonAncestor(t *testing.T) {
	if got := lowestCommonAncestor([]string{"/proj", "/proj/.afm/runs/r/build"}); got != "/proj" {
		t.Fatalf("want /proj, got %q", got)
	}
	if got := lowestCommonAncestor([]string{"/a/b", "/c/d"}); got != "/" {
		t.Fatalf("disjoint roots must fall back to /, got %q", got)
	}
	if got := lowestCommonAncestor([]string{"/only"}); got != "/only" {
		t.Fatalf("single root is itself, got %q", got)
	}
}
