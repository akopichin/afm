package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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

// codexExtItems is a helper-like run: a note, a failing and a passing command, an MCP call and the answer.
const codexExtItems = `{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"Looking at the tree"}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"false","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"false","aggregated_output":"","exit_code":1,"status":"failed"}}
{"type":"item.started","item":{"id":"item_2","type":"mcp_tool_call","server":"docs","tool":"search","arguments":{"query":"x"},"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_2","type":"mcp_tool_call","server":"docs","tool":"search","arguments":{"query":"x"},"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"first answer"}}
{"type":"item.completed","item":{"id":"item_4","type":"agent_message","text":"{\"verdict\":\"pass\"}"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`

// runCodexExt runs the wrapper in helper (verify-flag) mode with the stream extension and returns its stdout lines
// decoded as JSON objects, plus the exit error.
func runCodexExt(t *testing.T, fakeCodex string, extraEnv ...string) ([]map[string]any, error) {
	t.Helper()
	out, err := runCodexScript(t, fakeCodex, "review", append([]string{"CODEX_VERIFY=1", StreamExtensionEnv + "=1"}, extraEnv...)...)
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			lines = append(lines, m)
		}
	}
	return lines, err
}

func requireCodexTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not available")
		}
	}
}

// contentBlock is the first content block of an assistant or user line.
func contentBlock(line map[string]any) map[string]any {
	msg, _ := line["message"].(map[string]any)
	blocks, _ := msg["content"].([]any)
	if len(blocks) == 0 {
		return nil
	}
	block, _ := blocks[0].(map[string]any)
	return block
}

func lineTypes(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		out = append(out, fmt.Sprint(l["type"]))
	}
	return out
}

func finalLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["type"] == "afm_final" {
			out = append(out, l)
		}
	}
	return out
}

func TestCodexAsClaude_StreamExtension_TranslatesItemsWithLifecycle(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", "", codexExtItems, 0)
	lines, err := runCodexExt(t, fake)
	if err != nil {
		t.Fatalf("script failed: %v\n%v", err, lines)
	}
	if first := lines[0]; first["type"] != "system" || first["subtype"] != "afm_stream" || first["version"] != float64(1) {
		t.Fatalf("first line = %v, want the stream announcement", first)
	}
	byType := func(typ string) (out []map[string]any) {
		for _, l := range lines {
			if l["type"] == typ {
				out = append(out, l)
			}
		}
		return out
	}
	note := lines[1]
	if text := contentBlock(note)["text"]; text != "Looking at the tree" || note["afm"].(map[string]any)["item_id"] != "item_0" || !afmComplete(note) {
		t.Errorf("reasoning note = %v", note)
	}
	start, finish := contentBlock(lines[2]), contentBlock(lines[3])
	if start["type"] != "tool_use" || start["id"] != "item_1" || start["name"] != "Bash" || start["input"].(map[string]any)["command"] != "false" {
		t.Errorf("command start = %v", start)
	}
	hint := lines[3]["afm"].(map[string]any)
	if finish["type"] != "tool_result" || finish["tool_use_id"] != "item_1" || !isErrorResult(finish) ||
		hint["exit_code"] != float64(1) || hint["tool"] != "Bash" || hint["detail"] != "false" {
		t.Errorf("command finish = %v %v", finish, hint)
	}
	if mcp := contentBlock(lines[4]); mcp["id"] != "item_2" || mcp["name"] != "search" {
		t.Errorf("mcp start = %v", mcp)
	}
	if done := contentBlock(lines[5]); done["tool_use_id"] != "item_2" || !isCleanResult(done) {
		t.Errorf("mcp finish = %v", done)
	}
	finals := finalLines(lines)
	if len(finals) != 1 || finals[0]["text"] != `{"verdict":"pass"}` || finals[0]["item_id"] != "item_4" {
		t.Fatalf("afm_final = %v, want the last agent_message with its item id", finals)
	}
	if types := lineTypes(lines); types[len(types)-2] != "afm_final" || types[len(types)-1] != "result" {
		t.Errorf("line order = %v, want afm_final then result at the end", types)
	}
	if res := byType("result"); len(res) != 1 || res[0]["subtype"] != "success" || res[0]["channel"] != "codex" {
		t.Errorf("result = %v", res)
	}
}

