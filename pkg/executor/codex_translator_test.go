package executor

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
)

// codexScriptPath returns the absolute path to scripts/codex-as-claude.sh.
func codexScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	p := filepath.Join(root, "scripts", "codex-as-claude.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("script not found: %s: %v", p, err)
	}
	return p
}

// writeFakeCodex writes a fake codex binary that prints jsonlOutput verbatim to stdout and exits with
// exitCode. Returns the absolute path to the fake binary.
func writeFakeCodex(t *testing.T, jsonlOutput string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	content := "#!/usr/bin/env bash\ncat <<'FAKECODEXJSON'\n" + jsonlOutput + "\nFAKECODEXJSON\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return path
}

// codexControlVars are the environment variables scripts/codex-as-claude.sh reads to decide its behavior.
var codexControlVars = []string{
	"AFM_IN_DOCKER", "CODEX_BIN", "CODEX_HOME", "CODEX_MODEL", "CODEX_REASONING",
	"CODEX_SANDBOX", "CODEX_VERBOSE", "CODEX_VERIFY", StopHookCodexHooksEnv, StopHookCodexStateEnv, StopHookDescriptorEnv,
}

// codexBaseEnv returns os.Environ() with every codexControlVars entry removed —
// a hermetic base for codex-script tests to append their own settings onto.
func codexBaseEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if !slices.Contains(codexControlVars, name) {
			out = append(out, kv)
		}
	}
	return out
}

// runCodexScript uses fake Codex and an isolated HOME so tests cannot read the developer's config.
func runCodexScript(t *testing.T, fakeCodexPath, prompt string, extraEnv ...string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("bash", codexScriptPath(t))
	isolatedHome := t.TempDir()
	env := append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodexPath,
		"HOME="+isolatedHome,
	)
	cmd.Env = append(env, extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

// extractResultLine returns the last "type":"result" line from script output.
func extractResultLine(t *testing.T, out string) string {
	t.Helper()
	var resultLine string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, `"type":"result"`) {
			resultLine = l
		}
	}
	if resultLine == "" {
		t.Fatalf("no result line in output:\n%s", out)
	}
	return resultLine
}

func TestCodexAsClaude_UsageEnvelopeMatchesGoldenShape(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":22005,"cached_input_tokens":10752,"cache_write_input_tokens":0,"output_tokens":8,"reasoning_output_tokens":0}}`, 0)

	out, err := runCodexScript(t, fakeCodex, "do the thing", "CODEX_MODEL=gpt-5.6-sol")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	resultLine := extractResultLine(t, out)

	var env map[string]any
	if jsonErr := json.Unmarshal([]byte(resultLine), &env); jsonErr != nil {
		t.Fatalf("result line is not valid JSON: %v\nline: %s", jsonErr, resultLine)
	}
	if env["usage_contract_version"] != float64(1) {
		t.Errorf("usage_contract_version = %v, want 1", env["usage_contract_version"])
	}
	if env["usage_schema"] != "openai" {
		t.Errorf("usage_schema = %v, want %q", env["usage_schema"], "openai")
	}
	if env["channel"] != "codex" {
		t.Errorf("channel = %v, want %q", env["channel"], "codex")
	}
	if env["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %v, want %q", env["model"], "gpt-5.6-sol")
	}
	usage, ok := env["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing/wrong shape: %v", env["usage"])
	}
	wantUsage := map[string]float64{
		"input_tokens":             22005,
		"cached_input_tokens":      10752,
		"cache_write_input_tokens": 0,
		"output_tokens":            8,
		"reasoning_output_tokens":  0,
	}
	for k, want := range wantUsage {
		got, ok := usage[k].(float64)
		if !ok || got != want {
			t.Errorf("usage[%q] = %v, want %v", k, usage[k], want)
		}
	}

	// Cross-check against the canonical fixture through the REAL Go collector:
	// both must normalize to identical tokens (the whole point of this contract).
	golden, readErr := os.ReadFile(filepath.Join("..", "accounting", "testdata", "codex-gpt-5.6-sol.jsonl"))
	if readErr != nil {
		t.Fatalf("read golden fixture: %v", readErr)
	}
	cGolden := accounting.NewCollector(accounting.UsageHint{})
	cGolden.Observe([]byte(strings.TrimSpace(string(golden))))
	obsGolden := cGolden.Finish(nil)

	cScript := accounting.NewCollector(accounting.UsageHint{})
	cScript.Observe([]byte(resultLine))
	obsScript := cScript.Finish(nil)

	if !obsScript.Metered {
		t.Fatalf("script envelope must be metered: %+v", obsScript)
	}
	if obsScript.Tokens != obsGolden.Tokens {
		t.Errorf("tokens from live script = %+v, want match with golden fixture %+v", obsScript.Tokens, obsGolden.Tokens)
	}
	if obsScript.Schema != obsGolden.Schema || obsScript.Channel != obsGolden.Channel || obsScript.Model != obsGolden.Model {
		t.Errorf("schema/channel/model = (%q,%q,%q), want (%q,%q,%q)",
			obsScript.Schema, obsScript.Channel, obsScript.Model,
			obsGolden.Schema, obsGolden.Channel, obsGolden.Model)
	}
}

