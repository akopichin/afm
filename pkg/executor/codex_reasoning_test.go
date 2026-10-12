package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeArgRecordingCodex writes a fake codex that appends its argv (one arg per line) to argsFile, then
// emits a minimal valid turn.completed line so the adapter succeeds.
func writeArgRecordingCodex(t *testing.T) (binPath, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-codex")
	argsFile = filepath.Join(dir, "args.txt")
	content := "#!/usr/bin/env bash\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile +
		"\ncat <<'FAKECODEXJSON'\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\nFAKECODEXJSON\n"
	if err := os.WriteFile(binPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return binPath, argsFile
}

func runCodexForArgs(t *testing.T, fakeCodex string, extraEnv ...string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(), append([]string{"CODEX_BIN=" + fakeCodex, "HOME=" + t.TempDir()}, extraEnv...)...)
	cmd.Stdin = strings.NewReader("x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex script failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestCodexAsClaude_ReasoningForwarded(t *testing.T) {
	fakeCodex, argsFile := writeArgRecordingCodex(t)
	runCodexForArgs(t, fakeCodex, "CODEX_REASONING=high")
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	// -c and its value are separate argv entries.
	if !strings.Contains(string(args), "model_reasoning_effort=high") {
		t.Errorf("codex args missing model_reasoning_effort=high:\n%s", args)
	}
}

func TestCodexAsClaude_ReasoningUnsetIsClean(t *testing.T) {
	fakeCodex, argsFile := writeArgRecordingCodex(t)
	// No CODEX_REASONING set: must not abort under `set -u` and must not add the flag.
	runCodexForArgs(t, fakeCodex)
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.Contains(string(args), "model_reasoning_effort") {
		t.Errorf("unset reasoning must not add model_reasoning_effort:\n%s", args)
	}
}
