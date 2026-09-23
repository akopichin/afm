# Script/Hook Failure Visibility in the UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a `script:` stage, `script_before`, or `script_after` fails, the user can see WHY in the dashboard — today only stdout streams to the feed, stderr (where Python tracebacks and most error messages go) is written to a disk file and never shown, and a failed `script:` stage surfaces no reason at all.

**Architecture:** Three coordinated additions. (1) **Stream stderr to the feed** the same way stdout already is — `RunScript` tees `cmd.Stderr` through a newline-splitting writer that calls `OnAction("stderr", line)` in addition to the `.stderr.log` file; `execScript` publishes it as `script_output` with a new `stream` field so the UI can render stderr distinctly. (2) **Attach a stderr tail to failure notices** — on any script/hook failure, read the last lines of the stage's `.stderr.log` and include them in the failure notice. (3) **Emit a UI failure notice for a failed `script:` stage** — today `runScriptStage`'s failure branch publishes nothing to the feed (only a lifecycle-hook event + the FSM `EvFail`); add a durable `script_failed` feed notice carrying the exit error + stderr tail. All three are scoped to script execution (`execScript` / `RunScript`); agent stderr stays file-only (it's noisy CLI diagnostics).

**Tech Stack:** Go (`pkg/executor`, `pkg/orchestrator`, `pkg/orchestrator/bus`, `pkg/server`), React + TypeScript (`pkg/web/dashboard`, `feed-view-model.ts`), Go `testing`, Vitest, Chrome/`open` for the live run.

**Spec:** Self-contained. Root-caused 2026-09-23 by code trace + reproduction: `RunScript` (`pkg/executor/executor.go:670`) sends stdout to a per-line callback but `cmd.Stderr` to a plain file writer (`openStderrLog`), so stderr never reaches `OnAction`/the feed; `runScriptStage`'s failure branch (`pkg/orchestrator/agents.go:89-94`) does `Trigger(EvFail)` + a lifecycle emit but no UI notice; `transitionToFeedEvents` (`pkg/server/events_handler.go`) drops the `EvFail` reason. Confirmed against a real goga run: a failing `python3 -m goga.build` would leave its traceback only in `build/script.stderr.log`.

## Global Constraints

- **Do NOT change the Go version in `go.mod`.**
- **CHANGELOG.md is English; commit messages are Russian.** No keepachangelog.com references.
- **Never add `Co-Authored-By`.**
- **Go lint stays green** (`golangci-lint run ./...` — no NEW findings vs the ~21 pre-existing goconst baseline). **Frontend gate = `npm run typecheck` + `npx vitest run`** — the dashboard has **no ESLint** (do not run/expect `npm run lint`).
- **Scope stderr streaming to script execution only** (`execScript`/`RunScript`). `RunAgent`/`RunPlanning`/`RunVerifyAgent` keep stderr file-only (unchanged).
- **stderr must also keep going to `<phase>.stderr.log`** (diagnostic file) — streaming is *in addition*, not instead.
- **Presentation/observability only** — no FSM/status changes. A failed script stage still goes `failed` via the existing `EvFail`; a failed `script_after` still leaves the stage `done`. We only ADD feed notices and stream stderr.
- **Tail bounds:** stderr tail attached to notices is capped at **`stderrTailMaxLines = 20`** lines AND **`stderrTailMaxBytes = 4096`** bytes (whichever is smaller wins; keep the LAST lines). UTF-8 safe (never split a rune).

---

## Root cause (verified 2026-09-23)

- `pkg/executor/executor.go:670` `RunScript`: `cmd.Stderr = <stderr.log file>`; only stdout goes through the per-line `lineCallback` → `OnAction`. stderr never reaches the feed.
- `pkg/orchestrator/agents.go:89-94` `runScriptStage` failure: `emitStageEvent(EventStageScriptFailed=lifecycle, err)` + `Trigger(EvFail, err.Error())` — **no `o.ui.Publish` / `AppendNotice`**, so the feed shows only a bare `→ failed` with no reason.
- `pkg/orchestrator/hooks.go` `runBeforeHook`/`runAfterHook` failure: `publishHookNotice(EventHookFailed, {hook, error})` — `error` is `err.Error()` (`"exit status 1"`), not the stderr content.
- `pkg/web/dashboard/.../feed-view-model.ts`: `script_output` → `[hook] line` (neutral mono); `hook_failed` → `hook-hook failed: error` (danger). No stderr distinction, no `script_failed` case.

---

## File Structure

**Go — executor (`pkg/executor/`)**
- `linewriter.go` — CREATE. `lineWriter` (an `io.Writer` splitting on `\n`, calling a `func(string)` per complete line, with `Flush()` for a trailing partial line) + `ReadStderrTail(logFile string, maxLines int, maxBytes int) string` (reads the last lines of `<logFile-without-.log>.stderr.log`, UTF-8-safe, byte+line capped).
- `linewriter_test.go` — CREATE.
- `executor.go` — MODIFY. In `RunScript`, tee `cmd.Stderr` = `io.MultiWriter(stderrFile, lineWriter→OnAction("stderr", line))`; `Flush()` the lineWriter after `run` returns. Add `StderrLogPath()`-style access if needed (the caller already knows the logFile). No change to `run()`'s signature. `RunAgent`/`RunPlanning`/`RunVerifyAgent` untouched.
- `executor_test.go` — MODIFY. Stderr lines reach `OnAction` with tool=`"stderr"`; trailing partial line flushed; agent path unaffected.

**Go — orchestrator (`pkg/orchestrator/`)**
- `bus/bus.go` — MODIFY. Add `EventScriptFailed EventType = "script_failed"` (durable UI feed notice for a failed `script:` stage — distinct from the lifecycle-only `stage_script_failed`).
- `hooks.go` — MODIFY. `execScript`'s `OnAction` becomes `func(stream, line string)` → publishes `EventScriptOutput` with `data = {hook, line, stream}` (stream ∈ `"stdout"`/`"stderr"`) live + `AppendNotice`. `runBeforeHook`/`runAfterHook`: add `stderr_tail` (from `ReadStderrTail`) to the `EventHookFailed` notice data.
- `agents.go` — MODIFY. `runScriptStage` failure branch: after the existing `emitStageEvent`+`Trigger(EvFail)`, publish `EventScriptFailed` live + `AppendNotice` with `data = {error, stderr_tail}` (stage still goes `failed` — unchanged).
- `stagefiles/notices.go` — MODIFY. Add a package-level `sync.Mutex` around `AppendNotice`'s write (concurrent stdout+stderr appends now happen) + a func-comment note that cross-stream feed ordering is best-effort.
- Tests: `hooks_test.go`, `agents_test.go` / `lifecycle_scripts_test.go` (whichever matches existing patterns) + integration in `integration_hooks_test.go`; `stagefiles/notices_test.go` (concurrent-append safety).

**Go — server (`pkg/server/`)**
- `events_handler.go` — VERIFY (likely no change): `reconstructNotices` is generic over notice types, so `script_failed` + the enriched `script_output`/`hook_failed` replay automatically. Add a test asserting a `script_failed` notice round-trips through `/api/events`.

**Frontend (`pkg/web/dashboard/src/`)**
- `components/feed-workspace/feed-view-model.ts` — MODIFY. `script_output`: render stderr lines distinctly (`stream === 'stderr'` → `tone: 'warning'` + a `[hook:stderr]` marker); new `script_failed` case → `tone: 'danger'`, `script failed: <error>` with the tail; `hook_failed` → append the `stderr_tail` when present.
- `components/feed-workspace/feed-view-model.test.ts` — MODIFY.
- `types/afm-event.ts` — MODIFY. Add `script_failed` to `AFM_EVENT_TYPES`; consider adding it to `SIGNIFICANT_EVENT_TYPES` (a status refresh is already triggered by the `failed` transition, so this is optional — decide in Task 5).

**Docs**
- `docs/script-stages.md`, `CHANGELOG.md`, `pkg/web/dashboard/RELEASE_NOTES.md` — MODIFY (final task).

---

## Task 1: executor — `lineWriter` + `ReadStderrTail`

**Files:**
- Create: `pkg/executor/linewriter.go`
- Test: `pkg/executor/linewriter_test.go`

**Interfaces:**
- Produces: `type lineWriter struct{...}`; `newLineWriter(emit func(string)) *lineWriter` (implements `io.Writer`, buffers until `\n`, calls `emit` per complete line); `(*lineWriter) Flush()` (emits any buffered trailing partial line, once). `ReadStderrTail(logFile string, maxLines, maxBytes int) string` — resolves `<logFile trimmed of .log>.stderr.log`, returns the last ≤`maxLines` lines capped at ≤`maxBytes` bytes (keep the tail; UTF-8-safe cut; `""` if the file is missing/empty).

**Bounds (codex plan review — Important):**
- **`ReadStderrTail` must be memory-bounded**: do NOT `os.ReadFile` the whole file. `os.Stat` for size; if larger than a read window (`maxBytes*4 + 64KiB` headroom for line boundaries), `Seek` to `size-window` and read only the suffix (drop a leading partial line after the seek). Then take the last `maxLines`, then hard-cap total bytes at `maxBytes` — **truncating on a UTF-8 rune boundary even when a single retained line alone exceeds `maxBytes`** (a 1-line 10KB traceback must still cut to 4KB, not overflow). `maxLines<=0` or `maxBytes<=0` → return `""` (defensive; callers always pass the positive package consts).
- **`lineWriter` must cap the in-memory buffer**: a script emitting a huge line with no `\n` must not grow the buffer unbounded. Cap the buffered line at `maxLineBytes = 64 * 1024`; on overflow, emit the truncated line with a trailing ` …[truncated]` marker and reset the buffer (subsequent bytes up to the next `\n` are discarded until the line ends). Document this policy.

- [ ] **Step 1: Write the failing tests**

```go
func TestLineWriter_EmitsPerLine(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte("a\nb\n"))
	lw.Write([]byte("c")) // partial, no newline yet
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("per-line: got %v", got)
	}
	lw.Flush()
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("flush trailing partial: got %v", got)
	}
	lw.Flush() // idempotent: no duplicate "c"
	if len(got) != 3 {
		t.Fatalf("double flush emitted extra: %v", got)
	}
}

func TestLineWriter_SplitAcrossWrites(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte("hel"))
	lw.Write([]byte("lo\nworld\n"))
	if !reflect.DeepEqual(got, []string{"hello", "world"}) {
		t.Fatalf("split-across-writes: got %v", got)
	}
}

func TestReadStderrTail_LastLinesCapped(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	stderr := filepath.Join(dir, "script.stderr.log")
	var lines []string
	for i := 1; i <= 50; i++ { lines = append(lines, fmt.Sprintf("line %d", i)) }
	os.WriteFile(stderr, []byte(strings.Join(lines, "\n")+"\n"), 0644)
	tail := ReadStderrTail(logFile, 20, 4096)
	if strings.Contains(tail, "line 30") || !strings.Contains(tail, "line 50") || !strings.Contains(tail, "line 31") {
		t.Fatalf("expected last 20 lines (31..50), got:\n%s", tail)
	}
}

func TestReadStderrTail_Missing_ReturnsEmpty(t *testing.T) {
	if ReadStderrTail(filepath.Join(t.TempDir(), "nope.log"), 20, 4096) != "" {
		t.Fatal("missing file must return empty")
	}
}

func TestReadStderrTail_SingleHugeLine_CappedToBytes(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(strings.Repeat("x", 10000)+"\n"), 0644)
	tail := ReadStderrTail(logFile, 20, 4096)
	if len(tail) > 4096 { t.Fatalf("byte cap violated: %d bytes", len(tail)) }
	if !utf8.ValidString(tail) { t.Fatal("cut on non-rune boundary") }
}

func TestReadStderrTail_LargeFile_MemoryBounded(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "script.log")
	// 5 MB of lines; must not load it all — just assert it returns the tail lines cheaply.
	var b strings.Builder
	for i := 0; i < 200000; i++ { b.WriteString(fmt.Sprintf("noise %d\n", i)) }
	b.WriteString("FINAL-LINE\n")
	os.WriteFile(filepath.Join(dir, "script.stderr.log"), []byte(b.String()), 0644)
	tail := ReadStderrTail(logFile, 20, 4096)
	if !strings.Contains(tail, "FINAL-LINE") { t.Fatalf("tail missing final line: %q", tail) }
}

func TestLineWriter_HugeLineNoNewline_TruncatesNotUnbounded(t *testing.T) {
	var got []string
	lw := newLineWriter(func(s string) { got = append(got, s) })
	lw.Write([]byte(strings.Repeat("a", 200000))) // no newline, > maxLineBytes
	lw.Write([]byte("\n"))
	if len(got) != 1 { t.Fatalf("expected one (truncated) line, got %d", len(got)) }
	if len(got[0]) > 64*1024+32 { t.Fatalf("buffered line not capped: %d", len(got[0])) }
	if !strings.Contains(got[0], "[truncated]") { t.Fatalf("missing truncation marker: %.40q", got[0]) }
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./pkg/executor/ -run 'LineWriter|ReadStderrTail' -v` → FAIL (undefined).

- [ ] **Step 3: Implement** `linewriter.go`. `lineWriter.Write` appends to an internal `[]byte` buffer, scans for `\n`, emits each complete line (without the `\n`), keeps the remainder; when the buffered remainder exceeds `maxLineBytes` (64KiB), emit `<first 64KiB> …[truncated]` and set a "dropping until next `\n`" flag (discard bytes until the newline, then resume). `Flush` emits the remainder if non-empty and clears it (guard a `flushed`/empty-buffer check so a second `Flush` is a no-op). `ReadStderrTail`: `path := strings.TrimSuffix(logFile, ".log") + ".stderr.log"`; if `maxLines<=0 || maxBytes<=0` return `""`; `os.Stat` → open → if size > `window := maxBytes*4 + 64*1024`, `Seek(size-window)` and read only `window` bytes, then drop everything up to and including the first `\n` (partial leading line); split remaining on `\n`, drop a trailing empty from the final newline, take the last `maxLines`, join with `\n`, then if the joined result exceeds `maxBytes`, cut from the FRONT to `maxBytes` on a UTF-8 rune boundary (`for !utf8.RuneStart(s[i])` walk). Return the result.

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/executor/ -run 'LineWriter|ReadStderrTail' -v` → PASS. Then `golangci-lint run ./pkg/executor/...` (no new findings).

- [ ] **Step 5: Commit**
```bash
git add pkg/executor/linewriter.go pkg/executor/linewriter_test.go
git commit -m "feat(executor): lineWriter + ReadStderrTail для стрима stderr скрипта"
```

---

## Task 2: executor — stream stderr through `RunScript`'s `OnAction`

**Files:**
- Modify: `pkg/executor/executor.go` (`RunScript` only)
- Test: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: `newLineWriter`/`Flush` (Task 1).
- Produces: `RunScript` calls `e.cfg.OnAction("stderr", line)` for each stderr line (in addition to `OnAction("stdout", line)` for stdout), while still writing stderr to `<phase>.stderr.log`.

- [ ] **Step 1: Write the failing test**

```go
func TestRunScript_StreamsStderrToOnAction(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var stdout, stderr []string
	e := New(Config{
		Command: "sh", ExtraArgs: []string{"-c", "echo out; echo err 1>&2"},
		Dir: dir, IdleTimeout: time.Minute,
		OnAction: func(stream, line string) {
			mu.Lock(); defer mu.Unlock()
			if stream == "stdout" { stdout = append(stdout, line) }
			if stream == "stderr" { stderr = append(stderr, line) }
		},
	})
	if err := e.RunScript(context.Background(), time.Minute, filepath.Join(dir, "script.log")); err != nil {
		t.Fatal(err)
	}
	mu.Lock(); defer mu.Unlock()
	if !contains(stdout, "out") { t.Fatalf("stdout not streamed: %v", stdout) }
	if !contains(stderr, "err") { t.Fatalf("stderr not streamed: %v", stderr) }
	// stderr also persisted to the file:
	b, _ := os.ReadFile(filepath.Join(dir, "script.stderr.log"))
	if !strings.Contains(string(b), "err") { t.Fatalf("stderr.log missing content: %q", b) }
}
```

Also add a timeout/interruption test (codex Important): a script that writes a trailing partial stderr line (`printf 'partial-no-newline' 1>&2`) then `sleep`s past a short `ScriptTimeout` → assert the partial stderr line still reaches `OnAction` (Flush after `run` returns on `DeadlineExceeded`), and `RunScript` returns the `script timeout` error.

- [ ] **Step 2: Run to verify failure** — `go test ./pkg/executor/ -run TestRunScript_StreamsStderr -v` → FAIL (stderr not in OnAction).

- [ ] **Step 3: Implement.** In `RunScript`, build the stderr writer so that **streaming survives even if the log file failed to open** (codex Important — the `io.Discard` fallback must NOT defeat the primary feature):
```go
fileSink := io.Writer(io.Discard)
if sf := openStderrLog(logFile); sf != nil {
	fileSink = sf
	defer sf.Close()
}
var lw *lineWriter
stderrWriter := fileSink
if e.cfg.OnAction != nil {
	lw = newLineWriter(func(line string) { e.cfg.OnAction("stderr", line) })
	stderrWriter = io.MultiWriter(fileSink, lw) // stream to feed regardless of file-open success
}
```
Pass `stderrWriter` as the `stderr` arg to `e.run(...)`. After `e.run` returns (before `lg.LogEnd`), call `if lw != nil { lw.Flush() }` so a trailing partial stderr line (no newline) still streams — this also covers the timeout/interruption path (`e.run` returns on `context.DeadlineExceeded`, then Flush emits whatever stderr was buffered). Keep the stdout callback + timeout-error mapping unchanged. (Only the durable `.stderr.log` tail is unavailable when `openStderrLog` fails; the live feed still gets stderr.)

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/executor/... -run TestRunScript -v` → PASS; `golangci-lint run ./pkg/executor/...` clean.

- [ ] **Step 5: Commit**
```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "feat(executor): RunScript стримит stderr в OnAction (stdout+stderr в ленту)"
```

---

## Task 3: orchestrator — `execScript` publishes stdout/stderr with a `stream` field

**Files:**
- Modify: `pkg/orchestrator/hooks.go` (`execScript`)
- Test: `pkg/orchestrator/hooks_test.go` (or `lifecycle_scripts_test.go` — match the file that already exercises `execScript`)

**Interfaces:**
- Consumes: `RunScript`'s `OnAction("stdout"|"stderr", line)` (Task 2).
- Produces: `EventScriptOutput` notices/events with `data = map[string]string{"hook": hook, "line": line, "stream": stream}` (`stream` ∈ `"stdout"`/`"stderr"`). Both live (`o.ui.Publish`) and durable (`AppendNotice`), as today.

- [ ] **Step 1: Write the failing test** — drive `execScript` with a script that writes to both streams (via the injected orchestrator test harness), assert a published/`notices.jsonl` `script_output` entry exists with `stream:"stderr"` and `line:"<err text>"`. (Follow the existing `execScript`/`EventScriptOutput` test in the package; if it uses a fake bus/notice capture, reuse it.)

- [ ] **Step 2: Run to verify failure** — the current `OnAction: func(_, line string)` ignores the stream; the new assertion on `stream` fails.

- [ ] **Step 3: Implement.** Change `execScript`'s `OnAction` closure from `func(_, line string)` to `func(stream, line string)`; build `data := map[string]string{"hook": hook, "line": line, "stream": stream}`; keep the existing `o.ui.Publish(EventScriptOutput, data)` + `stagefiles.AppendNotice(runDir, s.ID, "script_output", data)`. Nothing else changes (Dir/StageDir/timeout plumbing intact). Update the `EventScriptOutput` doc comment in `bus/bus.go` (currently "one line of stdout") to note it now carries `stream` (stdout|stderr).

- [ ] **Step 3b: Serialize `AppendNotice` (codex Important — concurrent stdout+stderr appends).** stdout (StdoutPipe scanner goroutine) and stderr (MultiWriter copy goroutine) now call `AppendNotice` for the same run concurrently. Small O_APPEND writes are atomic on Linux/macOS, but add an explicit guard for robustness + deterministic per-arrival ordering: a package-level `var noticeMu sync.Mutex` in `pkg/orchestrator/stagefiles/notices.go`, locked around the `OpenFile`+`Write`+`Close` in `AppendNotice`. This serializes ALL notice writes (not a hot path — append+close). Document in the func comment that cross-stream (stdout vs stderr) feed ORDERING is best-effort (two OS streams, two reader goroutines). Add a test `TestAppendNotice_ConcurrentWritesNoCorruption` (N goroutines append; re-read `notices.jsonl`; every line is valid JSON and the count matches).

- [ ] **Step 3c: Legacy-field test.** Add/extend a test asserting a `script_output` notice WITHOUT a `stream` field (a pre-upgrade `notices.jsonl` line) still decodes and is handled — the reader must treat absent `stream` as `stdout` (backward compat).

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/orchestrator/ -run 'ExecScript|ScriptOutput' -v` → PASS.

- [ ] **Step 5: Commit**
```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/hooks_test.go
git commit -m "feat(orchestrator): script_output несёт stream (stdout/stderr)"
```

---

## Task 4: orchestrator — `script_failed` UI notice for a failed `script:` stage

**Files:**
- Modify: `pkg/orchestrator/bus/bus.go` (new `EventScriptFailed`)
- Modify: `pkg/orchestrator/agents.go` (`runScriptStage` failure branch)
- Test: `pkg/orchestrator/agents_test.go` or `integration_hooks_test.go`

**Interfaces:**
- Produces: `bus.EventScriptFailed EventType = "script_failed"`. On a failed `script:` stage, a durable feed notice `data = map[string]string{"error": err.Error(), "stderr_tail": <ReadStderrTail(script.log)>}` published live + `AppendNotice`. The stage STILL transitions to `failed` (existing `EvFail` unchanged).

- [ ] **Step 1: Write the failing test**

```go
func TestRunScriptStage_Failure_PublishesScriptFailedNotice(t *testing.T) {
	// harness with a script stage whose script exits 1 and writes "boom" to stderr.
	// After runScriptStage, assert:
	//  - stage status == failed (unchanged behavior)
	//  - notices.jsonl (or captured bus) has a "script_failed" entry
	//    with data.error non-empty AND data.stderr_tail containing "boom".
}
```

- [ ] **Step 2: Run to verify failure** — no `script_failed` notice exists today.

- [ ] **Step 3: Implement.** Add the const in `bus.go` (with a doc comment distinguishing it from the lifecycle-only `stage_script_failed`). In `runScriptStage`'s `if err != nil` branch (`agents.go:89`), BEFORE `return`, after `Trigger(EvFail,…)`:
```go
tail := executor.ReadStderrTail(logFile, stderrTailMaxLines, stderrTailMaxBytes)
data := map[string]string{"error": err.Error(), "stderr_tail": tail}
o.ui.Publish(bus.Event{Type: bus.EventScriptFailed, StageID: s.ID, Data: data})
stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventScriptFailed), data)
```
Define `stderrTailMaxLines = 20`, `stderrTailMaxBytes = 4096` as package consts (orchestrator) — reused by Task 5's hook path. Keep the existing `emitStageEvent(lifecycle)` + `failBlockedStages()`.

**Ordering (codex Important):** the `script_failed` notice is published AFTER `Trigger(EvFail)` (the durable transition is already committed). In the live feed the `script_failed` row may arrive just before/after the `stage_status_changed → failed` row — this is cosmetic (both render; the stage rail updates via the independent `/api/status` poll). Documented as acceptable; we deliberately do NOT thread the reason through the transition fan-out (keeps the FSM path untouched — a Global Constraint).

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/orchestrator/ -run 'ScriptStage.*Fail|ScriptFailed' -v` → PASS; lint clean.

