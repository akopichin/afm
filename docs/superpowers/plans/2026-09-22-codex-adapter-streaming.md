# Codex Adapter Live Streaming + Tool Rows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make afm's bundled `scripts/codex-as-claude.sh` stream codex events live and surface tool actions (Bash/Edit) as feed action-rows, matching how `claude` and `openai-agent-as-claude.sh` already appear in the dashboard feed.

**Architecture:** Split the adapter into two paths keyed on `CODEX_VERIFY`. The **non-verify** path stops aggregating; it translates each codex `item.completed` event into its own Claude `assistant` line the moment it arrives (per-line `jq`, live), while teeing the raw codex JSONL to a temp file used afterwards only for usage-accounting and exit-code capture. The **verify** path is left byte-for-byte as today (single aggregated final `assistant` text), because afm strictly JSON-decodes the verify agent's text buffer and any streamed narration would corrupt it.

**Tech Stack:** Bash 3.2-compatible shell, `jq`, Go test harness (`pkg/executor/codex_translator_test.go`) that runs the real script against a fake `CODEX_BIN`.

**Spec:** none — this is a scoped enhancement to an existing adapter. Behavioral contract is defined by this plan + the existing tests in `pkg/executor/codex_translator_test.go` and `pkg/executor/codex_verify_test.go`.

## Global Constraints

