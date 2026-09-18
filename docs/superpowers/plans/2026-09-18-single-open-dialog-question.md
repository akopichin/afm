# Single Open Dialog Question — Enforcement (afm defense-in-depth) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make afm structurally guarantee that at most one interactive dialog question per stage is answerable at a time — the one the agent's bash loop is actually blocked on (the OLDEST unanswered, by the `q<N>` id it declared) — so an agent that writes a batch of `qN.question.json` files (and polls only `q1`) can never deadlock when the user answers a later question.

**Architecture:** `pkg/mcp` gains a pure `SelectCurrentQuestion([]QuestionFile)` (oldest unanswered by `q<N>` id order, `(phase,id)` identity) and a thin `CurrentQuestion(stageDir)` wrapper. Four independent layers consume it — the poller processes only the current question (repairs it if malformed, surfaces it if valid, holds the rest), the dialog read API shows only the current *valid* question (interactive stages only), the answer HTTP handler rejects out-of-order answers (interactive stages only), and the interactive prompt tells the agent the rule. Non-interactive (auto-answer) stages are untouched. **Ordering key = the `q<N>` numeric suffix of the id**, which is embedded in the filename, is authoritative (the existing code already keys the answer path off the filename id, never the JSON body), and — unlike file mtime — is NOT mutated by the jsonrepair rewrite, the malformed-stub write, or question relocation. Question identity is always the `(phase, id)` pair (ids are unique only within a phase).

**Tech Stack:** Go (`pkg/mcp`, `pkg/orchestrator`, `pkg/server`, `pkg/prompts`), TypeScript/React (`pkg/web/dashboard`).

**Spec:** Self-contained. Root-cause: the 2026-09-18 `superpowers:systematic-debugging` session — spider-ui `GogaRefinement-20260917-175213-bab3` (worked: one `question.json` per round) vs python-qarium `GogaRefinement-20260917-203719-e58a` (hung: wrote `q1..q8.question.json` at once, polled only `q1`, user answered `q8` → deadlock on `q1.answer.json`). afm assumed one open question per stage but enforced nothing.

