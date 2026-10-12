package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeFakeCodexWithHelp имитирует --help и --output-last-message; argvFile сохраняет только рабочий
// вызов.
func writeFakeCodexWithHelp(t *testing.T, argvFile, helpOutput, lastMessage, jsonlOutput string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	content := "#!/usr/bin/env bash\n" +
		`if [[ "$1" == "exec" && "$2" == "--help" ]]; then` + "\n" +
		"  cat <<'HELPEOF'\n" + helpOutput + "\nHELPEOF\n" +
		"  exit 0\n" +
		"fi\n" +
		`echo "$@" > ` + strconv.Quote(argvFile) + "\n" +
		`prev=""` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [[ "$prev" == "--output-last-message" ]]; then` + "\n" +
		`    printf '%s' ` + strconv.Quote(lastMessage) + ` > "$a"` + "\n" +
		"  fi\n" +
		`  prev="$a"` + "\n" +
		"done\n" +
		"cat <<'FAKECODEXJSON'\n" + jsonlOutput + "\nFAKECODEXJSON\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return path
}

func TestCodexAsClaude_VerifyMode_ReadOnlyNoBypass(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fakeCodex := writeFakeCodexWithHelp(t, argvFile, "no special flags here",
		"", `{"type":"item.completed","item":{"type":"agent_message","text":"verdict text"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+t.TempDir(),
		"CODEX_VERIFY=1",
	)
	cmd.Stdin = strings.NewReader("verify this stage")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	argvData, rErr := os.ReadFile(argvFile)
	if rErr != nil {
		t.Fatalf("read argv file: %v\nscript output:\n%s", rErr, out)
	}
	argv := string(argvData)
	if !strings.Contains(argv, "-s read-only") {
		t.Errorf("argv missing -s read-only: %q", argv)
	}
	if strings.Contains(argv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("argv must not contain the bypass flag in verify mode: %q", argv)
	}
}

func TestCodexAsClaude_VerifyMode_DockerFullAccess(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fakeCodex := writeFakeCodexWithHelp(t, argvFile, "no special flags here",
		"", `{"type":"item.completed","item":{"type":"agent_message","text":"verdict text"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+t.TempDir(),
		"CODEX_VERIFY=1",
		"AFM_IN_DOCKER=1",
	)
	cmd.Stdin = strings.NewReader("verify this stage")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	argvData, rErr := os.ReadFile(argvFile)
	if rErr != nil {
		t.Fatalf("read argv file: %v\nscript output:\n%s", rErr, out)
	}
	argv := string(argvData)
	if !strings.Contains(argv, "-s danger-full-access") {
		t.Errorf("argv missing -s danger-full-access in docker verify: %q", argv)
	}
	if !strings.Contains(argv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("argv missing --dangerously-bypass-approvals-and-sandbox in docker verify: %q", argv)
	}
	if strings.Contains(argv, "-s read-only") {
		t.Errorf("argv must not contain -s read-only inside the container: %q", argv)
	}
}

func TestCodexAsClaude_VerifyMode_UsesOutputLastMessageWhenSupported(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	const exactFinalAnswer = `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`
	fakeCodex := writeFakeCodexWithHelp(t, argvFile,
		"  -o, --output-last-message <FILE>\n  -s, --sandbox <SANDBOX_MODE>",
		exactFinalAnswer,
		`{"type":"item.completed","item":{"type":"agent_message","text":"stale intermediate reasoning"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+t.TempDir(),
		"CODEX_VERIFY=1",
	)
	cmd.Stdin = strings.NewReader("verify this stage")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	argv, rErr := os.ReadFile(argvFile)
	if rErr != nil {
		t.Fatalf("read argv file: %v\nscript output:\n%s", rErr, out)
	}
	if !strings.Contains(string(argv), "--output-last-message") {
		t.Errorf("argv missing --output-last-message despite advertised support: %q", argv)
	}

	var assistantLine string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(l, `"type":"assistant"`) {
			assistantLine = l
		}
	}
	if assistantLine == "" {
		t.Fatalf("no assistant line in output:\n%s", out)
	}
	if !strings.Contains(assistantLine, "pass") || !strings.Contains(assistantLine, "schema_version") {
		t.Errorf("final text must come from --output-last-message content, got: %s", assistantLine)
	}
	if strings.Contains(assistantLine, "stale intermediate reasoning") {
		t.Errorf("final text must NOT be the aggregated agent_message when --output-last-message is supported: %s", assistantLine)
	}
}

func TestCodexAsClaude_VerifyMode_FallsBackToAggregatedWhenUnsupported(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fakeCodex := writeFakeCodexWithHelp(t, argvFile, "-s, --sandbox <SANDBOX_MODE>", "",
		`{"type":"item.completed","item":{"type":"agent_message","text":"the only available text"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+t.TempDir(),
		"CODEX_VERIFY=1",
	)
	cmd.Stdin = strings.NewReader("verify this stage")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	argv, rErr := os.ReadFile(argvFile)
	if rErr != nil {
		t.Fatalf("read argv file: %v\nscript output:\n%s", rErr, out)
	}
	if strings.Contains(string(argv), "--output-last-message") {
		t.Errorf("argv must not contain --output-last-message when unsupported: %q", argv)
	}

	if !strings.Contains(string(out), "the only available text") {
		t.Errorf("fallback aggregated text missing from output:\n%s", out)
	}
}

func TestCodexAsClaude_NonVerifyMode_UnchangedByDefault(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fakeCodex := writeFakeCodexWithHelp(t, argvFile,
		"  -o, --output-last-message <FILE>\n  -s, --sandbox <SANDBOX_MODE>", "",
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+t.TempDir(),
		// CODEX_VERIFY deliberately unset.
	)
	cmd.Stdin = strings.NewReader("do work")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	argv, rErr := os.ReadFile(argvFile)
	if rErr != nil {
		t.Fatalf("read argv file: %v\nscript output:\n%s", rErr, out)
	}
	got := string(argv)
	if !strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("non-verify argv must still contain the bypass flag: %q", got)
	}
	if !strings.Contains(got, "-s danger-full-access") {
		t.Errorf("non-verify argv must use the default sandbox: %q", got)
	}
	if strings.Contains(got, "-s read-only") {
		t.Errorf("non-verify argv must not switch to read-only: %q", got)
	}
	if strings.Contains(got, "--output-last-message") {
		t.Errorf("non-verify argv must not use --output-last-message even if supported: %q", got)
	}
}

func TestCodexAsClaude_VerifyMode_AggregatesNoToolRows(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	// Help output WITHOUT --output-last-message forces the aggregation fallback, keeping this test
	// independent of that flag's presence.
	fakeCodex := writeFakeCodexWithHelp(t, filepath.Join(t.TempDir(), "argv.txt"),
		"  -s, --sandbox <SANDBOX_MODE>", "",
		`{"type":"item.completed","item":{"type":"command_execution","command":"echo hi","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"pass\"}"}}
{"type":"item.completed","item":{"type":"agent_message","text":"tail"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(), "CODEX_BIN="+fakeCodex, "HOME="+t.TempDir(), "CODEX_VERIFY=1")
	cmd.Stdin = strings.NewReader("review")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	got := string(out)
	if strings.Contains(got, `"type":"tool_use"`) {
		t.Errorf("verify mode must not emit tool_use rows: %s", got)
	}
	var assistantCount int
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.Contains(l, `"type":"assistant"`) {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("verify mode must emit exactly ONE aggregated assistant line, got %d:\n%s", assistantCount, got)
	}
}