func TestCodexAsClaude_NoSecretOrPromptLeakage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	const secretMarker = "sk-super-secret-token-should-never-leak"
	const promptMarker = "PROMPT_TEXT_MUST_NOT_LEAK_xyz123"

	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"some agent text, not the envelope"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}`, 0)

	out, err := runCodexScript(t, fakeCodex, promptMarker, "CODEX_MODEL=gpt-5.6-sol", "SOME_API_KEY="+secretMarker)
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, out)

	if strings.Contains(resultLine, secretMarker) {
		t.Errorf("result line leaked a secret-shaped value: %s", resultLine)
	}
	if strings.Contains(resultLine, promptMarker) {
		t.Errorf("result line leaked prompt text: %s", resultLine)
	}
}

// TestCodexAsClaude_ModelPrecedence_EnvOverridesToml: an explicit CODEX_MODEL
// always wins over the fallback TOML config, even when both are present.
func TestCodexAsClaude_ModelPrecedence_EnvOverridesToml(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}`, 0)

	home := t.TempDir()
	if mkErr := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	toml := "model = \"toml-model-should-lose\"\n"
	if writeErr := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(toml), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(),
		"CODEX_BIN="+fakeCodex,
		"HOME="+home,
		"CODEX_MODEL=env-model-should-win",
	)
	cmd.Stdin = strings.NewReader("x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, string(out))
	if !strings.Contains(resultLine, `"model":"env-model-should-win"`) {
		t.Errorf("expected env model to win, got: %s", resultLine)
	}
}

func TestCodexAsClaude_ModelFallsBackToTopLevelTomlOnly(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}`, 0)

	home := t.TempDir()
	if mkErr := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	toml := "model = \"top-level-model\"\napproval_policy = \"never\"\n\n[profiles.other]\nmodel = \"nested-model-should-not-be-used\"\n"
	if writeErr := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(toml), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	// runCodexScript sets its own isolated HOME internally; this test needs a specific HOME, so it builds the
	// command directly instead of using the shared helper.
	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(), "CODEX_BIN="+fakeCodex, "HOME="+home)
	cmd.Stdin = strings.NewReader("x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, string(out))
	if !strings.Contains(resultLine, `"model":"top-level-model"`) {
		t.Errorf("expected top-level toml model, got: %s", resultLine)
	}
	if strings.Contains(resultLine, "nested-model-should-not-be-used") {
		t.Errorf("must not pick up a model nested under a later [table]: %s", resultLine)
	}
}

func TestCodexAsClaude_NoUsageCapturedOmitsContractFields(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"no usage this time"}}`, 0)

	out, err := runCodexScript(t, fakeCodex, "x")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	resultLine := extractResultLine(t, out)
	if strings.Contains(resultLine, "usage_contract_version") {
		t.Errorf("must not fabricate a usage envelope when no usage was captured: %s", resultLine)
	}
	if !strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("still expects a plain success result: %s", resultLine)
	}

	c := accounting.NewCollector(accounting.UsageHint{})
	c.Observe([]byte(resultLine))
	obs := c.Finish(nil)
	if obs.Metered {
		t.Errorf("expected unmetered observation, got: %+v", obs)
	}
}

