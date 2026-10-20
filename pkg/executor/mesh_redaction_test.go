package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunAgent_MeshTokenRedactedFromRawOutputAndLogs(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "mesh-echo.sh", `
token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$AFM_MESH_DESCRIPTOR")
cat "$AFM_MESH_DESCRIPTOR"
printf 'Error: %s\n' "$token"
printf '%s' "$token" >&2
exit 1
`)
	const secret = "side-mesh-secret-should-stay-private"
	var minted string
	var revoked bool
	e := New(Config{
		Command: script, IdleTimeout: 5 * time.Second,
		MeshClient: &MeshClient{ParticipantID: "side:test", Mint: func() (string, string, func()) {
			minted = secret
			return "http://127.0.0.1:1234/api/agent-mesh", secret, func() { revoked = true }
		}},
		Secrets: func() []string { return []string{minted} },
	})
	logFile := filepath.Join(dir, "agent.log")
	_ = e.RunAgent(context.Background(), "side", "side-agent", "prompt", logFile)
	if !revoked {
		t.Fatal("Mesh token was not revoked")
	}
	for _, path := range []string{logFile, filepath.Join(dir, "agent.jsonl"), filepath.Join(dir, "agent.stderr.log")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("Mesh token leaked into %s", path)
		}
		if !strings.Contains(string(data), "[REDACTED]") {
			t.Fatalf("expected redacted token in %s", path)
		}
	}
}
