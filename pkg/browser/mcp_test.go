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
	// filesystemRoot is an array carrying EVERY requested root (the MCP accepts
	// repeated roots): the project root plus the stage artifacts dir.
	var fr []string
	if err := json.Unmarshal(raw["filesystemRoot"], &fr); err != nil {
		t.Fatalf("filesystemRoot must be a JSON array, got %s: %v", raw["filesystemRoot"], err)
	}
	if len(fr) != 2 || fr[0] != "/a/b" || fr[1] != "/a/c" {
		t.Fatalf("filesystemRoot must carry all requested roots [/a/b /a/c], got %v", fr)
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

func TestWriteMCPConfig_CarriesAllRootsNeverWholeFS(t *testing.T) {
	// Divergent trees (afm-root != root_dir) must expose EACH requested root
	// explicitly, never a common ancestor that would widen to "/".
	d := &InvocationDescriptor{
		WSEndpoint: "ws://x/session/ep/s", Token: "t",
		Roots: []string{"/workspace", "/app/.afm/runs/r/s"},
	}
	cfgPath, cleanup, err := WriteMCPConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var fr []string
	if err := json.Unmarshal(raw["filesystemRoot"], &fr); err != nil {
		t.Fatal(err)
	}
	if len(fr) != 2 || fr[0] != "/workspace" || fr[1] != "/app/.afm/runs/r/s" {
		t.Fatalf("divergent roots must both be carried, never collapsed to an ancestor, got %v", fr)
	}
	for _, r := range fr {
		if r == "/" || r == "" {
			t.Fatalf("filesystemRoot must never be the whole FS or empty, got %v", fr)
		}
	}
}