func TestCodexAsClaude_FailureStillEmitsGatheredUsage(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"partial work"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}`, 7)

	out, err := runCodexScript(t, fakeCodex, "x", "CODEX_MODEL=m")
	if err == nil {
		t.Fatalf("expected non-zero exit, got success. output:\n%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 7 {
		t.Errorf("expected exit code 7, got %v", err)
	}
	if !strings.Contains(out, `"type":"assistant"`) {
		t.Errorf("expected an assistant envelope even on failure, got:\n%s", out)
	}
	resultLine := extractResultLine(t, out)
	if strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("failure path must not report subtype:success: %s", resultLine)
	}
	if !strings.Contains(resultLine, `"usage_contract_version":1`) {
		t.Errorf("expected gathered usage to survive the failure path: %s", resultLine)
	}

	c := accounting.NewCollector(accounting.UsageHint{})
	c.Observe([]byte(resultLine))
	obs := c.Finish(exitErr)
	if !obs.Metered {
		t.Errorf("usage gathered before a graceful failure must still be metered: %+v", obs)
	}
}

func TestCodexAsClaude_VerifyFailureDumpsRawOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	const rawMarker = "VERIFY_RAW_DETAIL_MARKER_7Q"
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"`+rawMarker+`"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 1)

	out, err := runCodexScript(t, fakeCodex, "verify this", "CODEX_VERIFY=1")
	if err == nil {
		t.Fatalf("expected non-zero exit, got success. output:\n%s", out)
	}
	if !strings.Contains(out, "codex raw output") {
		t.Errorf("verify failure must dump the raw codex output marker:\n%s", out)
	}
	if !strings.Contains(out, rawMarker) {
		t.Errorf("verify failure must include the actual raw codex line text:\n%s", out)
	}
}

func TestCodexAsClaude_NonVerifyFailureDumpsRawOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	const rawMarker = "NONVERIFY_RAW_DETAIL_MARKER_4K"
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"`+rawMarker+`"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 1)

	out, err := runCodexScript(t, fakeCodex, "do work")
	if err == nil {
		t.Fatalf("expected non-zero exit, got success. output:\n%s", out)
	}
	if !strings.Contains(out, "codex raw output") {
		t.Errorf("normal-mode failure must dump the raw codex output marker:\n%s", out)
	}
	if !strings.Contains(out, rawMarker) {
		t.Errorf("normal-mode failure must include the actual raw codex line text:\n%s", out)
	}
}

func TestCodexAsClaude_SuccessOmitsRawOutputDump(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"all good"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)

	out, err := runCodexScript(t, fakeCodex, "do work")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "codex raw output") {
		t.Errorf("success path must not dump the raw codex output:\n%s", out)
	}
}

func TestCodexAsClaude_NonVerify_StreamsPerItemAndToolRows(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	// Two narrations around one shell command, then usage.
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"first thought"}}
{"type":"item.started","item":{"type":"command_execution","command":"echo hi","status":"in_progress"}}
{"type":"item.completed","item":{"type":"command_execution","command":"echo hi","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"type":"agent_message","text":"second thought"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}`, 0)

	out, err := runCodexScript(t, fakeCodex, "do work")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	// Collect assistant lines in order.
	var assistantLines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, `"type":"assistant"`) {
			assistantLines = append(assistantLines, strings.TrimSpace(l))
		}
	}
	if len(assistantLines) != 3 {
		t.Fatalf("want 3 assistant lines (2 text + 1 Bash tool_use), got %d:\n%s", len(assistantLines), out)
	}
	if !strings.Contains(assistantLines[0], "first thought") {
		t.Errorf("line 0 should carry the first narration: %s", assistantLines[0])
	}
	if !strings.Contains(assistantLines[1], `"type":"tool_use"`) ||
		!strings.Contains(assistantLines[1], `"name":"Bash"`) ||
		!strings.Contains(assistantLines[1], "echo hi") {
		t.Errorf("line 1 should be a Bash tool_use for the command: %s", assistantLines[1])
	}
	if !strings.Contains(assistantLines[2], "second thought") {
		t.Errorf("line 2 should carry the second narration: %s", assistantLines[2])
	}

	// The terminal usage result line is still present and well-formed.
	rl := extractResultLine(t, out)
	if !strings.Contains(rl, `"channel":"codex"`) || !strings.Contains(rl, `"input_tokens":5`) {
		t.Errorf("result line must still carry the usage envelope: %s", rl)
	}
}