func TestCodexAsClaude_StreamExtension_FinalFromLastMessageFileIsNotConcatenated(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	const answer = `{"verdict":"pass","summary":"from file"}`
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", answer, codexExtItems, 0)
	lines, err := runCodexExt(t, fake)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	finals := finalLines(lines)
	if len(finals) != 1 || finals[0]["text"] != answer {
		t.Fatalf("afm_final = %v, want exactly the file content", finals)
	}
	if _, named := finals[0]["item_id"]; named {
		t.Errorf("a file answer that is not a streamed message must not name an item: %v", finals[0])
	}
}

func TestCodexAsClaude_StreamExtension_FallbackIsTheLastMessageOnly(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "-s, --sandbox", "", codexExtItems, 0)
	lines, err := runCodexExt(t, fake)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if finals := finalLines(lines); len(finals) != 1 || finals[0]["text"] != `{"verdict":"pass"}` {
		t.Fatalf("afm_final = %v, want the last agent_message, not the joined ones", finals)
	}
}

func TestCodexAsClaude_StreamExtension_NoCandidateMeansNoFinal(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", "",
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","exit_code":0}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	lines, err := runCodexExt(t, fake)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if finals := finalLines(lines); len(finals) != 0 {
		t.Fatalf("afm_final without an answer: %v", finals)
	}
}

func TestCodexAsClaude_StreamExtension_FailureKeepsCandidateAndExitCode(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", "",
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"partial answer"}}
{"type":"turn.failed","error":{"message":"boom"}}`, 3)
	lines, err := runCodexExt(t, fake)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("exit = %v, want status 3", err)
	}
	if finals := finalLines(lines); len(finals) != 1 || finals[0]["text"] != "partial answer" {
		t.Errorf("afm_final = %v, want the candidate kept for audit", finals)
	}
	last := lines[len(lines)-1]
	if last["type"] != "result" || last["subtype"] != "error_during_execution" || !isErrorResult(last) || last["result"] != "boom" {
		t.Errorf("last line = %v, want the error result", last)
	}
}

func TestCodexAsClaude_StreamExtension_FinishWithoutStartAndMalformedLines(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", "",
		`not json at all
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","exit_code":0}}
{"type":"item.completed","item":{"type":"command_execution","command":"pwd","exit_code":0}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"ünïcode ответ"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	lines, err := runCodexExt(t, fake)
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	raw, _ := json.Marshal(lines)
	got := string(raw)
	if !strings.Contains(got, `"tool_use_id":"item_1"`) || !strings.Contains(got, `"command":"pwd"`) || strings.Contains(got, "not json") {
		t.Errorf("output = %s, want a finish without a start, an id-less command as a plain tool_use, no malformed line", got)
	}
	if finals := finalLines(lines); len(finals) != 1 || finals[0]["text"] != "ünïcode ответ" {
		t.Errorf("afm_final = %v", finals)
	}
}

// With the extension the verify argv is the same as without it, so the sandbox policy is untouched.
func TestCodexAsClaude_StreamExtension_KeepsVerifyArgv(t *testing.T) {
	requireCodexTools(t)
	cases := []struct {
		name string
		help string
		env  []string
	}{
		{"read-only no bypass", "no special flags", nil},
		{"docker full access", "no special flags", []string{"AFM_IN_DOCKER=1"}},
		{"output last message", "--output-last-message <FILE>", nil},
		{"falls back to aggregated", "-s, --sandbox", nil},
		{"output schema", "--output-last-message <FILE>", []string{"AFM_OUTPUT_SCHEMA_FILE=/tmp/schema.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argvOf := func(extension bool) string {
				argvFile := filepath.Join(t.TempDir(), "argv")
				fake := writeFakeCodexWithHelp(t, argvFile, tc.help, "answer",
					`{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"answer"}}`, 0)
				env := append([]string{"CODEX_VERIFY=1"}, tc.env...)
				if extension {
					env = append(env, StreamExtensionEnv+"=1")
				}
				if out, err := runCodexScript(t, fake, "verify", env...); err != nil {
					t.Fatalf("script failed: %v\n%s", err, out)
				}
				raw, err := os.ReadFile(argvFile)
				if err != nil {
					t.Fatal(err)
				}
				// The last-message file is a fresh temp path per run.
				return regexp.MustCompile(`--output-last-message \S+`).ReplaceAllString(string(raw), "--output-last-message FILE")
			}
			if plain, ext := argvOf(false), argvOf(true); plain != ext {
				t.Errorf("argv differs with the extension:\n plain: %s\n ext:   %s", plain, ext)
			}
		})
	}
}