- [ ] **Step 5: Commit**
```bash
git add pkg/orchestrator/bus/bus.go pkg/orchestrator/agents.go pkg/orchestrator/*_test.go
git commit -m "feat(orchestrator): script_failed notice с причиной+stderr для упавшей script-стадии"
```

---

## Task 5: orchestrator — stderr tail in `hook_failed` (script_before/script_after)

**Files:**
- Modify: `pkg/orchestrator/hooks.go` (`runBeforeHook`, `runAfterHook`)
- Test: `pkg/orchestrator/hooks_test.go` / `recovery_hooks_test.go`

**Interfaces:**
- Produces: the `EventHookFailed` notice data gains `"stderr_tail"` (from `ReadStderrTail` on the hook's `before.log`/`after.log`), alongside the existing `"hook"` + `"error"`.

- [ ] **Step 1: Write the failing test** — a `script_before`/`script_after` that exits non-zero and writes to stderr → the published `hook_failed` notice includes `stderr_tail` with that text. (Extend the existing `TestRunAfterHook_FailsThenSkip_StageStaysDone` pattern / the before-hook equivalent.)

- [ ] **Step 2: Run to verify failure** — today `publishHookNotice(EventHookFailed, {"hook", "error"})` has no tail.

- [ ] **Step 3: Implement.** In both hooks' failure path, compute `tail := executor.ReadStderrTail(logFile, stderrTailMaxLines, stderrTailMaxBytes)` (logFile = the hook's `before.log`/`after.log`) and pass `{"hook": …, "error": err.Error(), "stderr_tail": tail}` to `publishHookNotice`. (`publishHookNotice` already forwards arbitrary `map[string]string` data live + to `notices.jsonl`.)

