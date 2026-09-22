package executor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

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

// writeFakeCodex writes a fake codex binary (used via CODEX_BIN, bypassing
// PATH entirely) that prints jsonlOutput verbatim to stdout and exits with
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

// runCodexScript runs the real scripts/codex-as-claude.sh with CODEX_BIN
// pointed at fakeCodexPath, isolating HOME (and hence ~/.codex/config.toml) to
// an empty temp dir unless the test overrides it via extraEnv, so tests never
// depend on the developer machine's real codex config.
func runCodexScript(t *testing.T, fakeCodexPath, prompt string, extraEnv ...string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("bash", codexScriptPath(t))
	isolatedHome := t.TempDir()
	env := append(os.Environ(),
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

// TestCodexAsClaude_UsageEnvelopeMatchesGoldenShape feeds the exact
// turn.completed.usage documented in docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md
// (and mirrored in pkg/accounting/testdata/codex-gpt-5.6-sol.jsonl) through the
// REAL script and asserts the emitted synthetic result carries the same
// field names AND that pkg/accounting's own Collector normalizes it into the
// same tokens as the canonical fixture — the shell↔Go contract this task
// exists to keep in sync.
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

// TestCodexAsClaude_NoSecretOrPromptLeakage: the prompt text and a
// secret-shaped env var must never appear anywhere in the emitted envelope —
// the usage block is numbers-only by contract.
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
	cmd.Env = append(os.Environ(),
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

// TestCodexAsClaude_ModelFallsBackToTopLevelTomlOnly: with no CODEX_MODEL, the
// script must read only the TOP-LEVEL "model" key of config.toml — a model key
// nested under a later [table] header (e.g. a profile) must not be picked up.
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

	// runCodexScript sets its own isolated HOME internally; this test needs a
	// specific HOME (holding the fixture config.toml above), so it builds the
	// command directly instead of using the shared helper.
	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(os.Environ(), "CODEX_BIN="+fakeCodex, "HOME="+home)
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

// TestCodexAsClaude_NoUsageCapturedOmitsContractFields: when codex never emits
// a turn.completed (e.g. killed before finishing), the result line must stay
// the bare pre-existing shape — no fabricated usage_contract_version/usage —
// so Collector.Finish degrades to an honest unmetered observation.
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

// TestCodexAsClaude_FailureStillEmitsGatheredUsage: codex exits non-zero AFTER
// reporting a turn.completed usage (a graceful, non-killed failure) — the
// script must still forward that usage (with a non-success subtype) and exit
// with codex's own status so afm still fails the stage.
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

// TestCodexAsClaude_NonVerify_StreamsPerItemAndToolRows asserts that, without
// CODEX_VERIFY, each codex item.completed is emitted as its OWN assistant line
// (not aggregated into one; a command_execution may emit one or more lines), and
// command_execution becomes a Bash tool_use row — matching how claude/openai-agent
// appear in the afm feed. (Ordering/shape only; liveness is a separate test.)
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