func TestCodexAsClaude_NonVerify_NullUsageLastTurnStillEmitsResult(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"answer"}}
{"type":"item.completed","item":{"type":"agent_message","text":"more"}}
{"type":"turn.completed","usage":null}`, 0)
	out, err := runCodexScript(t, fakeCodex, "go")
	if err != nil {
		t.Fatalf("script must exit 0 even when the last turn.completed carries null usage: %v\n%s", err, out)
	}
	rl := extractResultLine(t, out)
	if strings.Contains(rl, "usage_contract_version") {
		t.Errorf("null usage must not fabricate an envelope: %s", rl)
	}
	if !strings.Contains(rl, `"subtype":"success"`) {
		t.Errorf("still expects a plain success result: %s", rl)
	}
}

// writeSlowFakeCodex writes a fake codex that prints firstLine, flushes, sleeps
// sleepSecs, then prints restLines, then exits 0. Used to prove live streaming.
func writeSlowFakeCodex(t *testing.T, firstLine, restLines string, sleepSecs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "slow-fake-codex")
	content := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' " + strconv.Quote(firstLine) + "\n" +
		"sleep " + strconv.Itoa(sleepSecs) + "\n" +
		"printf '%s\\n' " + strconv.Quote(restLines) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write slow fake codex: %v", err)
	}
	return path
}

// TestCodexAsClaude_NonVerify_StreamsLive proves the first translated assistant
// line is emitted BEFORE codex exits (i.e. output is not buffered until the end).
func TestCodexAsClaude_NonVerify_StreamsLive(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	slow := writeSlowFakeCodex(t,
		`{"type":"item.completed","item":{"type":"agent_message","text":"early thought"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		3)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(codexBaseEnv(), "CODEX_BIN="+slow, "HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader("go")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read the first line; it must arrive well before the 3s sleep elapses.
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		r := bufio.NewReader(stdout)
		l, e := r.ReadString('\n')
		ch <- res{l, e}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("reading first line: %v", got.err)
		}
		if !strings.Contains(got.line, "early thought") {
			t.Fatalf("first streamed line should carry the early narration, got: %q", got.line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no output within 2s — adapter is buffering instead of streaming live")
	}
	_ = cmd.Wait()
}

// TestCodexAsClaude_NonVerify_ReasoningBecomesText: reasoning items surface as
// agent prose.
func TestCodexAsClaude_NonVerify_ReasoningBecomesText(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"reasoning","text":"weighing options"}}
{"type":"item.completed","item":{"type":"agent_message","text":"answer"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	out, err := runCodexScript(t, fakeCodex, "go")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "weighing options") {
		t.Errorf("reasoning text must reach the feed: %s", out)
	}
}

func TestCodexAsClaude_NonVerify_VerboseEmitsCommandOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"command_execution","command":"emit-verbose-marker","aggregated_output":"VERBOSE_OUTPUT_MARKER_9Z\n","exit_code":0,"status":"completed"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	out, err := runCodexScript(t, fakeCodex, "go", "CODEX_VERBOSE=1")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("verbose still emits the Bash tool row: %s", out)
	}
	if !strings.Contains(out, "VERBOSE_OUTPUT_MARKER_9Z") {
		t.Errorf("verbose must also emit the command output: %s", out)
	}
}

func TestCodexAsClaude_NonVerify_WhitespaceAndSummaryArray(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	// Raw string literals keep \t and \n as the two-character JSON escapes — the stream itself must contain
	// tab/newline ESCAPES, which decode to whitespace-only item text inside jq.
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"   "}}
{"type":"item.completed","item":{"type":"reasoning","text":"\t\n"}}
{"type":"item.completed","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"first summary line"},{"type":"summary_text","text":"second line"}]}}
{"type":"item.completed","item":{"type":"agent_message","text":"real answer"}}
{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}`, 0)

	out, err := runCodexScript(t, fakeCodex, "go")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	var assistantLines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, `"type":"assistant"`) {
			assistantLines = append(assistantLines, strings.TrimSpace(l))
		}
	}
	if len(assistantLines) != 2 {
		t.Fatalf("want exactly 2 assistant lines (joined summary + real answer; no Bash rows in this stream), got %d:\n%s", len(assistantLines), out)
	}
	for _, l := range assistantLines {
		if !strings.Contains(l, `"type":"text"`) {
			t.Errorf("every assistant line here must be a text narration, got: %s", l)
		}
	}

	var summaryLine, answerLine string
	for _, l := range assistantLines {
		switch {
		case strings.Contains(l, "first summary line"):
			summaryLine = l
		case strings.Contains(l, "real answer"):
			answerLine = l
		default:
			// Лишняя строка отсекается проверкой len(assistantLines) выше.
		}
	}
	if summaryLine == "" {
		t.Fatalf("no assistant line carries the summary-array reasoning text:\n%s", out)
	}
	// jq -c encodes the newline inside the joined text as the two characters \n.
	if !strings.Contains(summaryLine, "first summary line\\nsecond line") {
		t.Errorf("summary array must join its entries with a newline into ONE line: %s", summaryLine)
	}
	if answerLine == "" {
		t.Fatalf("no assistant line carries the real answer:\n%s", out)
	}

	// Whitespace-only narrations must not survive as standalone bubbles (jq -c
	// keeps spaces literal and encodes tab/newline as \t/\n escapes).
	if strings.Count(out, `"text":"   `) != 0 {
		t.Errorf("whitespace-only agent_message must not emit a bubble:\n%s", out)
	}
	if strings.Contains(out, `"text":"\t\n`) {
		t.Errorf("whitespace-only reasoning must not emit a bubble:\n%s", out)
	}

	rl := extractResultLine(t, out)
	if !strings.Contains(rl, `"input_tokens":3`) {
		t.Errorf("result line must still carry the usage envelope: %s", rl)
	}
}