**Retry-tail semantics (codex minor):** hook logs are opened `O_APPEND` and a hook retries 3× into the SAME `before.log`/`after.log`, so the tail is the last N lines ACROSS attempts — i.e. it shows the final (fatal) attempt's stderr, which is exactly what's wanted. This is intentionally cumulative; no per-attempt log segmentation. State it in the func comment.

- [ ] **Step 4: Run to verify pass** — `go test ./pkg/orchestrator/ -run 'Hook.*Fail|AfterHook|BeforeHook' -v` → PASS.

- [ ] **Step 5: Commit**
```bash
git add pkg/orchestrator/hooks.go pkg/orchestrator/*_test.go
git commit -m "feat(orchestrator): stderr-хвост в hook_failed (script_before/after)"
```

---

## Task 6: server — verify new/enriched notices replay through `/api/events`

**Files:**
- Modify (test-only, likely): `pkg/server/events_handler_test.go`

**Interfaces:**
- `reconstructNotices` is generic over notice `type`, so `script_failed` and the `stream`/`stderr_tail`-enriched `script_output`/`hook_failed` reconstruct with no code change. This task PROVES it.

- [ ] **Step 1: Write the failing/covering test** — append a `script_failed` notice (via the same `AppendNotice`-style helper other tests use) + a `script_output` notice with `stream:"stderr"`, GET `/api/events?stage=<id>`, assert both appear with their `data` (`error`/`stderr_tail`/`stream`) intact and `stage_id` correct.