// Verify output without the extension flag (or with any other value) is exactly what it was before.
func TestCodexAsClaude_VerifyWithoutExtensionIsByteForByte(t *testing.T) {
	requireCodexTools(t)
	const want = `{"type":"assistant","message":{"content":[{"type":"text","text":"final from file"}]}}
{"type":"result","subtype":"success","usage_contract_version":1,"usage_schema":"openai","channel":"codex","usage":{"input_tokens":5,"output_tokens":2}}
`
	for _, ext := range []string{"", "0", "2", "true"} {
		fake := writeFakeCodexWithHelp(t, filepath.Join(t.TempDir(), "argv"), "--output-last-message", "final from file",
			`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"first note"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"x\n","exit_code":0}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)
		env := []string{"CODEX_VERIFY=1"}
		if ext != "" {
			env = append(env, StreamExtensionEnv+"="+ext)
		}
		out, err := runCodexScript(t, fake, "verify", env...)
		if err != nil || out != want {
			t.Errorf("%s=%q: err %v, output\n%q\nwant\n%q", StreamExtensionEnv, ext, err, out, want)
		}
	}
}

// The first note and the first command are on stdout before codex exits, so the helper's screen is not empty.
func TestCodexAsClaude_StreamExtension_StreamsBeforeCodexExits(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "slow-fake-codex")
	gate := filepath.Join(dir, "gate")
	script := "#!/usr/bin/env bash\n" +
		`if [[ "$1" == "exec" && "$2" == "--help" ]]; then exit 0; fi` + "\n" +
		`printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"early note"}}'` + "\n" +
		`printf '%s\n' '{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"sleep 9"}}'` + "\n" +
		"until [ -e " + strconv.Quote(gate) + " ]; do sleep 0.05; done\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake codex
		t.Fatal(err)
	}
	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(), "CODEX_BIN="+fake, "HOME="+t.TempDir(), "CODEX_VERIFY=1", StreamExtensionEnv+"=1")
	cmd.Stdin = strings.NewReader("go")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The fake codex exits only once the gate is open, so both lines must arrive while it is still alive.
	release := func() { _ = os.WriteFile(gate, nil, 0o644) }
	defer func() { release(); _ = cmd.Wait() }()

	seen := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			seen <- sc.Text()
		}
		close(seen)
	}()
	var got []string
	deadline := time.After(10 * time.Second) // only a failure guard: the gate, not time, decides
	for !strings.Contains(strings.Join(got, "\n"), "early note") || !strings.Contains(strings.Join(got, "\n"), `"tool_use"`) {
		select {
		case l, ok := <-seen:
			if !ok {
				t.Fatalf("stream ended early: %v", got)
			}
			got = append(got, l)
		case <-deadline:
			t.Fatalf("only %v arrived while codex was alive — the wrapper buffers until it exits", got)
		}
	}
}

// The wrapper's lines, read by the real helper runner, become activity: messages, a tool row that is updated by
// its finish, and the answer named by afm_final.
func TestCodexAsClaude_StreamExtension_FeedsHelperActivity(t *testing.T) {
	requireCodexTools(t)
	dir := t.TempDir()
	fake := writeFakeCodexWithHelp(t, filepath.Join(dir, "argv"), "--output-last-message", "", codexExtItems, 0)
	cmd := filepath.Join(dir, "codex-e2e")
	script := "#!/usr/bin/env bash\nexport CODEX_BIN=" + strconv.Quote(fake) + " HOME=" + strconv.Quote(dir) + "\nexec bash " + strconv.Quote(codexScriptPath(t)) + " \"$@\"\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable wrapper
		t.Fatal(err)
	}
	var events []ActivityEvent
	ex := New(Config{Command: cmd, Dir: dir, OnActivity: func(ev ActivityEvent) { events = append(events, ev) }})
	out, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "review", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
	if err != nil || out.RawText != `{"verdict":"pass"}` {
		t.Fatalf("outcome %q err %v, want the afm_final answer", out.RawText, err)
	}
	var tools, finals int
	var failed bool
	for _, ev := range events {
		switch {
		case ev.Kind == KindToolFinished && ev.Outcome == OutcomeFailed && ev.ExitCode != nil && *ev.ExitCode == 1 && ev.Tool == "Bash" && ev.Detail == "false":
			failed = true
		case ev.Kind == KindToolStarted:
			tools++
		case ev.Kind == KindMessage && ev.Final:
			finals++
		default:
		}
	}
	last := events[len(events)-1]
	if tools != 2 || !failed || finals != 1 || last.Kind != KindInvocationFinished || last.Outcome != OutcomeCompleted {
		t.Errorf("activity = %+v, want two tool starts, the failed command finish, one final message, then a completed invocation", events)
	}
}