// TestCodexAsClaude_NonVerify_PropagatesExitCode: a non-zero codex exit
// propagates even though items streamed first.
func TestCodexAsClaude_NonVerify_PropagatesExitCode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}`, 3)
	out, err := runCodexScript(t, fakeCodex, "go")
	if err == nil {
		t.Fatalf("expected non-zero exit, got success:\n%s", out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "partial") {
		t.Errorf("streamed items must still appear before failure: %s", out)
	}
	if !strings.Contains(out, `"subtype":"error_during_execution"`) {
		t.Errorf("failure must still emit the error result line: %s", out)
	}
}

const codexLimitMessage = "You've hit your usage limit. Try again at 3:45 PM."

// codexFailureFixture — реальная форма отказа Codex: error и затем terminal turn.failed с тем же сообщением.
var codexFailureFixture = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"error","message":"` + codexLimitMessage + `"}
{"type":"turn.failed","error":{"message":"` + codexLimitMessage + `"}}`

// countResultLines возвращает все result-строки вывода wrapper'а.
func countResultLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, `"type":"result"`) {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return lines
}

// TestCodexAsClaude_TurnFailedBecomesErrorResult: terminal turn.failed превращается ровно в одну
// Claude-совместимую result-строку is_error:true с исходным сообщением, статус выхода сохраняется,
// а executor распознаёт в ней usage-limit.
func TestCodexAsClaude_TurnFailedBecomesErrorResult(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	for _, mode := range []struct {
		name string
		env  []string
	}{
		{"stream", nil},
		{"verify", []string{"CODEX_VERIFY=1"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			fakeCodex := writeFakeCodex(t, codexFailureFixture, 1)
			out, err := runCodexScript(t, fakeCodex, "go", mode.env...)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit code 1, got %v\n%s", err, out)
			}
			results := countResultLines(out)
			if len(results) != 1 {
				t.Fatalf("want exactly one result line, got %d:\n%s", len(results), out)
			}
			var env struct {
				Subtype string `json:"subtype"`
				IsError bool   `json:"is_error"`
				Result  string `json:"result"`
			}
			if jsonErr := json.Unmarshal([]byte(results[0]), &env); jsonErr != nil {
				t.Fatalf("result line is not valid JSON: %v\n%s", jsonErr, results[0])
			}
			if !env.IsError || env.Result != codexLimitMessage || env.Subtype != "error_during_execution" {
				t.Errorf("result = %+v, want is_error with the original message", env)
			}
			limit, ok := parseUsageLimitText(env.Result, time.Now(), time.UTC)
			if !ok || limit.ResetAt.IsZero() {
				t.Errorf("executor must recognize the limit with a reset time in %q", env.Result)
			}
		})
	}
}

// TestCodexAsClaude_RecoveredErrorIsNotTerminal: промежуточный error, за которым следует успешный turn, —
// не отказ: результат остаётся success без is_error.
func TestCodexAsClaude_RecoveredErrorIsNotTerminal(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	fixture := `{"type":"error","message":"Reconnecting... 1/5"}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":1}}`
	for _, env := range [][]string{nil, {"CODEX_VERIFY=1"}} {
		out, err := runCodexScript(t, writeFakeCodex(t, fixture, 0), "go", env...)
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		rl := extractResultLine(t, out)
		if strings.Contains(rl, "is_error") || !strings.Contains(rl, `"subtype":"success"`) {
			t.Errorf("recovered error must stay a plain success: %s", rl)
		}
	}
}