- [ ] **Step 2: Run** — `go test ./pkg/server/ -run 'Events.*Script|ScriptFailed' -v`. If it passes immediately (generic replay), keep it as a regression guard. If a notice-type allowlist drops it, add `script_failed` there and note it.

- [ ] **Step 3: Commit**
```bash
git add pkg/server/events_handler_test.go pkg/server/events_handler.go
git commit -m "test(server): script_failed + stderr script_output реплеятся через /api/events"
```

---

## Task 7: frontend — render stderr, `script_failed`, and hook stderr tail

**Files:**
- Modify: `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts`
- Modify: `pkg/web/dashboard/src/types/afm-event.ts`
- Test: `pkg/web/dashboard/src/components/feed-workspace/feed-view-model.test.ts`

**Interfaces:**
- Consumes the new `data` shapes: `script_output {hook, line, stream}`, `script_failed {error, stderr_tail}`, `hook_failed {hook, error, stderr_tail?}`.

- [ ] **Step 1: Write the failing tests**

```ts
it('renders a stderr script_output line distinctly', () => {
  const item = toFeedItems([ev('script_output', { hook: 'script', line: 'Traceback...', stream: 'stderr' })])[0]
  expect(item.tone).toBe('warning')
  expect(item.text).toContain('stderr')
  expect(item.text).toContain('Traceback...')
})

it('renders a stdout script_output line as before (neutral)', () => {
  const item = toFeedItems([ev('script_output', { hook: 'script', line: 'building', stream: 'stdout' })])[0]
  expect(item.tone).toBe('neutral')
  expect(item.text).toBe('[script] building')
})

it('renders script_failed as a danger row with error + tail', () => {
  const item = toFeedItems([ev('script_failed', { error: 'exit status 1', stderr_tail: 'boom' })])[0]
  expect(item.tone).toBe('danger')
  expect(item.text).toContain('exit status 1')
  expect(item.text).toContain('boom')
})

it('appends stderr_tail to hook_failed when present', () => {
  const item = toFeedItems([ev('hook_failed', { hook: 'after', error: 'exit status 1', stderr_tail: 'boom' })])[0]
  expect(item.text).toContain('exit status 1')
  expect(item.text).toContain('boom')
})
```