**Review history:** Hardened across two codex reviews of the plan.
- Iter 1 (8 findings): `(phase,id)` identity, `os.ReadDir` (glob-metachar safety), fail-closed guards, malformed gate ordering, typed frontend error, real test helpers.
- Iter 2 (2 CRITICAL + 4 MAJOR): **(C1)** the poller must pick the current question from the slice it already fetched, never a second FS scan, and must never fail-open; **(C2)** ordering must use the immutable `q<N>` id, NOT file mtime (mtime is rewritten by `runJSONFixAgent`, `giveUpOnMalformedQuestion`, and `normalizeMisplacedQuestion`) — mtime/`ModTime`/`os.Chtimes` are dropped entirely; **(M3)** fail-closed guards; **(M4)** the read-side visibility map must match only *unanswered* entries and emit exactly one; **(M5)** the frontend must track `(phase,id)`, not `id`; **(M6)** read/answer serialization must be gated on `stage.Interactive`.
- Iter 3 (2 CRITICAL + 4 MAJOR + 2 MINOR): **(C1)** the iter2 "unreadable→Malformed" fix was reverted — it dead-locks repair; Task 1 now only swaps glob→ReadDir and keeps the existing read-error `continue` (a genuine IO error on afm's own local file is out of scope; torn/broken JSON is still covered by the malformed machine); **(C2)** `buildDialogEntries` must keep the original all-pending append for `serialize=false` (non-interactive) and apply the one-question filter only for `serialize=true`; **(M)** `askOrder` accepts only strict `q<N>` (not `foo2`); `TestDialogGetWithTranscript` stays on `serialize=false`; the answer guard's `!hasCur` fall-through is documented as safe; the frontend composite key is computed inside `refresh` from `data` (not a stale render-scope value); **(minor)** `/` delimiter not NUL; `strconv` import + real harness in Task 8.

## Global Constraints

- Do NOT change the Go version in `go.mod`.
- Run `golangci-lint run ./...` after Go edits; build/lint clean. No deprecated constructs.
- Commit messages in Russian. No `Co-Authored-By`.
- Prefer the simplest correct option. Ordering uses the immutable filename id — no new persisted state, no locks, no mtime.
- Non-interactive / `agents:[auto]` stages MUST keep byte-for-byte identical behavior. Every serialization gate (poller, read API, answer handler) is applied ONLY when the stage is interactive.
- Question identity is the `(phase, id)` pair everywhere — `id` alone is unique only within a phase (`planning.q1` and `implementation.q1` can coexist; see `TestFindUnansweredQuestions`).
- Ordering assumes the agent declares ids in ask order (`q1`, `q2`, …), which the `<interactive_rules>` prompt mandates and which is immutable across repair/relocation. This is the documented contract; a non-`qN`/out-of-write-order id is a best-effort case (falls back to lexical).
- **Known limitation (iter3-M, non-blocking):** when two DIFFERENT phases have an unanswered same-numbered id at once (e.g. a stale `implementation/q1` lingering after the agent completed — see `onAgentCompleted` in `orchestrator.go`, plus a fresh `review/q1`), the `(phase,id)` tie-break orders by phase lexically (`implementation` < `review`), so the stale implementation question could be shown before the one the review agent is actually blocked on. This does not cause a permanent deadlock (the review agent's question is still answerable once the stale one is cleared) and never affects non-interactive stages; a full "active-phase-first" ordering is out of scope. In practice a stage runs one phase's agent at a time, so simultaneous cross-phase unanswered questions are rare.
- The existing `"Ask ONE question at a time."` prompt line was ignored by a real agent — the structural layers (Tasks 3–5) are the guarantee; the prompt (Task 6) only helps.

---

## File Structure

- `pkg/mcp/dialog.go` — (Task 1) `FindUnansweredQuestions` → `os.ReadDir` (listing only; read-error keeps its existing `continue`); (Task 2) add `askOrder`/`AskOrderLess`/`questionBefore`/`SelectCurrentQuestion`/`CurrentQuestion`. No mtime.
- `pkg/mcp/dialog_test.go` (`package mcp_test`) — unit tests calling `mcp.*` (no `time` import needed).
- `pkg/orchestrator/dialog_poller.go` — (Task 3) `pollQuestions` picks the current question from the already-fetched `questions` via `mcp.SelectCurrentQuestion`; for interactive stages, hold every non-current `(phase,id)`; gate sits BEFORE the malformed branch.
- `pkg/orchestrator/dialog_poller_test.go` — serialization tests reusing `writeQuestionFile`/`drainDialogQuestionEvents`.
- `pkg/server/handlers.go` — (Task 4) `buildDialogEntries(stageDir, serialize bool)`; (Task 5) `handleDialogAnswer` out-of-order guard, both gated on `s.stageInteractive[stageID]`.
- `pkg/server/handlers_test.go` / `pkg/server/dialog_answer_flowpause_test.go` — read-filter + rejection tests.
- `pkg/prompts/builder.go` / `builder_test.go` — (Task 6) `<interactive_rules>`.
- `pkg/web/dashboard/src/api/run-client.ts` + `.../dialog-channel/DialogChannel.tsx` — (Task 7) typed error, `(phase,id)` composite key, reload only on `answer_out_of_order`.

---

### Task 1: `FindUnansweredQuestions` — `os.ReadDir` (glob-metachar safe)

**Files:**
- Modify: `pkg/mcp/dialog.go` (`FindUnansweredQuestions` listing only)
- Test: `pkg/mcp/dialog_test.go`

**Interfaces:**
- Produces: `FindUnansweredQuestions(stageDir string) ([]QuestionFile, error)` no longer uses `filepath.Glob` (so a `[` in the path is a literal, not a pattern). `QuestionFile` is unchanged.

Design note (iter3-C1): the earlier draft turned a *read error* into a `Malformed` entry to be "fail-closed", but that is strictly worse — `handleMalformedQuestion` re-reads the file, returns on the same error, and the stage blocks forever with no repair. A genuine IO read error on a local `.afm/` file afm wrote itself is essentially impossible; the realistic case (torn read / broken JSON) is already handled by the parse→jsonrepair→malformed machine. So the read-error branch keeps its **existing** `continue` behavior — this task changes ONLY the directory listing (glob → ReadDir). Do NOT add mtime, `ModTime`, or `os.Chtimes`.

- [ ] **Step 1: Write the failing test**

Add to `pkg/mcp/dialog_test.go`:

```go
func TestFindUnansweredQuestions_GlobMetacharDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "run[1]")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"q1","question":"Q","options":["A"],"allow_custom":true}`
	if err := os.WriteFile(filepath.Join(base, "planning.q1.question.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	qs, err := mcp.FindUnansweredQuestions(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].ID != "q1" {
		t.Fatalf("expected q1 found in a '[' path, got %+v", qs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mcp/ -run TestFindUnansweredQuestions_GlobMetacharDir -v`
Expected: FAIL — the metachar dir currently matches nothing via `Glob` (0 results).

- [ ] **Step 3: Write minimal implementation**

In `pkg/mcp/dialog.go`, `FindUnansweredQuestions`, replace ONLY the `filepath.Glob` listing with `os.ReadDir`:

```go
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []QuestionFile
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".question.json") {
			continue
		}
		qPath := filepath.Join(stageDir, ent.Name())
		base := strings.TrimSuffix(ent.Name(), ".question.json")
		// ... keep the ENTIRE rest of the loop body unchanged: phase/id split,
		// IsValidPhase skip, answerPath "already answered" skip, os.ReadFile
		// with its existing `continue`-on-error, unmarshal/jsonrepair/malformed,
		// the id=="" skip, and the final QuestionFile append ...
```

(The loop body below `base := ...` is byte-for-byte the current code; only the listing changed from `filepath.Glob(...)` matches to `os.ReadDir` entries.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mcp/...`
Expected: PASS — new test + all existing `TestFindUnansweredQuestions*` (ReadDir filtering by `.question.json` suffix is equivalent to the `*.question.json` glob).

- [ ] **Step 5: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "refactor(mcp): FindUnansweredQuestions на os.ReadDir (безопасно к glob-метасимволам в пути)"
```

---

### Task 2: `AskOrderLess` + `SelectCurrentQuestion` + `CurrentQuestion`

**Files:**
- Modify: `pkg/mcp/dialog.go` (add `import "strconv"`; add `askOrder`, `AskOrderLess`, `questionBefore`, `SelectCurrentQuestion`, `CurrentQuestion`)
- Test: `pkg/mcp/dialog_test.go`

**Interfaces:**
- Produces:
  - `func AskOrderLess(a, b string) bool` — `q<N>` numeric order (q2 before q10); numeric before non-numeric; else lexical.
  - `func SelectCurrentQuestion(qs []QuestionFile) (QuestionFile, bool)` — **pure** (no FS). Oldest by `AskOrderLess(id)`, tie-broken by `(phase,id)` lexical. Includes malformed entries. `ok=false` for empty input. This is what the poller uses on the slice it already has (iter2-C1).
  - `func CurrentQuestion(stageDir string) (QuestionFile, bool, error)` — `FindUnansweredQuestions` + `SelectCurrentQuestion`. Used by the server read/answer paths. Propagates the `FindUnansweredQuestions` error (enables fail-closed).

- [ ] **Step 1: Write the failing test**

Add to `pkg/mcp/dialog_test.go` (`package mcp_test`):

```go
func TestAskOrderLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"q2", "q10", true}, {"q10", "q2", false}, {"q1", "q2", true},
		{"q1", "foo", true}, {"foo", "q1", false}, {"aaa", "bbb", true},
		// Only exact q<N> is numeric; foo2/bar10/qX fall back to lexical.
		{"foo2", "foo10", false}, // lexical: "foo10" < "foo2"
		{"q1", "foo2", true},     // numeric q1 before non-numeric foo2
		{"qX", "q1", false},      // qX not numeric → q1 (numeric) sorts first
	}
	for _, c := range cases {
		if got := mcp.AskOrderLess(c.a, c.b); got != c.want {
			t.Errorf("AskOrderLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSelectCurrentQuestion(t *testing.T) {
	qs := []mcp.QuestionFile{
		{Phase: "autonomous_execution", ID: "q10"},
		{Phase: "autonomous_execution", ID: "q2"},
		{Phase: "autonomous_execution", ID: "q1", Malformed: true}, // malformed still wins if oldest
	}
	cur, ok := mcp.SelectCurrentQuestion(qs)
	if !ok || cur.ID != "q1" {
		t.Fatalf("expected q1 current, got ok=%v id=%q", ok, cur.ID)
	}
	if _, ok := mcp.SelectCurrentQuestion(nil); ok {
		t.Fatalf("empty input must return ok=false")
	}
}

func TestCurrentQuestion_ReadsDirAndSelects(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q2", "q1"} {
		body := `{"id":"` + id + `","question":"Q","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(dir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cur, ok, err := mcp.CurrentQuestion(dir)
	if err != nil || !ok || cur.ID != "q1" {
		t.Fatalf("expected q1 current, got ok=%v id=%q err=%v", ok, cur.ID, err)
	}
	// Answer q1 → q2 becomes current.
	if err := os.WriteFile(filepath.Join(dir, "autonomous_execution.q1.answer.json"),
		[]byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	cur, ok, err = mcp.CurrentQuestion(dir)
	if err != nil || !ok || cur.ID != "q2" {
		t.Fatalf("expected q2 current after answering q1, got ok=%v id=%q err=%v", ok, cur.ID, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mcp/ -run 'TestAskOrderLess|TestSelectCurrentQuestion|TestCurrentQuestion' -v`
Expected: FAIL — undefined `mcp.AskOrderLess`, `mcp.SelectCurrentQuestion`, `mcp.CurrentQuestion`.

- [ ] **Step 3: Write minimal implementation**

Add `"strconv"` to the import block in `pkg/mcp/dialog.go`, then:

```go
// askOrder parses the strict q<N> form (q1, q2, … q10): the id must be 'q'
// followed by one or more digits and nothing else. foo2/bar10/qX are NOT
// numeric — they fall back to lexical order in AskOrderLess.
func askOrder(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'q' {
		return 0, false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil {
		return 0, false // overflow → treat as non-numeric
	}
	return n, true
}

// AskOrderLess orders question ids by the q<N> convention the interactive
// prompt mandates: numeric suffix order (q2 before q10), a numeric id before a
// non-numeric one, else lexical. The id is authoritative (filename-derived) and
// immutable across repair/relocation, so this is a stable ask-order key.
func AskOrderLess(a, b string) bool {
	na, oka := askOrder(a)
	nb, okb := askOrder(b)
	switch {
	case oka && okb:
		if na != nb {
			return na < nb
		}
		return a < b
	case oka:
		return true
	case okb:
		return false
	default:
		return a < b
	}
}

func questionBefore(a, b QuestionFile) bool {
	if a.ID != b.ID {
		return AskOrderLess(a.ID, b.ID)
	}
	return a.Phase < b.Phase
}

// SelectCurrentQuestion returns the oldest unanswered question in qs — the one
// the agent's sequential polling loop is blocked on and the ONLY one afm should
// surface / accept an answer for. Pure (no FS): callers that already scanned the
// directory pass their slice, avoiding a second, inconsistent scan. Malformed
// entries participate (a malformed oldest question still blocks younger ones;
// the poller repairs it first). ok is false for empty input.
func SelectCurrentQuestion(qs []QuestionFile) (QuestionFile, bool) {
	var cur QuestionFile
	found := false
	for _, q := range qs {
		if !found || questionBefore(q, cur) {
			cur = q
			found = true
		}
	}
	return cur, found
}

// CurrentQuestion is the FS-backed convenience wrapper for server read/answer
// paths. It propagates the FindUnansweredQuestions error so callers can fail
// closed.
func CurrentQuestion(stageDir string) (QuestionFile, bool, error) {
	qs, err := FindUnansweredQuestions(stageDir)
	if err != nil {
		return QuestionFile{}, false, err
	}
	cur, ok := SelectCurrentQuestion(qs)
	return cur, ok, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mcp/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "feat(mcp): SelectCurrentQuestion/CurrentQuestion — старший вопрос по неизменяемому id"
```

---

### Task 3: Poller processes only the current interactive question (no re-scan, no fail-open)

**Files:**
- Modify: `pkg/orchestrator/dialog_poller.go` (`pollQuestions`)
- Test: `pkg/orchestrator/dialog_poller_test.go`

**Interfaces:**
- Consumes: `mcp.SelectCurrentQuestion` (Task 2), the already-fetched `questions []mcp.QuestionFile`, `stage.Interactive`, the per-question loop.
- Produces: for an interactive stage, exactly one question — the current `(phase,id)`, chosen from the SAME `questions` slice — is processed per tick; malformed handling runs only for the current one; the next is processed only after the present one is answered.

Design notes (iter2 C1/C2/F4):
- The current question is selected from `questions` (already fetched this tick), NOT via a second `mcp.CurrentQuestion(stageDir)` FS scan — no snapshot skew, no fail-open. Since the gate only holds *back* questions (never invents one), and `SelectCurrentQuestion` returns ok whenever `questions` is non-empty, there is no error path that disables the gate.
- Ordering is by immutable `q<N>` id, so a repaired/relocated question keeps its position — a younger question can never preempt the one the agent polls (resolves iter2-C2 and F4).
- The gate sits BEFORE the `if q.Malformed` branch, so a malformed *younger* question is neither repaired-ahead nor able to publish its own `EvAskUser` via `giveUpOnMalformedQuestion`.

- [ ] **Step 1: Write the failing test**

Add to `pkg/orchestrator/dialog_poller_test.go` (mirrors `TestPollQuestions_InteractiveQuestion_EmitsDialogQuestionOnce`; no `time` import needed):

```go
func TestPollQuestions_InteractiveBatch_SurfacesOldestOnly(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, stage.ID)

	// Agent wrote q1 and q2 at once (a batch); it polls q1.
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A"})
	writeQuestionFile(t, stageDir, "implementation", "q2", []string{"A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	subID, events := o.ui.Subscribe(64)
	defer o.ui.Unsubscribe(subID)

	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}
	o.pollQuestions(processed, malformed)

	first := drainDialogQuestionEvents(events)
	if len(first) != 1 || first[0].Data.(map[string]any)["id"] != "q1" {
		t.Fatalf("expected only q1 surfaced first, got %+v", first)
	}

	// Answer q1 → next tick surfaces q2.
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.q1.answer.json"),
		[]byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "user_answered"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed)
	second := drainDialogQuestionEvents(events)
	if len(second) != 1 || second[0].Data.(map[string]any)["id"] != "q2" {
		t.Fatalf("expected q2 surfaced after answering q1, got %+v", second)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestPollQuestions_InteractiveBatch_SurfacesOldestOnly -v`
Expected: FAIL — the first `pollQuestions` currently surfaces BOTH q1 and q2.

- [ ] **Step 3: Write minimal implementation**

In `pkg/orchestrator/dialog_poller.go`, inside `pollQuestions`, right after `questions, err := mcp.FindUnansweredQuestions(stageDir)` + its error `continue`, add:

```go
		// Serialize interactive dialogs: an agent's polling loop blocks on the
		// FIRST question it wrote, so afm must process only the oldest unanswered
		// question (by its immutable q<N> id) and hold the rest. Chosen from the
		// SAME slice we just scanned — never a second FS read. Non-interactive
		// stages are exempt (they auto-answer every question below).
		var interactiveCurrent *mcp.QuestionFile
		if stage != nil && stage.Interactive {
			if cur, ok := mcp.SelectCurrentQuestion(questions); ok {
				c := cur
				interactiveCurrent = &c
			}
		}
```

Then at the very TOP of the `for _, q := range questions {` loop body — BEFORE `key := ...` and BEFORE the `if q.Malformed` branch — add:

```go
		// Hold back every interactive question that is not the current one. Not
		// marked processed, so it is re-evaluated each tick and surfaced once it
		// becomes current.
		if interactiveCurrent != nil &&
			(q.Phase != interactiveCurrent.Phase || q.ID != interactiveCurrent.ID) {
			continue
		}
```

(Leave `key`, malformed handling, `processed`, dialog.jsonl append, non-interactive auto-answer, and interactive publish unchanged.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run 'TestPollQuestions' -v`
Expected: PASS — new test + all existing `TestPollQuestions_*`.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/dialog_poller.go pkg/orchestrator/dialog_poller_test.go
git commit -m "feat(orchestrator): интерактивная стадия обрабатывает только текущий вопрос из уже прочитанного слайса"
```

---

### Task 4: Dialog read API shows only the current valid question (interactive-gated, fail-closed)

**Files:**
- Modify: `pkg/server/handlers.go` (`buildDialogEntries` signature + body; `handleDialogGet` caller)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `mcp.CurrentQuestion` (Task 2), `s.stageInteractive` (already on `Server`), `dialogUIEntry`, `typeAgentText`.
- Produces: `buildDialogEntries(stageDir string, serialize bool) []dialogUIEntry`. When `serialize` is false (non-interactive) it behaves exactly as today. When true, it returns full answered history + agent text and at most ONE unanswered question — the current valid one; on a `CurrentQuestion` error it shows NO unanswered question (fail-closed) and logs.

Design notes (iter2 M4/M6):
- The visibility map matches only **unanswered** `(phase,id)` entries, so an answered history entry with a reused id does not block adding the current question, and duplicate transcript entries are collapsed to exactly one.
- `handleDialogGet` passes `s.stageInteractive[stageID]` — non-interactive read semantics are untouched.

- [ ] **Step 1: Write the failing test**

Add to `pkg/server/handlers_test.go`:

```go
func TestBuildDialogEntries_SerializeShowsOnlyCurrent(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q1", "q2", "q3"} {
		body := `{"id":"` + id + `","question":"Q ` + id + `","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(dir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := buildDialogEntries(dir, true)
	var unanswered []string
	for _, e := range out {
		if e.Type == typeAgentText || e.ID == "" || e.Answer != nil {
			continue
		}
		unanswered = append(unanswered, e.ID)
	}
	if len(unanswered) != 1 || unanswered[0] != "q1" {
		t.Fatalf("serialize=true: expected only q1 unanswered, got %v", unanswered)
	}
}

func TestBuildDialogEntries_NoSerializeShowsAll(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"q1", "q2"} {
		body := `{"id":"` + id + `","question":"Q ` + id + `","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(dir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := buildDialogEntries(dir, false)
	count := 0
	for _, e := range out {
		if e.Type != typeAgentText && e.ID != "" && e.Answer == nil {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("serialize=false: expected both questions, got %d", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run 'TestBuildDialogEntries' -v`
Expected: FAIL to compile — `buildDialogEntries` takes one arg. (After adding the param, `TestBuildDialogEntries_SerializeShowsOnlyCurrent` fails because all three show.)

- [ ] **Step 3: Write minimal implementation**

(a) Change the signature and the caller:

```go
func buildDialogEntries(stageDir string, serialize bool) []dialogUIEntry {
```
```go
	// handleDialogGet:
	out := buildDialogEntries(stageDir, s.stageInteractive[stageID])
```
Update the THREE existing call sites in `handlers_test.go` (`TestDialogGet`, `TestDialogGetWithTranscript`, `TestDialogGetNoDialogFile`) to pass `false` — they assert the base transcript/visibility behavior, which `serialize=false` preserves byte-for-byte (iter3-M3: `TestDialogGetWithTranscript` has a `q2` that exists only in `dialog.jsonl` with no question-file, which the serialize filter would drop — keep it on `false`).

(b) Keep the EXISTING guarantee-visibility block (the `if pending, perr := mcp.FindUnansweredQuestions(stageDir); perr == nil { ... append all pending not in haveID ... }`) UNCHANGED — it runs for both modes and preserves non-interactive behavior exactly. Then replace only the final `return out` with the serialize gate + filter:

```go
	if !serialize {
		return out // non-interactive: unchanged behavior (all pending shown)
	}

	// Interactive stages: at most ONE open question — the current (oldest
	// unanswered) valid one. Determine it once, fail closed on error.
	cur, hasCur, curErr := mcp.CurrentQuestion(stageDir)
	if curErr != nil {
		log.Printf("WARN: dialog current-question lookup failed for %s: %v", stageDir, curErr)
		hasCur = false
	}
	showCur := hasCur && !cur.Malformed

	// Is the current question already present as an UNANSWERED entry? (An
	// answered entry with a reused id must NOT block adding it.)
	haveUnansweredCur := false
	for _, e := range out {
		if e.Type != typeAgentText && e.Phase == cur.Phase && e.ID == cur.ID && e.Answer == nil {
			haveUnansweredCur = true
			break
		}
	}
	if showCur && !haveUnansweredCur {
		out = append(out, dialogUIEntry{
			Phase: cur.Phase, ID: cur.ID, Question: cur.Question,
			Options: cur.Options, AllowCustom: cur.AllowCustom,
		})
	}

	// Keep answered history + agent text; among unanswered questions keep
	// exactly ONE — the current valid one (first occurrence wins, dropping any
	// transcript duplicates).
	filtered := out[:0]
	keptCur := false
	for _, e := range out {
		isUnanswered := e.Type != typeAgentText && e.ID != "" && e.Answer == nil
		if isUnanswered {
			isCur := showCur && e.Phase == cur.Phase && e.ID == cur.ID
			if !isCur || keptCur {
				continue
			}
			keptCur = true
		}
		filtered = append(filtered, e)
	}
	return filtered
```

Ensure `log` is imported in `handlers.go` (it is).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run 'TestDialogGet|TestBuildDialogEntries' -v`
Expected: PASS (existing `TestDialogGet*` updated to pass `false` → unchanged behavior; the new `TestBuildDialogEntries_*` cover both modes explicitly).

- [ ] **Step 5: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "feat(server): /dialog интерактивной стадии отдаёт только текущий валидный вопрос (fail-closed)"
```

---

### Task 5: Answer handler rejects out-of-order answers (interactive-gated)

**Files:**
- Modify: `pkg/server/handlers.go` (`handleDialogAnswer`)
- Test: `pkg/server/dialog_answer_flowpause_test.go`

**Interfaces:**
- Consumes: `mcp.CurrentQuestion` (Task 2), `s.stageInteractive`, `writeFlowError`.
- Produces: for an **interactive** stage, `POST .../dialog/answer` returns `409 {"error":"answer_out_of_order"}` when `(req.Phase, req.ID)` is not the current question, and `500 {"error":"current_question_lookup_failed"}` on a lookup error (fail-closed — the answer is not written). Non-interactive stages skip the check entirely.

- [ ] **Step 1: Write the failing test**

Add to `pkg/server/dialog_answer_flowpause_test.go` (reuse that file's server/stage/POST scaffolding from `TestHandleDialogAnswer_AllowedWhenNotPaused`; ensure the test stage is registered as interactive in the server's `stageInteractive` map):

```go
func TestHandleDialogAnswer_RejectsOutOfOrder(t *testing.T) {
	// Build the Server with stageInteractive[stageID]=true (as the existing
	// flowpause tests build it; add the interactive flag to that config).
	s, stageID, stageDir := newInteractiveDialogAnswerServer(t)

	for _, id := range []string{"q1", "q2"} {
		body := `{"id":"` + id + `","question":"Q","options":["A"],"allow_custom":true}`
		if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution."+id+".question.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	rec := doDialogAnswer(t, s, stageID, "autonomous_execution", "q2", "x", false)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q2.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("q2.answer.json must NOT be written on rejection")
	}

	rec = doDialogAnswer(t, s, stageID, "autonomous_execution", "q1", "ok", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for current question, got %d", rec.Code)
	}
}
```

> Implementation note: `newInteractiveDialogAnswerServer`/`doDialogAnswer` stand in for the exact scaffolding already in `dialog_answer_flowpause_test.go` — inline it, and make sure the server config marks `stageID` interactive (the serialization gate reads `s.stageInteractive`). If the existing helper doesn't set interactive, extend the config it builds.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run TestHandleDialogAnswer_RejectsOutOfOrder -v`
Expected: FAIL — q2 currently accepted (200) and written.

- [ ] **Step 3: Write minimal implementation**

In `pkg/server/handlers.go`, `handleDialogAnswer`, add immediately AFTER `stageDir := filepath.Join(s.runDir, stageID)` and BEFORE `questionPath := ...`:

```go
	// Serialize answers for interactive stages only: an agent's polling loop
	// blocks on the first question it wrote; answering a later one leaves the
	// earlier answer.json missing and deadlocks the agent. A well-behaved client
	// only shows the current question (buildDialogEntries), so this guards
	// stale/direct clients. Non-interactive stages auto-answer and are exempt.
	if s.stageInteractive[stageID] {
		cur, hasCur, err := mcp.CurrentQuestion(stageDir)
		if err != nil {
			writeFlowError(w, http.StatusInternalServerError, "current_question_lookup_failed")
			return
		}
		if hasCur && (cur.Phase != req.Phase || cur.ID != req.ID) {
			writeFlowError(w, http.StatusConflict, "answer_out_of_order")
			return
		}
	}
```

Design note (iter3-M1): the `!hasCur` case falls through intentionally — it means no unanswered question was found, so either `req.ID` is already answered (the existing `WriteAnswerFile` O_EXCL returns 409 "already answered") or it does not exist (the existing `os.Stat(questionPath)` returns 404). A legitimately-open `req.ID` always makes `hasCur` true with `cur == req`, so `!hasCur` never rejects a valid answer. The "an older question appears in the scan→write window" race is unreachable: the agent is blocked polling the question it already wrote (the current one), so it cannot have written an older one yet — no rollback/re-check is needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run 'TestHandleDialogAnswer' -v`
Expected: PASS — new test + all existing `TestHandleDialogAnswer_*` (single open question → it IS current → no-op; non-interactive tests skip the gate).

- [ ] **Step 5: Commit**

```bash
git add pkg/server/handlers.go pkg/server/dialog_answer_flowpause_test.go
git commit -m "feat(server): отклонять ответ вне очереди для интерактивной стадии (409), fail-closed при ошибке"
```

---

### Task 6: Strengthen the interactive prompt contract

**Files:**
- Modify: `pkg/prompts/builder.go` (`<interactive_rules>`)
- Test: `pkg/prompts/builder_test.go` (`package prompts`, call `Build`/`Inputs` directly)

**Interfaces:**
- Consumes: existing `<interactive_rules>` writer, `Inputs{Interactive, PhaseAgent, StageDir, Stage}`.
- Produces: the prompt states, imperatively, that only one question file may be open at a time and that afm enforces it.

- [ ] **Step 1: Write the failing test**

Add to `pkg/prompts/builder_test.go` (mirror the existing `Interactive: true` test):

```go
func TestBuild_InteractiveRules_ForbidsBatchQuestions(t *testing.T) {
	in := Inputs{
		Interactive: true,
		PhaseAgent:  "autonomous_execution",
		StageDir:    t.TempDir(),
		Stage:       flow.Stage{ID: "discover", Name: "Discover"},
	}
	out := Build(in)
	for _, want := range []string{
		"Write ONLY ONE question file at a time",
		"do NOT write the next question until",
		"afm accepts an answer only to the OLDEST unanswered question",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("interactive_rules missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/prompts/ -run TestBuild_InteractiveRules_ForbidsBatchQuestions -v`
Expected: FAIL — strings not present.

- [ ] **Step 3: Write minimal implementation**

In `pkg/prompts/builder.go`, replace `sb.WriteString("Ask ONE question at a time.\n")` with:

```go
		sb.WriteString("Ask ONE question at a time. Write ONLY ONE question file at a time.\n")
		fmt.Fprintf(&sb, "  After writing %s.q<N>.question.json, WAIT for its %s.q<N>.answer.json (step 2) before writing the next question — do NOT write the next question until the current one is answered.\n", in.PhaseAgent, in.PhaseAgent)
		sb.WriteString("  If you have several questions for this round, either ask them ONE BY ONE (recommended), or combine them into a SINGLE question file with numbered sub-questions inside 'question' and let the user answer them together.\n")
		sb.WriteString("  Note: afm accepts an answer only to the OLDEST unanswered question — any extra question files you write ahead of time are ignored until the earlier ones are answered, so writing a batch will stall you.\n")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/prompts/... -v`
Expected: PASS (update a golden/substring test if it pins the old line).

- [ ] **Step 5: Commit**

```bash
git add pkg/prompts/builder.go pkg/prompts/builder_test.go
git commit -m "feat(prompts): запретить писать несколько question-файлов за раз, пояснить enforcement"
```

---

### Task 7: Dashboard — `(phase,id)` composite key + typed error reload

**Files:**
- Modify: `pkg/web/dashboard/src/api/run-client.ts` (`answerDialog`)
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`

**Interfaces:**
- Consumes: `FlowApiError`, `readErrorCode`, `stageUrl`, `reload()`, `pending`.
- Produces: `answerDialog` throws `FlowApiError` on non-2xx. `DialogChannel` tracks the pending question by a `(phase,id)` composite key in `lastPending` and the scroll/flash effect deps (iter2-M5), so a `planning/q1 → implementation/q1` switch resets `selectedOption`/`customText`/`comments`. The panel reloads (discarding draft) ONLY for `code === 'answer_out_of_order'`; every other error keeps the draft (iter1-F6).

- [ ] **Step 1: `answerDialog` throws a typed error**

In `pkg/web/dashboard/src/api/run-client.ts`:

```ts
export async function answerDialog(
  stageId: string,
  phase: string,
  id: string,
  answer: string,
  fromOptions: boolean,
): Promise<void> {
  const url = stageUrl(stageId, 'dialog/answer')
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, phase, answer, from_options: fromOptions }),
  })
  if (!response.ok) {
    throw new FlowApiError(url, response.status, await readErrorCode(response))
  }
}
```

- [ ] **Step 2: Composite key + selective reload in `DialogChannel.tsx`**

Import `FlowApiError` from `../../api/run-client` (alongside `answerDialog`).

Add a tiny module-level helper (phases are fixed words with no `/`, so `/` is an unambiguous delimiter — do NOT use a literal NUL, which flags the file as binary):

```tsx
function questionKey(e: { phase?: string; id?: string } | null | undefined): string | undefined {
  return e == null ? undefined : `${e.phase ?? ''}/${e.id ?? ''}`
}
```

The polling effect (around line ~107) tracks `lastPendingId` as a LOCAL variable inside the effect and computes `findPending(data)?.id` INSIDE `refresh`. Change only that inner computation to the composite key — it is derived from the freshly-fetched `data`, so it is never stale (iter3-M4):

```tsx
    let lastPendingKey: string | undefined
    // ...
    const refresh = (): void => {
      const requestId = ++requestGen
      void loadDialog(current.id).then((data) => {
        if (cancelled) return
        if (requestId !== requestGen) return
        if (data === null) return
        setEntries(data)
        const nextPendingKey = questionKey(findPending(data))
        if (nextPendingKey !== lastPendingKey) {
          lastPendingKey = nextPendingKey
          setSelectedOption(null)
          setCustomText('')
          setComments({})
          setActiveCommentLine(null)
        }
      })
    }
```

If any RENDER-level effect (scroll/flash) depends on `pending?.id`, change that dependency to `questionKey(pending)` — a render-scope value, correct there. The staleness concern is ONLY about using a render-scope key inside the polling effect, which the code above avoids by computing from `data`.

In BOTH `sendAnswer` and `sendFeedback`, change the catch block (use each handler's own default message):

```tsx
    } catch (e) {
      setSubmitError(e instanceof Error ? e.message : 'failed to send answer')
      if (e instanceof FlowApiError && e.code === 'answer_out_of_order') {
        await reload() // stale view — resync to the real current question
      }
    } finally {
```

- [ ] **Step 3: Frontend tests**

Run: `cd pkg/web/dashboard && npm test`
Expected: PASS. Add/adjust DialogChannel tests: (a) reject with `new FlowApiError(url, 409, 'answer_out_of_order')` → `reload` called; (b) reject with a generic `Error` → draft preserved, no `reload`; (c) pending switches `planning/q1 → implementation/q1` → `selectedOption`/`customText` cleared.

- [ ] **Step 4: Build**

Run: `cd pkg/web/dashboard && npm run build`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx
git commit -m "feat(dashboard): (phase,id) ключ вопроса + resync только при answer_out_of_order"
```

---

### Task 8: Regression test mirroring the hung run + verification

**Files:**
- Test: `pkg/orchestrator/dialog_poller_test.go` (integration-style regression) or `pkg/server/dialog_answer_flowpause_test.go`

**Interfaces:** Consumes everything above.

- [ ] **Step 1: Regression test reproducing `GogaRefinement...discover` (the python-qarium hang)**

Add a test that mirrors the real log: an interactive stage where the agent wrote `q1..q8` at once and polls `q1`. Assert: (a) the poller surfaces only `q1`; (b) answering `q8` out-of-order is rejected `409` and writes no `q8.answer.json`; (c) answering `q1` is accepted and, on the next poll, `q2` is surfaced. Split into a poller-side assertion (a/c, orchestrator package) and a handler-side assertion (b, server package) — the two harnesses live in different packages. Add `"strconv"` to the orchestrator test file's imports (the loop below uses `strconv.Itoa`).

Poller leg (`pkg/orchestrator/dialog_poller_test.go`) — reuse the Task 3 harness verbatim (`state.Open`, interactive `flow.Stage`, `writeQuestionFile`, `drainDialogQuestionEvents`):

```go
func TestPollQuestions_Regression_BatchOfEight_NoDeadlock(t *testing.T) {
	// build interactive stage exactly as TestPollQuestions_InteractiveBatch_SurfacesOldestOnly
	// ... store, stage, stageDir, o, subID/events ...
	for i := 1; i <= 8; i++ {
		writeQuestionFile(t, stageDir, "autonomous_execution", "q"+strconv.Itoa(i), []string{"A"})
	}
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}
	o.pollQuestions(processed, malformed)
	ev := drainDialogQuestionEvents(events)
	if len(ev) != 1 || ev[0].Data.(map[string]any)["id"] != "q1" {
		t.Fatalf("batch of 8 must surface only q1, got %+v", ev)
	}

	// Answer q1 → the very next question (q2), not q3..q8, becomes current.
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.answer.json"),
		[]byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "user_answered"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed)
	ev = drainDialogQuestionEvents(events)
	if len(ev) != 1 || ev[0].Data.(map[string]any)["id"] != "q2" {
		t.Fatalf("after answering q1, only q2 must surface next, got %+v", ev)
	}
}
```

Handler leg (`pkg/server/dialog_answer_flowpause_test.go`) — reuse the Task 5 interactive-server harness; write `q1..q8`, assert answering `q8` → `409 answer_out_of_order` and no `q8.answer.json`, answering `q1` → `200`.

- [ ] **Step 2: Run it**

Run: `go test ./pkg/orchestrator/... ./pkg/server/... -run 'Regression' -v`
Expected: PASS.

- [ ] **Step 3: Full suite + lint**

Run:
```bash
golangci-lint run ./...
go test ./pkg/mcp/... ./pkg/orchestrator/... ./pkg/server/... ./pkg/prompts/...
cd pkg/web/dashboard && npm test && npm run build
```
Expected: clean, all green.

- [ ] **Step 4: Live run (dashboard, real browser)**

Per project memory (`project_docker_image_pin`, `project_file_browser_testing`, skill `verify`): interactive stage, mock agent writes `q1` then `q2` at once and polls `q1`. Confirm the dashboard offers only q1; answer it; q2 then appears; the agent proceeds — no hang. In a second tab still showing q2, confirm answering returns 409 and the panel resyncs.

---

## Self-Review

**1. Spec coverage:** one open interactive question — Tasks 3/4/5; no out-of-order answers — Task 5; agent told — Task 6; regression + live — Task 8; non-interactive untouched — every gate on `stage.Interactive`/`serialize`.

**2. Iter-2 findings folded in:**
- C1 (poller re-scan / fail-open): Task 3 uses `mcp.SelectCurrentQuestion(questions)` on the fetched slice; ok is true whenever non-empty → no fail-open. ✓
- C2 (mtime mutable): dropped entirely; ordering by immutable `q<N>` id (Task 2). ✓
- M3 (fail-closed): `CurrentQuestion` propagates the `FindUnansweredQuestions` error; handler → 500 (Task 5); read → fail-closed (Task 4). (iter3-C1: the "unreadable→Malformed" idea was reverted — read-error keeps `continue`, see Task 1 note.) ✓
- M4 (visibility map): matches only unanswered `(phase,id)`, emits exactly one (Task 4). ✓
- M5 (frontend `(phase,id)`): composite key (Task 7). ✓
- M6 (interactive gate): `serialize`/`s.stageInteractive` on read (Task 4) and answer (Task 5). ✓
- Minor (test imports/helpers): no `time` needed now; real helpers named; `mcp.*` qualified. ✓

**3. Type consistency:** `AskOrderLess(a,b string) bool`, `SelectCurrentQuestion([]mcp.QuestionFile) (mcp.QuestionFile, bool)`, `CurrentQuestion(stageDir string) (mcp.QuestionFile, bool, error)`, `buildDialogEntries(stageDir string, serialize bool)`, `writeFlowError(w, status, code)`, `s.stageInteractive map[string]bool`, `FlowApiError`/`readErrorCode` — all match the real code verified during planning.
