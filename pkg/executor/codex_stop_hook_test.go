package executor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeRecordingFakeCodex is a fake codex that records its argv (one per line) and the hook-relevant environment,
// then prints jsonl. Returns the fake and the files it writes.
func writeRecordingFakeCodex(t *testing.T, jsonl string) (fake, argsFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, envFile = filepath.Join(dir, "args.txt"), filepath.Join(dir, "env.txt")
	fake = filepath.Join(dir, "fake-codex")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" +
		"printf 'CODEX_HOME=%s\\nDESCRIPTOR=%s\\n' \"${CODEX_HOME:-}\" \"${" + StopHookDescriptorEnv + ":-}\" > " + envFile + "\n" +
		"cat <<'FAKECODEXJSON'\n" + jsonl + "\nFAKECODEXJSON\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake, argsFile, envFile
}

const (
	stopHookTOML  = `[{hooks=[{type="command",command="'/afm' stop-hook",timeout=30}]}]`
	stopStateTOML = `{"/<session-flags>/config.toml:stop:0:0"={trusted_hash="sha256:abc"}}`
	codexOneTurn  = `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}
{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":1}}`
)

func requireShimTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// The shim forwards the hook and its trust to the real `codex exec` as session overrides, next to the options
// it already passes. It never bypasses Codex's trust policy, never moves CODEX_HOME, and the hook command
// inherits the descriptor path from codex's own environment.
func TestCodexAsClaude_PassesTheStopHookAsSessionOverrides(t *testing.T) {
	requireShimTools(t)
	fake, argsFile, envFile := writeRecordingFakeCodex(t, codexOneTurn)
	out, err := runCodexScript(t, fake, "go",
		StopHookCodexHooksEnv+"="+stopHookTOML, StopHookCodexStateEnv+"="+stopStateTOML,
		StopHookDescriptorEnv+"=/run/afm/descriptor.json", "CODEX_HOME=/user/codex-home", "CODEX_REASONING=high")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	args := fileLines(t, argsFile)
	// Everything else codex was started with stays: the hook adds exactly its two overrides.
	plainFake, plainArgsFile, _ := writeRecordingFakeCodex(t, codexOneTurn)
	if out, err := runCodexScript(t, plainFake, "go", "CODEX_REASONING=high"); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	plain := fileLines(t, plainArgsFile)
	if len(args) != len(plain)+4 || !slices.Equal(args[:len(plain)], plain) {
		t.Fatalf("args = %q, want %q followed by the two -c overrides", args, plain)
	}
	for _, want := range [][2]string{{"-c", "hooks.Stop=" + stopHookTOML}, {"-c", "hooks.state=" + stopStateTOML}, {"-c", "model_reasoning_effort=high"}} {
		if i := slices.Index(args, want[1]); i < 1 || args[i-1] != want[0] {
			t.Fatalf("args = %q, want %s %s", args, want[0], want[1])
		}
	}
	for _, a := range args {
		if strings.Contains(a, "bypass-hook-trust") || strings.Contains(a, "ignore-user-config") {
			t.Fatalf("args = %q: the shim must keep Codex's own trust policy and user config", args)
		}
	}
	if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("args = %q: the existing sandbox policy must stay", args)
	}
	env := string(readFileOrFail(t, envFile))
	if dumpEnvValue(env, "CODEX_HOME") != "/user/codex-home" || dumpEnvValue(env, "DESCRIPTOR") != "/run/afm/descriptor.json" {
		t.Fatalf("codex saw env %q, want the caller's CODEX_HOME and the descriptor", env)
	}
}

// Without the full pair (a hook is useless, and a trust without its hook is wrong) and in verify mode, the shim
// adds nothing: the argument list is the one it always built.
func TestCodexAsClaude_StopHookOverridesNeedTheWholePairAndAnAuthorRun(t *testing.T) {
	requireShimTools(t)
	baseline := func(extra ...string) []string {
		fake, argsFile, _ := writeRecordingFakeCodex(t, codexOneTurn)
		if out, err := runCodexScript(t, fake, "go", extra...); err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		return fileLines(t, argsFile)
	}
	want := baseline()
	for name, env := range map[string][]string{
		"only the hook":  {StopHookCodexHooksEnv + "=" + stopHookTOML},
		"only the trust": {StopHookCodexStateEnv + "=" + stopStateTOML},
		"verify mode":    {StopHookCodexHooksEnv + "=" + stopHookTOML, StopHookCodexStateEnv + "=" + stopStateTOML, "CODEX_VERIFY=1"},
	} {
		t.Run(name, func(t *testing.T) {
			got := baseline(env...)
			if name == "verify mode" {
				for _, a := range got {
					if strings.HasPrefix(a, "hooks.") {
						t.Fatalf("args = %q: verify mode must never get the hook", got)
					}
				}
				return
			}
			if !slices.Equal(got, want) {
				t.Fatalf("args = %q, want the unchanged %q", got, want)
			}
		})
	}
}

// A Stop hook that keeps codex going can produce more than one turn in one process. Codex reports usage as the
// running total of the session, so the last turn.completed is the whole cost; the shim must stream every turn and
// end with a single result that is neither the first turn's nor a sum of the totals.
func TestCodexAsClaude_ContinuationTurnsEndInOneResultWithTheLastTotal(t *testing.T) {
	requireShimTools(t)
	fixture := `{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"first answer"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":1}}
{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"second answer"}}
{"type":"turn.completed","usage":{"input_tokens":260,"cached_input_tokens":20,"output_tokens":12,"reasoning_output_tokens":3}}`
	out, err := runCodexScript(t, writeFakeCodex(t, fixture, 0), "go")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	first, second := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "first answer") }),
		slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "second answer") })
	results := countResultLines(out)
	if first < 0 || second < first || len(results) != 1 || lines[len(lines)-1] != results[0] {
		t.Fatalf("want both answers streamed in order and one final result, got:\n%s", out)
	}
	var result struct {
		Subtype string          `json:"subtype"`
		IsError bool            `json:"is_error"`
		Usage   shimResultUsage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(results[0]), &result); err != nil {
		t.Fatal(err)
	}
	if result.Subtype != "success" || result.IsError || result.Usage.Input != 260 || result.Usage.Output != 12 {
		t.Fatalf("result = %+v, want success with the last turn's totals (260/12)", result)
	}
}

// shimResultUsage is the token totals of the shim's final result event.
type shimResultUsage struct {
	Input  float64 `json:"input_tokens"`
	Output float64 `json:"output_tokens"`
}

// A continuation turn that fails after an earlier completed turn is the run's outcome: the failure is not hidden
// by the first turn's success.
func TestCodexAsClaude_FailedContinuationTurnIsTheOutcome(t *testing.T) {
	requireShimTools(t)
	fixture := `{"type":"item.completed","item":{"type":"agent_message","text":"first answer"}}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":5}}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"continuation failed"}}`
	out, err := runCodexScript(t, writeFakeCodex(t, fixture, 1), "go")
	var ee *exec.ExitError
	if err == nil || !asExitError(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("want exit code 1, got %v\n%s", err, out)
	}
	results := countResultLines(out)
	if len(results) != 1 || !strings.Contains(results[0], `"is_error":true`) || !strings.Contains(results[0], "continuation failed") {
		t.Fatalf("want one error result with the failure, got:\n%s", out)
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	*target = ee
	return ok
}