- [ ] **Step 2: Run to verify failure** — `cd pkg/web/dashboard && npx vitest run src/components/feed-workspace/feed-view-model` → FAIL.

- [ ] **Step 3: Implement.**
  - `script_output` case: read `stream = str(obj.stream)`. If `stream === 'stderr'` → `{ actor:'agent', tone:'warning', kind:'script', text:`[${str(obj.hook)}:stderr] ${str(obj.line)}`, mono:true }`. Else keep exactly today's `{ tone:'neutral', text:`[${hook}] ${line}` }` (backward-compatible when `stream` is absent — old notices have no `stream`, treat as stdout).
  - New `script_failed` case: render the tail as a **fenced markdown code block** so a multiline traceback keeps its line breaks (plain `\n` collapses in HTML). `const tail = str(obj.stderr_tail); { actor:'system', tone:'danger', kind:'status', markdown:true, mono:false, text: tail ? `**script failed:** ${str(obj.error)}\n\n\`\`\`\n${tail}\n\`\`\`` : `**script failed:** ${str(obj.error)}` }`. (Renders via `renderPlainMarkdown` → markdown-it `html:false`, so the traceback is safe preformatted text, not HTML.)
  - `hook_failed` case: same treatment — when `stderr_tail` present, append it as a fenced code block: `` `${hook}-hook failed: ${error}` `` + (tail ? `\n\n\`\`\`\n${tail}\n\`\`\`` : '') with `markdown:true`. When no tail, keep today's plain danger row.
  - **Codex minor:** the `feed-view-model.test.ts` must test a REAL multiline traceback (e.g. `"Traceback (most recent call last):\n  File …\nValueError: boom"`), asserting the rendered item carries all lines — not just `"boom"`.
  - `types/afm-event.ts`: add `'script_failed'` to `AFM_EVENT_TYPES`. **Decision:** do NOT add it to `SIGNIFICANT_EVENT_TYPES` — the `failed` `stage_status_changed` transition already triggers the `/api/status` refresh, so `script_failed` is purely informational; adding it would double-refresh. (Record this decision in the commit body.)
  - **`script_failed` dedup (codex Important):** leave it OUT of `CONTENT_DEDUPE_ON_INGEST` (consistent with `script_output`) — it's published live AND to `notices.jsonl`, but `mergeCapped`/`mergeHistory` already dedup live-vs-history by the seq-less content `dedupeKey` on initial load/reconnect, and `onmessage` only appends genuinely-new live messages, so no double-row in practice. Adding it to the ingest allowlist would wrongly collapse two DISTINCT failures with identical `{error, stderr_tail}` (e.g. a manual retry that fails the same way). Add a test in `use-event-feed.test.ts`: a `script_failed` present in BOTH history and live resolves to ONE row via `mergeCapped`.