- **Do NOT change `go.mod` Go version.** (User global rule.)
- **`CODEX_VERIFY=1` behavior stays behaviorally identical.** afm's `RunVerifyAgent` collects the agent's text into a buffer and calls `DecodeModelResult`, which rejects code fences, trailing content, and any non-JSON prose. Streaming intermediate `agent_message`/tool rows into that buffer would break verification. All four tests in `pkg/executor/codex_verify_test.go` MUST stay green. (The verify branch is a straight copy of today's logic — behaviorally equivalent, not necessarily character-for-character.)
- **Usage-accounting contract unchanged.** The terminal `result` line MUST still carry `usage_contract_version:1, usage_schema:"openai", channel:"codex"` and the `model` key exactly as `build_result_line` emits today (`TestCodexAsClaude_UsageEnvelopeMatchesGoldenShape`, `..._NoUsageCapturedOmitsContractFields`, `..._FailureStillEmitsGatheredUsage`).
- **Exit-code semantics unchanged.** A non-zero codex exit MUST make the adapter exit with codex's own code so afm fails the stage (`TestCodexAsClaude_FailureStillEmitsGatheredUsage`).
- **Model resolution unchanged.** `resolve_codex_model` (env → top-level `config.toml` → empty) is untouched (`..._ModelPrecedence_EnvOverridesToml`, `..._ModelFallsBackToTopLevelTomlOnly`).
- **No prompt/secret leakage.** The adapter streams codex *output*, never the prompt or env (`TestCodexAsClaude_NoSecretOrPromptLeakage`).
- **Bash 3.2 compatible** (macOS ships 3.2; CI/Docker are newer). Use `${PIPESTATUS[@]}`, `[[ ]]`, no bash-4 features.
- **Keep `set -euo pipefail`** at the top; suspend `set -e` only around the codex invocation exactly as today.

---

## File Structure

- `scripts/codex-as-claude.sh` — the adapter. Only the *execution + emission* section (current lines ~99–219) is restructured. The header/arg-parsing/`supports_output_last_message`/`CODEX_VERBOSE` validation/`resolve_codex_model`/`build_result_line` blocks are unchanged.
- `pkg/executor/codex_translator_test.go` — add streaming/tool-row tests using the existing `writeFakeCodex` + `runCodexScript` helpers.
- `CHANGELOG.md` (if present at repo root) — one conventional-commit line in Russian (repo convention).

## Reference: the translation `jq` program (shared shape)

The non-verify path emits, per codex `item.completed`, **one or more** Claude lines:

| codex `item.type`                         | emitted Claude event(s)                                                     | feed rendering        |
|-------------------------------------------|-----------------------------------------------------------------------------|-----------------------|
| `agent_message` (nonempty)                | `assistant` with `text` = `.item.text`                                      | agent prose           |
| `reasoning` (nonempty)                    | `assistant` with `text` = `.item.text // .item.summary`                     | agent prose           |
| `command_execution`                       | `assistant` with `tool_use` name `Bash`, `input.command` = `.item.command`  | compact command row   |
| `command_execution` (+ `CODEX_VERBOSE=1`) | the Bash row **plus** a second `assistant` `text` line = `.item.aggregated_output` | command row + prose |
| anything else                             | nothing                                                                     | —                     |

Notes:
- Only `item.completed` is translated (never `item.started`, which repeats `command_execution` with `status:"in_progress"` — translating it would double every command row).
- `turn.completed` is NOT translated inline; its `.usage` is read from the tee'd temp file afterwards.
- **No `file_change`/`patch_apply` mapping.** codex (as of CLI 0.144.x/0.155.x) performs file edits *through* `command_execution` (bash redirects / `apply_patch`), so edits already surface as Bash rows. A dedicated file-change item type was NOT observed in testing, and its real path field is unconfirmed (candidates seen in review: `.item.path` vs `.item.changes[]`). Mapping it now would be guessing a schema — deferred until a real `file_change` event can be captured and asserted against.
- Empty `agent_message`/`reasoning` text emits nothing (both guarded), so codex never produces a blank feed bubble.

---

## Task 1: Failing test — non-verify streams per-item assistant + Bash tool_use

**Files:**
- Test: `pkg/executor/codex_translator_test.go` (add `TestCodexAsClaude_NonVerify_StreamsPerItemAndToolRows`)

**Interfaces:**
- Consumes existing helpers: `writeFakeCodex(t, jsonlOutput string, exitCode int) string`, `runCodexScript(t, fakeCodexPath, prompt string, extraEnv ...string) (stdout string, err error)`.
- Produces: a test asserting the emitted stream contains, in order, two separate `assistant` text lines and one `tool_use` Bash line — locking the streaming contract for later tasks.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/executor/ -run TestCodexAsClaude_NonVerify_StreamsPerItemAndToolRows -v`
Expected: FAIL — current adapter emits ONE aggregated assistant line and no `tool_use` (so `len(assistantLines)` is 1, not 3).

- [ ] **Step 3: Commit the failing test**

```bash
git add pkg/executor/codex_translator_test.go
git commit -m "test(codex): требуем live-стриминг item'ов и Bash tool_use в не-verify режиме"
```

---

## Task 2: Restructure the adapter — stream non-verify, keep verify aggregating

**Files:**
- Modify: `scripts/codex-as-claude.sh` (execution/emission section, current lines ~99–219)

**Interfaces:**
- Consumes: `$CODEX_VERIFY`, `$CODEX_VERBOSE`, `$CODEX_BIN`, `codex_args` array, `$last_msg_file`, functions `resolve_codex_model`/`build_result_line` (all already defined above the changed region).
- Produces: on stdout — a live sequence of `assistant` lines (non-verify) or one aggregated `assistant` line (verify), followed by exactly one `result` line. Exit code = codex's own.

- [ ] **Step 1: Replace the execution/emission section**

Replace everything from the `# run codex with JSON output ...` comment block (current line ~99) through the end of the file with:

```bash
# run codex with JSON output.
#
# Two modes:
#   * CODEX_VERIFY=1 — aggregation path (UNCHANGED): codex runs to completion,
#     all agent_message text is concatenated into ONE assistant envelope (or the
#     exact --output-last-message final answer), because afm strictly JSON-decodes
#     the verify agent's text buffer (DecodeModelResult) — streamed narration or
#     tool rows would corrupt it.
#   * otherwise — streaming path: each codex item.completed is translated to its
#     OWN Claude assistant line the moment it arrives, so the dashboard feed shows
#     codex's thoughts and actions live (agent_message/reasoning -> text,
#     command_execution -> Bash tool_use). afm's executor.parseStreamEvent accepts
#     only type=="assistant" lines. (codex does file edits via command_execution,
#     so they already surface as Bash rows — no separate file_change mapping.)
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

# codex's raw JSONL always goes to a temp file so we can (a) capture usage from
# turn.completed after the stream, (b) read the verify final message, and (c) in
# verify mode aggregate the full answer. Streaming mode ALSO tees each line to
# this file while translating live.
out_file=$(mktemp)
trap 'rm -f "$out_file" "$last_msg_file"' EXIT

# XLATE: per-line jq program (streaming path). One assistant/tool_use line per
# codex item.completed. Kept per-line (printf | jq) so a single malformed line
# can't abort the whole stream — same robustness the aggregation loop relies on.
XLATE='
    def asst($t): {type:"assistant",message:{content:[{type:"text",text:$t}]}};
    def tool($n;$inp): {type:"assistant",message:{content:[{type:"tool_use",name:$n,input:$inp}]}};
    if .type == "item.completed" then
        (.item.type) as $it
        | if $it == "agent_message" then
            ((.item.text // "")) as $m
            | if ($m | length) > 0 then asst($m + "\n") else empty end
        elif $it == "reasoning" then
            ((.item.text // .item.summary // "")) as $r
            | if ($r | length) > 0 then asst($r + "\n") else empty end
        elif $it == "command_execution" then
            tool("Bash"; {command: (.item.command // "")}),
            (if $verbose == 1 and ((.item.aggregated_output // "") | length) > 0
             then asst((.item.aggregated_output) + "\n") else empty end)
        else empty
        end
    else empty
    end
'

# extract_usage <file> — sets the global usage_json from the LAST turn.completed
# .usage seen in the raw codex stream (numeric-only, safe to forward verbatim).
usage_json=""
extract_usage() {
    local f="$1" line t u
    while IFS= read -r line; do
        t=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
        if [[ "$t" == "turn.completed" ]]; then
            u=$(printf '%s' "$line" | jq -c '.usage // empty' 2>/dev/null) || continue
            [[ -n "$u" && "$u" != "null" ]] && usage_json="$u"
        fi
    done < "$f"
}

if [[ "$CODEX_VERIFY" == "1" ]]; then
    # --- verify mode: UNCHANGED aggregation path ---
    set +e
    printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" > "$out_file"
    codex_exit=$?
    set -e

    final_text=""
    while IFS= read -r line; do
        ev_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
        case "$ev_type" in
            item.completed)
                item_type=$(printf '%s' "$line" | jq -r '.item.type // empty' 2>/dev/null) || continue
                case "$item_type" in
                    agent_message)
                        text=$(printf '%s' "$line" | jq -r '.item.text // empty' 2>/dev/null) || continue
                        final_text="${final_text}${text}"$'\n'
                        ;;
                    command_execution)
                        if [[ "$CODEX_VERBOSE" == "1" ]]; then
                            cmd=$(printf '%s' "$line" | jq -r '.item.command // empty' 2>/dev/null) || continue
                            out=$(printf '%s' "$line" | jq -r '.item.aggregated_output // empty' 2>/dev/null) || continue
                            final_text="${final_text}\$ ${cmd}"$'\n'"${out}"$'\n'
                        fi
                        ;;
                esac
                ;;
            turn.completed)
                u=$(printf '%s' "$line" | jq -c '.usage // empty' 2>/dev/null) || continue
                [[ -n "$u" && "$u" != "null" ]] && usage_json="$u"
                ;;
        esac
    done < "$out_file"

    if [[ -n "$last_msg_file" && -s "$last_msg_file" ]]; then
        final_text=$(cat "$last_msg_file")
    fi

    resolved_model=$(resolve_codex_model)

    if [[ "$codex_exit" -ne 0 ]]; then
        echo "error: codex exited with status $codex_exit" >&2
        jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
        build_result_line "error_during_execution"
        exit "$codex_exit"
    fi

    jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
    build_result_line "success"
    exit 0
fi

# --- streaming mode (default) ---
# Each raw line is appended to out_file AND translated live. codex is the 2nd
# stage of the pipeline, so its exit status is ${PIPESTATUS[1]} (printf=0,
# codex=1, subshell=2). set -e is suspended so a non-zero codex exit is captured
# rather than aborting the script before we read PIPESTATUS.
set +e
printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" | {
    while IFS= read -r line; do
        printf '%s\n' "$line" >> "$out_file"
        printf '%s' "$line" | jq -c --argjson verbose "$CODEX_VERBOSE" "$XLATE" 2>/dev/null || true
    done
}
codex_exit=${PIPESTATUS[1]}
set -e

extract_usage "$out_file"
resolved_model=$(resolve_codex_model)

if [[ "$codex_exit" -ne 0 ]]; then
    echo "error: codex exited with status $codex_exit" >&2
    # Items already streamed live; just emit the terminal result and propagate.
    build_result_line "error_during_execution"
    exit "$codex_exit"
fi

build_result_line "success"
```

- [ ] **Step 2: Run the new streaming test**

Run: `go test ./pkg/executor/ -run TestCodexAsClaude_NonVerify_StreamsPerItemAndToolRows -v`
Expected: PASS (3 assistant lines: text, Bash tool_use, text; result line with usage).

- [ ] **Step 3: Run the full executor suite (regression gate)**

Run: `go test ./pkg/executor/ -run TestCodexAsClaude -v`
Expected: PASS for ALL — the six translator tests and the four verify tests. If any golden-shape/usage test fails, reconcile against the Global Constraints (the `result` line and `resolve_codex_model` are unchanged; do NOT loosen the constraints to make a test pass).

- [ ] **Step 4: Static shell check**

Run: `bash -n scripts/codex-as-claude.sh && shellcheck scripts/codex-as-claude.sh || true`
Expected: `bash -n` clean. shellcheck warnings acceptable if pre-existing (note any new ones).

- [ ] **Step 5: Commit**

```bash
git add scripts/codex-as-claude.sh
git commit -m "feat(codex): live-стриминг мыслей и Bash/Edit-действий в ленте (verify-режим без изменений)"
```

---

## Task 3: Lock coverage — reasoning, verbose output, exit-code, verify regression

**Files:**
- Test: `pkg/executor/codex_translator_test.go` (add three tests)
- Test: `pkg/executor/codex_verify_test.go` (add one regression guard)

**Interfaces:**
- Consumes the same helpers as Task 1, plus a new `writeSlowFakeCodex` helper.
- Produces guard tests that pin: **liveness** (first line observable before codex exits), reasoning→text, `CODEX_VERBOSE=1`→extra output block, non-zero exit propagation while items still stream, and verify-mode single-aggregated-assistant (no tool_use).

- [ ] **Step 0: Add the slow fake-codex helper + liveness test**

`writeFakeCodex` emits everything at once, so it can only prove ordering. This helper emits one event, sleeps, then emits the rest — letting the test read the first translated line from the adapter's stdout pipe *while the process is still alive*, which is the whole point of streaming.

```go
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
	if _, err := exec.LookPath("bash"); err != nil { t.Skip("bash not available") }
	if _, err := exec.LookPath("jq"); err != nil { t.Skip("jq not available") }

	slow := writeSlowFakeCodex(t,
		`{"type":"item.completed","item":{"type":"agent_message","text":"early thought"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		3)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(os.Environ(), "CODEX_BIN="+slow, "HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader("go")
	stdout, err := cmd.StdoutPipe()
	if err != nil { t.Fatalf("stdout pipe: %v", err) }
	if err := cmd.Start(); err != nil { t.Fatalf("start: %v", err) }

	// Read the first line; it must arrive well before the 3s sleep elapses.
	type res struct{ line string; err error }
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
```

Note: add `"bufio"` and `"time"` to the import block if not present.

Run: `go test ./pkg/executor/ -run TestCodexAsClaude_NonVerify_StreamsLive -v`
Expected: PASS (first line within 2s, sleep is 3s).

- [ ] **Step 1: Write the reasoning + verbose + exit-code tests**

```go
// Reasoning items surface as agent prose.
func TestCodexAsClaude_NonVerify_ReasoningBecomesText(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil { t.Skip("bash not available") }
	if _, err := exec.LookPath("jq"); err != nil { t.Skip("jq not available") }
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"reasoning","text":"weighing options"}}
{"type":"item.completed","item":{"type":"agent_message","text":"answer"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	out, err := runCodexScript(t, fakeCodex, "go")
	if err != nil { t.Fatalf("script failed: %v\n%s", err, out) }
	if !strings.Contains(out, "weighing options") {
		t.Errorf("reasoning text must reach the feed: %s", out)
	}
}

// CODEX_VERBOSE=1 additionally emits the command's aggregated output as text.
func TestCodexAsClaude_NonVerify_VerboseEmitsCommandOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil { t.Skip("bash not available") }
	if _, err := exec.LookPath("jq"); err != nil { t.Skip("jq not available") }
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"command_execution","command":"echo hi","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	out, err := runCodexScript(t, fakeCodex, "go", "CODEX_VERBOSE=1")
	if err != nil { t.Fatalf("script failed: %v\n%s", err, out) }
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Errorf("verbose still emits the Bash tool row: %s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("verbose must also emit the command output: %s", out)
	}
}

// A non-zero codex exit propagates even though items streamed first.
func TestCodexAsClaude_NonVerify_PropagatesExitCode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil { t.Skip("bash not available") }
	if _, err := exec.LookPath("jq"); err != nil { t.Skip("jq not available") }
	fakeCodex := writeFakeCodex(t, `{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}`, 3)
	out, err := runCodexScript(t, fakeCodex, "go")
	if err == nil {
		t.Fatalf("expected non-zero exit, got success:\n%s", out)
	}
	var ee *exec.ExitError
	if !errorsAs(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "partial") {
		t.Errorf("streamed items must still appear before failure: %s", out)
	}
	if !strings.Contains(out, `"subtype":"error_during_execution"`) {
		t.Errorf("failure must still emit the error result line: %s", out)
	}
}
```

Note: `errorsAs` is `errors.As` — add `"errors"` to the import block if not present, and replace `errorsAs(err, &ee)` with `errors.As(err, &ee)`.

- [ ] **Step 2: Write the verify-mode regression guard**

```go
// Verify mode must NOT stream per-item and must NOT emit tool_use rows — the
// whole answer is ONE aggregated assistant text (DecodeModelResult needs a clean
// JSON buffer). Guards against a future refactor accidentally streaming verify.
func TestCodexAsClaude_VerifyMode_AggregatesNoToolRows(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil { t.Skip("bash not available") }
	if _, err := exec.LookPath("jq"); err != nil { t.Skip("jq not available") }
	// Help output WITHOUT --output-last-message forces the aggregation fallback,
	// keeping this test independent of that flag's presence.
	fakeCodex := writeFakeCodexWithHelp(t, filepath.Join(t.TempDir(), "argv.txt"),
		"  -s, --sandbox <SANDBOX_MODE>", "",
		`{"type":"item.completed","item":{"type":"command_execution","command":"echo hi","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"pass\"}"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)

	cmd := exec.Command("bash", codexScriptPath(t))
	cmd.Env = append(os.Environ(), "CODEX_BIN="+fakeCodex, "HOME="+t.TempDir(), "CODEX_VERIFY=1")
	cmd.Stdin = strings.NewReader("review")
	out, err := cmd.CombinedOutput()
	if err != nil { t.Fatalf("script failed: %v\n%s", err, out) }

	got := string(out)
	if strings.Contains(got, `"type":"tool_use"`) {
		t.Errorf("verify mode must not emit tool_use rows: %s", got)
	}
	var assistantCount int
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.Contains(l, `"type":"assistant"`) { assistantCount++ }
	}
	if assistantCount != 1 {
		t.Errorf("verify mode must emit exactly ONE aggregated assistant line, got %d:\n%s", assistantCount, got)
	}
}
```

Note: reuse the existing `writeFakeCodexWithHelp` helper from `pkg/executor/codex_verify_test.go` (already defined there — check its exact signature and match it; the two args after `argvFile` are the `--help` stdout and an unused slot per the existing verify tests).

- [ ] **Step 3: Run the new tests**

Run: `go test ./pkg/executor/ -run 'TestCodexAsClaude_NonVerify_ReasoningBecomesText|TestCodexAsClaude_NonVerify_VerboseEmitsCommandOutput|TestCodexAsClaude_NonVerify_PropagatesExitCode|TestCodexAsClaude_VerifyMode_AggregatesNoToolRows' -v`
Expected: PASS all four.

- [ ] **Step 4: Full package + vet + lint**

Run: `go test ./pkg/executor/... && go vet ./pkg/executor/... && golangci-lint run ./pkg/executor/... || true`
Expected: tests + vet clean; lint no NEW findings.

- [ ] **Step 5: Commit**

```bash
git add pkg/executor/codex_translator_test.go pkg/executor/codex_verify_test.go
git commit -m "test(codex): фиксируем reasoning/verbose/exit-code стриминга и verify-регрессию"
```

---

## Task 4: Changelog + adapter header docs

**Files:**
- Modify: `scripts/codex-as-claude.sh` (top-of-file comment block — document the two modes)
- Modify: `CHANGELOG.md` (repo root, if present)

**Interfaces:** none (docs only).

- [ ] **Step 1: Update the adapter's top comment**

In the header comment block of `scripts/codex-as-claude.sh`, add two lines under the existing description documenting that non-verify streams per-item (`agent_message`/`reasoning` → text, `command_execution` → Bash tool_use), while `CODEX_VERIFY=1` aggregates into one final answer for strict JSON decoding.

- [ ] **Step 2: Add a CHANGELOG entry (if `CHANGELOG.md` exists)**

Append under the top/unreleased section, in Russian (repo convention), e.g.:

```
- feat(codex): codex-адаптер стримит мысли и Bash/Edit-действия в ленту дашборда live; verify-режим без изменений
```

If there is no `CHANGELOG.md` at the repo root, skip this step (do not create one).

- [ ] **Step 3: Commit**

```bash
git add scripts/codex-as-claude.sh CHANGELOG.md 2>/dev/null; git add scripts/codex-as-claude.sh
git commit -m "docs(codex): описываем два режима адаптера (streaming vs verify)"
```

---

## Downstream (NOT part of this repo's tasks — note for the operator)

The goga pipelines (`~/work/spider`, `~/work/spider-ui`) currently use a *separate* overridden adapter (`.goga/codex-as-claude.sh`) that already streams — they do NOT consume afm's bundled script. This afm change only ships to consumers using afm's native codex autoShim (`type: codex`) or `command: codex-as-claude`. To roll it out there: release a new afm image tag and bump `AFM_VERSION` in each `.goga/Dockerfile`. No action required for the goga override adapters.

## Self-Review

**1. Coverage vs stated goal:**
- Live streaming of thoughts → Task 2 streaming path + Task 1 (per-item ordering/shape) + Task 3 Step 0 (actual liveness via slow fake). ✓
- Bash tool rows → Task 2 `command_execution`→Bash + Task 1 assertion. ✓
- Reasoning → Task 2 branch (empty-guarded) + Task 3 test. ✓
- Verify unchanged → Task 2 preserves the block + Task 3 regression guard + existing 4 verify tests. ✓
- Usage/exit-code/model contracts → Task 2 reuses `build_result_line`/`resolve_codex_model`/`extract_usage`; Task 3 exit-code test; existing golden tests. ✓
- `file_change` mapping → deliberately OUT of scope (unconfirmed schema; codex edits already appear as Bash rows). Documented in the reference section. ✓

**2. Placeholder scan:** All code steps contain concrete shell/Go. `errorsAs`→`errors.As`, the `writeFakeCodexWithHelp` signature note, and the `bufio`/`time`/`errors` import notes are the only "check/add the existing symbol" pointers — all reference already-existing, readable symbols in the same package (or stdlib), not undefined ones.

**3. Type/name consistency:** `XLATE`, `extract_usage`, `usage_json`, `resolved_model`, `codex_exit`, `build_result_line`, `resolve_codex_model`, `last_msg_file` used consistently between the verify and streaming branches. `${PIPESTATUS[1]}` indexed against the exact 3-stage pipeline (`printf | codex | { ... }`) — confirmed correct (both by review and by a standalone bash test: `PIPESTATUS[0]=printf`, `[1]=codex`, `[2]=brace-group`). The jq `asst`/`tool` defs match the emitted shapes asserted in tests (`"type":"assistant"`, `"type":"tool_use"`, `"name":"Bash"`, `input.command`).

**4. Codex review (2026-09-22) reconciliation:** file_change branch dropped (was a guessed schema); "one line per item" reworded to "one or more"; empty `agent_message` now guarded like `reasoning`; a real liveness test added (Task 3 Step 0); "byte-identical" softened to "behaviorally identical". Review confirmed no BLOCKERs, correct PIPESTATUS, robust per-line jq, verify isolation intact, and no breakage among the existing 10 tests.