- [ ] **Step 4: Run to verify pass** — `npx vitest run src/components/feed-workspace/feed-view-model && npm run typecheck` → PASS.

- [ ] **Step 5: Commit**
```bash
git add pkg/web/dashboard/src/components/feed-workspace/feed-view-model.ts pkg/web/dashboard/src/types/afm-event.ts pkg/web/dashboard/src/components/feed-workspace/feed-view-model.test.ts
git commit -m "feat(dashboard): stderr-строки, script_failed и stderr-хвост hook_failed в ленте"
```

---

## Task 8: build bundle + live verification

**Files:** `pkg/web/dashboard/index.html`, `pkg/web/dashboard/assets/*`, `pkg/web/dashboard/public/skins/*` (regenerated).

- [ ] **Step 1: Build** — `cd pkg/web/dashboard && npm run build`.
- [ ] **Step 2: Build afm** — `go build -o /tmp/afm-stderr/afm ./cmd/afm`.
- [ ] **Step 3: Live repro** — a flow with a **failing** script stage that writes to BOTH streams, plus a passing stage with a failing `script_after`:
  ```yaml
  stages:
    - id: buildfail
      script: |
        echo "starting build (stdout)"
        printf 'Traceback (most recent call last):\n  File "x.py", line 1\nValueError: KaBoom\n' 1>&2
        exit 1
    - id: hookfail          # INDEPENDENT of buildfail (codex minor: a dep on the failed stage would block it, never running script_after)
      script: echo "hook stage ok"
      script_after: |
        echo "cleaning up (stderr)" 1>&2
        exit 3
    - id: hold
      depends_on: [hookfail]
      auto_run: false        # keeps the dashboard up for inspection
      script: echo held
  ```
  (Config: docker off, a fixed port, `open_browser:false`.) Run it (background). `buildfail` and `hookfail` have no dependency between them, so both actually execute.
- [ ] **Step 4: Verify via curl + browser** — `curl /api/events?stage=buildfail` shows: `script_output stream=stdout "starting build"`, `script_output stream=stderr "Traceback: KaBoom..."`, and a `script_failed` notice with `error` + `stderr_tail` containing the traceback. Confirm the stage shows as **failed** in the UI. Open the dashboard (`open http://localhost:PORT`) and confirm the feed shows the stderr line distinctly + a red "script failed" row with the reason; a failing `script_after` shows a `hook_failed` row carrying its stderr tail.
- [ ] **Step 5: Commit the bundle**
```bash
git add pkg/web/dashboard/index.html pkg/web/dashboard/assets pkg/web/dashboard/public/skins
git commit -m "build(dashboard): пересборка — stderr/script_failed в ленте"
```

---

## Task 9: docs + changelog

**Files:** `docs/script-stages.md`, `CHANGELOG.md`, `pkg/web/dashboard/RELEASE_NOTES.md`

- [ ] **Step 1:** `docs/script-stages.md` — document that stderr now streams to the feed (distinct styling), a failed `script:` stage shows a `script failed: <reason>` row with a stderr tail, and `hook_failed` carries a stderr tail; the full stderr still lives in `<stage>/<phase>.stderr.log`.
- [ ] **Step 2:** `CHANGELOG.md` — dated `## 2026-09-23` (append under any existing same-day section) `### Improvement:` (English), covering stderr-in-feed + `script_failed` reason + hook stderr tail.
- [ ] **Step 3:** `RELEASE_NOTES.md` — user-facing one-liner.
- [ ] **Step 4: Commit** `docs: видимость ошибок script/hook (stderr + причина) в ленте`.

---

## Self-Review

**Spec coverage:**
- "Stream stderr to the feed like stdout" → Tasks 1-3 (lineWriter → RunScript OnAction("stderr") → execScript `stream` field) + Task 7 (render). ✓
- "On failure include what it failed with (stderr) in a notice" → Task 4 (`script_failed` with `stderr_tail`) + Task 5 (`hook_failed` + `stderr_tail`). ✓
- "For script-stage failures emit a UI notice" → Task 4 (`script_failed`). ✓
- "If a script fails it shows in the UI as a failed script" → the stage already transitions to `failed` (existing `EvFail`, shown as a red stage + `→ failed` feed row); Task 4 ADDS the reason row so it's unambiguous. Task 8 Step 4 explicitly verifies the failed status in the UI. ✓

**Type/name consistency:** `stderrTailMaxLines=20`/`stderrTailMaxBytes=4096` defined once in orchestrator, `ReadStderrTail(logFile, maxLines, maxBytes)` in executor; `EventScriptFailed="script_failed"`; `script_output` data key `stream`; notice data key `stderr_tail` used identically in `script_failed` + `hook_failed` + the frontend readers.

**Open decisions (defaults chosen):**
1. **stderr streaming is script-only** (not agents) — agent stderr is noisy CLI diagnostics; keep file-only.
2. **stderr line tone = `warning`, not `danger`** — stderr is not always an error (progress/warnings use it); the FAILURE rows are `danger`.
3. **Tail = last 20 lines / 4 KiB.** Full stderr remains in `.stderr.log`.
4. **`script_failed` NOT added to `SIGNIFICANT_EVENT_TYPES`** — the `failed` transition already refreshes status.
5. **stdout/stderr interleaving** in the feed is best-effort (separate OS streams, separate reader goroutines) — each line timestamped on arrival; exact cross-stream ordering isn't guaranteed. Acceptable.

**Concurrency note:** stdout (scanner goroutine) and stderr (os/exec's stderr-copy goroutine via the MultiWriter) now BOTH call `OnAction` → `o.ui.Publish` + `stagefiles.AppendNotice` concurrently for the same stage. Resolved in Task 3 Step 3b (a `sync.Mutex` in `AppendNotice`); cross-stream ordering is best-effort by design.

## Codex plan-review (2026-09-23) — resolutions
- **[Important] `ReadStderrTail` unbounded / byte-cap violated by a single long line** → Task 1: bounded suffix read (`Seek`), front-cut to `maxBytes` on a rune boundary even for one huge line, `maxLines/maxBytes<=0`→`""`; tests `SingleHugeLine_CappedToBytes`, `LargeFile_MemoryBounded`.
- **[Important] `lineWriter` unbounded partial-line buffer** → Task 1: `maxLineBytes=64KiB` cap + `…[truncated]` marker; test `HugeLineNoNewline_TruncatesNotUnbounded`.
- **[Important] stderr lost when `openStderrLog` fails** → Task 2: always tee to `lineWriter` with `io.Discard` file branch; only the durable tail is lost, live stream survives.
- **[Important] timeout/interruption flush** → Task 2: explicit test for a trailing partial stderr line under `ScriptTimeout`.
- **[Important] `AppendNotice` unsynchronized** → Task 3 Step 3b: package `sync.Mutex` + concurrent-append test; ordering documented best-effort.
- **[Important] `script_failed` live/history dedup** → Task 7: kept OUT of `CONTENT_DEDUPE_ON_INGEST` (like `script_output`), relies on `mergeCapped` content-dedup; explicit `use-event-feed` test.
- **[Important] `script_failed` design/ordering** → Task 4: justified (transition drops the reason; lifecycle events aren't feed events); publish-after-Trigger ordering documented cosmetic; FSM path untouched (Global Constraint).
- **[minor] EventScriptOutput doc / legacy `stream` absent** → Task 3 Step 3/3c (doc + legacy-field decode test; frontend treats absent `stream` as stdout).
- **[minor] hook tail cumulative across retries** → Task 5: intentional (final attempt's stderr at the tail), documented.
- **[minor] multiline tail rendering** → Task 7: fenced code block + real-traceback test.
- **[minor] live repro dep-blocks `script_after`** → Task 8: `hookfail` made independent of `buildfail`.
