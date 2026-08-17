# Paused Stage Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new stage status `paused` for three cases: `auto_run: false` gating first activation, manual pause from the kebab menu on a live stage, and script stages which can only be paused before they start.

**Architecture:** New FSM status `paused` + a `PausedFrom` field on `StageState` that remembers both where to resume (Continue) and whether the stage has already been through one pause cycle (so `auto_run` never re-gates a retried stage). Manual pause reuses the existing `interruptChans`/SIGINT mechanism (`Revise` already uses it for `running`); Continue reuses the exact dispatch `recovery.go` already uses to resume a stage after an afm restart.

**Tech Stack:** Go (orchestrator/state/FSM/HTTP), React + TypeScript (dashboard), Vitest + `@testing-library/react`, `go test`.

**Spec:** `docs/superpowers/specs/2026-08-17-paused-stage-status-design.md`

## Global Constraints

- Commits must be in Russian (repo convention — see recent `git log`).
- Do not change the Go version in `go.mod`.
- Run `make lint` after backend changes; run `cd pkg/web/dashboard && npm run lint` (or the project's equivalent) after frontend changes — fix everything before moving to the next task.
- After any change to `pkg/state/state.go`'s `AllStatuses()`, run `go run ./tools/genstagestatus` (or `make generate`) — `make generate-check` fails CI if `stage-status.generated.ts` drifts.
- `StageState`/`Transition`/FSM `GuardCtx` are shared, hot-path types — every task touching them must keep the full existing test suite green, not just its own new tests.
- One correction to the spec, discovered during implementation research: the spec's §7 mentions duplicating the paused-entry notice into `notices.jsonl` "like `EventAutoAnswered`". This is unnecessary — `paused` is a real FSM transition (unlike auto-answer, which bypasses the FSM entirely), and `pkg/server/events_handler.go`'s `reconstructEventHistory` already maps *every* `events.jsonl` transition to a `stage_status_changed` feed event via `transitionToFeedEvents`. No task below writes to `notices.jsonl` for this feature.

---

### Task 1: `StatusPaused` stage status

**Files:**
- Modify: `pkg/state/state.go` (`StageStatus` const block, `AllStatuses()`)
- Modify: `pkg/state/state_test.go` (`TestAllStatuses_MatchesConsts`)
- Generate: `pkg/web/dashboard/src/types/stage-status.generated.ts` (via `go run ./tools/genstagestatus`)
- Modify: `pkg/web/dashboard/src/types/stage.ts` (`STAGE_STATUS_LABELS`, `ACTIVE_STAGE_STATUSES`)

**Interfaces:**
- Produces: `state.StatusPaused` (`StageStatus` constant, value `"paused"`), usable by every later task.

- [ ] **Step 1: Write the failing test**

Update the `want` slice in `pkg/state/state_test.go`'s `TestAllStatuses_MatchesConsts`:

```go
func TestAllStatuses_MatchesConsts(t *testing.T) {
	want := []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusPaused, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed,
	}
	got := AllStatuses()
	if len(got) != len(want) {
		t.Fatalf("AllStatuses() has %d entries, want %d: %v", len(got), len(want), got)
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("AllStatuses()[%d] = %q, want %q", i, got[i], s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/state/... -run TestAllStatuses_MatchesConsts -v`
Expected: FAIL — `got` still has 11 entries (`StatusPaused` doesn't exist yet, so this won't even compile until Step 3).

- [ ] **Step 3: Add `StatusPaused` to the enum and `AllStatuses()`**

In `pkg/state/state.go`, add the constant right after `StatusRetrying` (grouping it with the other "stage is waiting mid-flow" statuses):

```go
const (
	StatusPending           StageStatus = "pending"
	StatusPlanning          StageStatus = "planning"
	StatusAwaitingApproval  StageStatus = "awaiting_approval"
	StatusRevising          StageStatus = "revising"
	StatusReady             StageStatus = "ready"
	StatusRunning           StageStatus = "running"
	StatusRetrying          StageStatus = "retrying"
	// StatusPaused — стадия приостановлена: либо auto_run:false не дал ей
	// стартовать (PausedFrom=pending), либо пользователь вручную поставил на
	// паузу уже бегущую стадию (PausedFrom=running/planning/revising/retrying).
	// См. StageState.PausedFrom и bus.EvPause/EvContinue.
	StatusPaused            StageStatus = "paused"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	StatusDone              StageStatus = "done"
	StatusFailed            StageStatus = "failed"
	StatusHookFailed StageStatus = "hook_failed"
)

func AllStatuses() []StageStatus {
	return []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusPaused, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/state/... -run TestAllStatuses_MatchesConsts -v`
Expected: PASS

- [ ] **Step 5: Regenerate the frontend status union**

Run: `go run ./tools/genstagestatus`
Expected: `pkg/web/dashboard/src/types/stage-status.generated.ts` now includes `"paused"` in `STAGE_STATUSES`.

- [ ] **Step 6: Update the two hand-maintained TS maps that will now fail to typecheck**

`pkg/web/dashboard/src/types/stage.ts` — `Record<StageStatus, string>` requires every union member to have a label, so this won't compile until you add one:

```ts
export const STAGE_STATUS_LABELS: Record<StageStatus, string> = {
  pending: 'Pending',
  planning: 'Planning',
  awaiting_approval: 'Awaiting approval',
  revising: 'Revising',
  ready: 'Ready',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  retrying: 'Retrying',
  paused: 'Paused',
  awaiting_user_input: 'Awaiting reply',
  hook_failed: 'Hook failed',
}
```

`ACTIVE_STAGE_STATUSES` gains `'paused'` — a paused stage blocks the flow and needs a human decision, exactly like `awaiting_approval`/`awaiting_user_input` (same auto-select rationale already documented on this const):

```ts
export const ACTIVE_STAGE_STATUSES: ReadonlySet<StageStatus> = new Set([
  'running',
  'planning',
  'revising',
  'retrying',
  'paused',
  'awaiting_user_input',
  'awaiting_approval',
  'hook_failed',
])
```

- [ ] **Step 7: Verify the frontend still typechecks and builds**

Run: `cd pkg/web/dashboard && npm run build`
Expected: builds cleanly (this is also what CI's `make generate-check`/build step will run).

- [ ] **Step 8: Run full backend test suite**

Run: `go test ./... `
Expected: PASS (no other test hardcodes the 11-status list — `AllStatuses()`'s only consumer besides the generator is `TestAllStatuses_MatchesConsts` itself).

- [ ] **Step 9: Commit**

```bash
git add pkg/state/state.go pkg/state/state_test.go pkg/web/dashboard/src/types/stage-status.generated.ts pkg/web/dashboard/src/types/stage.ts
git commit -m "feat: добавляем статус стадии paused"
```

---

### Task 2: `StageState.PausedFrom` + `Store.PausedFrom` + `isIdle`

**Files:**
- Modify: `pkg/state/state.go` (`StageState`, `SetStageStatusAt`, `isIdle`)
- Modify: `pkg/state/store.go` (new `Store.PausedFrom` method)
- Test: `pkg/state/state_test.go`

**Interfaces:**
- Consumes: `state.StatusPaused` (Task 1).
- Produces: `StageState.PausedFrom StageStatus` (json `paused_from`), `(*Store).PausedFrom(stageID string) StageStatus` — both used by Task 3 (FSM `GuardCtx`), Task 5 (auto_run gate check), Task 7 (`Continue`).

- [ ] **Step 1: Write the failing tests**

Append to `pkg/state/state_test.go`:

```go
func TestSetStageStatusAt_PausedFromRecordsAndSurvives(t *testing.T) {
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRunning},
	}}

	rs.SetStageStatusAt("a", StatusPaused, time.Now())
	if rs.Stages["a"].PausedFrom != StatusRunning {
		t.Fatalf("PausedFrom = %q, want %q", rs.Stages["a"].PausedFrom, StatusRunning)
	}

	// Continuing (leaving paused) must NOT erase PausedFrom — it doubles as
	// the auto_run one-shot gate marker (see shouldGateAutoRun, Task 5).
	rs.SetStageStatusAt("a", StatusRunning, time.Now())
	if rs.Stages["a"].PausedFrom != StatusRunning {
		t.Errorf("PausedFrom should survive leaving paused, got %q", rs.Stages["a"].PausedFrom)
	}
}

func TestIsIdle_PausedIsIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusPaused},
	}
	if !isIdle(stages) {
		t.Error("want idle=true for a lone paused stage")
	}
}

func TestStore_PausedFrom(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "run"), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if got := store.PausedFrom("a"); got != "" {
		t.Errorf("PausedFrom before any pause = %q, want empty", got)
	}

	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&Transition{StageID: "a", From: StatusRunning, To: StatusPaused, Event: "pause"}); err != nil {
		t.Fatal(err)
	}
	if got := store.PausedFrom("a"); got != StatusRunning {
		t.Errorf("PausedFrom after pause = %q, want %q", got, StatusRunning)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/state/... -run 'TestSetStageStatusAt_PausedFromRecordsAndSurvives|TestIsIdle_PausedIsIdle|TestStore_PausedFrom' -v`
Expected: FAIL to compile (`StageState.PausedFrom` and `Store.PausedFrom` don't exist yet).

- [ ] **Step 3: Add `PausedFrom` to `StageState` and update `SetStageStatusAt`**

In `pkg/state/state.go`:

```go
type StageState struct {
	Status    StageStatus `json:"status"`
	UpdatedAt time.Time   `json:"updated_at"`
	// PausedFrom — статус, из которого стадия ушла в paused. Заполняется один
	// раз, при первом входе в paused, и НЕ очищается на выходе из paused
	// (Continue) — это совмещает две роли: (1) пока стадия в paused — куда
	// резюмиться; (2) после Continue — постоянная метка "эта стадия уже
	// проходила цикл паузы хотя бы раз", которую auto_run-гейт (Task 5)
	// использует, чтобы срабатывать только при самой первой активации.
	PausedFrom StageStatus `json:"paused_from,omitempty"`
}

func (rs *RunState) SetStageStatusAt(stageID string, status StageStatus, t time.Time) {
	pausedFrom := rs.Stages[stageID].PausedFrom
	if status == StatusPaused {
		pausedFrom = rs.Stages[stageID].Status // статус ДО этого перехода
	}
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: t, PausedFrom: pausedFrom}
}
```

- [ ] **Step 4: Add `paused` to `isIdle`**

```go
func isIdle(stages map[string]StageState) bool {
	hasFailed := false
	anyActive := false
	for _, st := range stages {
		switch st.Status {
		case StatusAwaitingUserInput, StatusAwaitingApproval, StatusPaused:
			return true
		case StatusFailed:
			hasFailed = true
		case StatusRunning, StatusPlanning, StatusRevising:
			anyActive = true
		default:
			// pending/ready/retrying/done/hook_failed — не влияют на isIdle.
		}
	}
	return hasFailed && !anyActive
}
```

- [ ] **Step 5: Add `Store.PausedFrom` accessor**

In `pkg/state/store.go`, next to `Get`:

```go
// PausedFrom returns the status a paused stage was paused from (empty if the
// stage has never been paused). See StageState.PausedFrom for why this value
// survives after the stage leaves paused.
func (s *Store) PausedFrom(stageID string) StageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Stages[stageID].PausedFrom
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/state/... -v`
Expected: PASS, including all pre-existing tests in the package.

- [ ] **Step 7: Commit**

```bash
git add pkg/state/state.go pkg/state/store.go pkg/state/state_test.go
git commit -m "feat: добавляем StageState.PausedFrom и Store.PausedFrom"
```

---

### Task 3: FSM `EvPause`/`EvContinue`

**Files:**
- Modify: `pkg/orchestrator/bus/fsm.go`
- Test: `pkg/orchestrator/bus/fsm_test.go`

**Interfaces:**
- Consumes: `state.StatusPaused`, `state.StageStatus` (Tasks 1-2).
- Produces: `bus.EvPause`, `bus.EvContinue` (`FSMEvent` constants), `bus.GuardCtx.PausedFrom state.StageStatus` field — used by Task 5 (gate fires `EvPause`), Task 7 (`Continue` reads `Store.PausedFrom` into `GuardCtx.PausedFrom`), Task 8 (`Pause` fires `EvPause`).

- [ ] **Step 1: Write the failing tests**

Append to `pkg/orchestrator/bus/fsm_test.go`:

```go
func TestFSM_Apply_Pause(t *testing.T) {
	for _, from := range []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: from, Event: "test_setup"})

		to, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "manual pause")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", from, err)
		}
		if !ok || to != state.StatusPaused {
			t.Errorf("%s->paused: got (%v, %v), want (paused, true)", from, to, ok)
		}
	}
}

func TestFSM_Apply_Pause_IllegalFromAwaitingApproval(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"})

	_, _, ok, err := fsm.Apply("a", EvPause, GuardCtx{}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("pause from awaiting_approval: ok = true, want false")
	}
}

func TestFSM_Apply_Continue_ResumesToPausedFrom(t *testing.T) {
	for _, pausedFrom := range []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying, state.StatusPending} {
		fsm, store := newTestFSM(t, []string{"a"})
		_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"})

		to, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: pausedFrom}, "")
		store.Close()
		if err != nil {
			t.Fatalf("%s: Apply: %v", pausedFrom, err)
		}
		if !ok || to != pausedFrom {
			t.Errorf("continue with PausedFrom=%s: got (%v, %v), want (%s, true)", pausedFrom, to, ok, pausedFrom)
		}
	}
}

func TestFSM_Apply_Continue_IllegalFromRunning(t *testing.T) {
	fsm, store := newTestFSM(t, []string{"a"})
	defer store.Close()
	_ = store.Apply(&state.Transition{StageID: "a", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"})

	_, _, ok, err := fsm.Apply("a", EvContinue, GuardCtx{PausedFrom: state.StatusRunning}, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("continue from running (not paused): ok = true, want false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/bus/... -run 'TestFSM_Apply_Pause|TestFSM_Apply_Continue' -v`
Expected: FAIL to compile (`EvPause`/`EvContinue`/`GuardCtx.PausedFrom` don't exist yet).

- [ ] **Step 3: Add the events, `GuardCtx` field, and rules**

In `pkg/orchestrator/bus/fsm.go`:

```go
const (
	EvStartPlanning      FSMEvent = "start_planning"
	EvPlanReady          FSMEvent = "plan_ready"
	EvApprove            FSMEvent = "approve"
	EvRevise             FSMEvent = "revise"
	EvStartRun           FSMEvent = "start_run"
	EvComplete           FSMEvent = "complete"
	EvFail               FSMEvent = "fail"
	EvAskUser            FSMEvent = "ask_user"
	EvUserAnswered       FSMEvent = "user_answered"
	EvScheduleRetry      FSMEvent = "schedule_retry"
	EvResumeAfterRetry   FSMEvent = "resume_after_retry"
	EvManualRetry        FSMEvent = "manual_retry"
	EvBlockedByDep       FSMEvent = "blocked_by_dep"
	EvReady              FSMEvent = "ready"
	EvSupervisorApproved FSMEvent = "supervisor_approved"
	EvHookFailed FSMEvent = "hook_failed"
	EvHookResolved FSMEvent = "hook_resolved"
	// EvPause — стадия приостанавливается: либо auto_run:false не дал ей
	// начать (From включает только живые/ожидающие-повтора статусы, pending
	// обрабатывается отдельно самим гейтом — см. Task 5), либо пользователь
	// вручную поставил на паузу уже бегущую стадию.
	EvPause FSMEvent = "pause"
	// EvContinue возвращает стадию из paused туда, откуда она была
	// приостановлена (ctx.PausedFrom) — реальный перезапуск агента делает
	// вызывающий код (Orchestrator.Continue), а не сама FSM-таблица.
	EvContinue FSMEvent = "continue"
)

type GuardCtx struct {
	Stage flow.Stage
	Phase string
	// PausedFrom используется только EvContinue — вызывающий код читает его
	// заранее из Store.PausedFrom(stageID) (см. Orchestrator.Continue).
	PausedFrom state.StageStatus
}
```

Add two rules to the map literal in `NewFSM`, next to the existing retry/pause-adjacent ones:

```go
EvPause: {
	From: []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying},
	To:   to(state.StatusPaused),
},
EvContinue: {
	From: []state.StageStatus{state.StatusPaused},
	To:   func(ctx GuardCtx) state.StageStatus { return ctx.PausedFrom },
},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/bus/... -v`
Expected: PASS, including all pre-existing FSM tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/bus/fsm.go pkg/orchestrator/bus/fsm_test.go
git commit -m "feat: добавляем FSM-события EvPause/EvContinue"
```

---

### Task 4: `flow.Stage.AutoRun`

**Files:**
- Modify: `pkg/flow/flow.go`
- Test: `pkg/flow/flow_test.go`

**Interfaces:**
- Produces: `Stage.AutoRun *bool` (yaml `auto_run`), `(Stage) AutoRunDisabled() bool` — used by Task 5's `shouldGateAutoRun`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/flow/flow_test.go`:

```go
func TestStage_AutoRunDisabled(t *testing.T) {
	enabled := true
	disabled := false

	if (flow.Stage{}).AutoRunDisabled() {
		t.Error("nil AutoRun should not be disabled (default: enabled)")
	}
	if (flow.Stage{AutoRun: &enabled}).AutoRunDisabled() {
		t.Error("AutoRun=true should not be disabled")
	}
	if !(flow.Stage{AutoRun: &disabled}).AutoRunDisabled() {
		t.Error("AutoRun=false should be disabled")
	}
}

func TestParseFile_AutoRunFalse(t *testing.T) {
	yaml := `
name: test-flow
stages:
  - id: s1
    name: "S1"
    script: "echo hi"
    auto_run: false
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Stages[0].AutoRunDisabled() {
		t.Error("expected auto_run: false to parse as disabled")
	}
}

func TestParseFile_AutoRunOmittedDefaultsEnabled(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].AutoRunDisabled() {
		t.Error("expected omitted auto_run to default to enabled (not disabled)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/flow/... -run 'TestStage_AutoRunDisabled|TestParseFile_AutoRun' -v`
Expected: FAIL to compile (`AutoRun`/`AutoRunDisabled` don't exist yet).

- [ ] **Step 3: Add the field and method**

In `pkg/flow/flow.go`, add to the end of the `Stage` struct (after `AutoApprove`):

```go
	// AutoRun, если явно false, приостанавливает стадию сразу при первой
	// активации (когда её depends_on выполнены) вместо немедленного старта —
	// стадия уходит в paused с PausedFrom=pending и ждёт Continue. nil (не
	// задано) или true — прежнее поведение, немедленный старт. Гейт
	// срабатывает один раз: см. state.StageState.PausedFrom.
	AutoRun *bool `yaml:"auto_run,omitempty"`
}

// AutoRunDisabled reports whether this stage's first activation should pause
// instead of starting immediately (auto_run explicitly set to false).
func (s Stage) AutoRunDisabled() bool {
	return s.AutoRun != nil && !*s.AutoRun
}
```

(Note: `AutoRunDisabled` takes `Stage` by value, not `*Stage`, matching `IsAuto`/`IsScript`'s pointer receivers being used as `(*Stage)` elsewhere but called on value copies throughout the orchestrator, e.g. `s flow.Stage` loop variables in `scheduling.go`/`recovery.go` — a value receiver avoids needing `&s` at every call site.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/flow/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat: добавляем flow.Stage.AutoRun"
```

---

### Task 5: `auto_run` gate wiring

**Files:**
- Modify: `pkg/orchestrator/scheduling.go` (`tryActivatePrePlanned`, `startPlanningForUnblocked`)
- Modify: `pkg/orchestrator/recovery.go` (both `default:` branches in `startPlanningForPending`)
- Test: `pkg/orchestrator/pause_continue_test.go` (new file)

**Interfaces:**
- Consumes: `Stage.AutoRunDisabled()` (Task 4), `Store.PausedFrom` (Task 2), `bus.EvPause` (Task 3).
- Produces: `(o *Orchestrator) shouldGateAutoRun(s flow.Stage) bool` — used nowhere else outside this task, but documents the one-shot invariant later tasks rely on.

- [ ] **Step 1: Write the failing integration tests**

Create `pkg/orchestrator/pause_continue_test.go`:

```go
package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_AutoRunGatesScriptStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].PausedFrom != state.StatusPending {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["s1"].PausedFrom, state.StatusPending)
	}
}

func TestIntegration_AutoRunGatesRegularStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	// No Runner injected: the gate must fire before any agent would ever be
	// spawned. If it doesn't, orch.Run would hang on a nil Runner and this
	// test would time out instead of failing cleanly on the wrong status.
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)
}

// TestIntegration_AutoRunGateFiresOnceOnly locks in the "только при первой
// активации" requirement: a stage that already completed one pause/continue
// cycle (PausedFrom permanently non-empty, see state.SetStageStatusAt) must
// not re-pause when a later failure sends it back through Pending.
func TestIntegration_AutoRunGateFiresOnceOnly(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPaused, To: state.StatusPending, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (gate must not re-fire on second pending pass), got %v", final.Stages["s1"].Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_AutoRunGates -v`
Expected: FAIL — stages start immediately (script stage reaches `done`, regular stage hangs waiting for a nil Runner until the 5s context deadline) because the gate doesn't exist yet.

- [ ] **Step 3: Add `shouldGateAutoRun`**

In `pkg/orchestrator/scheduling.go`, next to `activateAutoStage`:

```go
// shouldGateAutoRun reports whether s's first activation should pause
// instead of proceeding: auto_run is explicitly false AND this stage has
// never been through a pause cycle before (PausedFrom is the permanent
// marker — see state.StageState.PausedFrom). Without the second condition a
// stage retried after failing (which re-enters Pending via EvManualRetry)
// would re-pause on every retry instead of only the very first activation.
func (o *Orchestrator) shouldGateAutoRun(s flow.Stage) bool {
	return s.AutoRunDisabled() && o.opts.Store.PausedFrom(s.ID) == ""
}
```

- [ ] **Step 4: Wire the gate into `tryActivatePrePlanned`**

In `pkg/orchestrator/scheduling.go`, `tryActivatePrePlanned`, insert right before the `activateAutoStage`/`activateScriptStage` calls:

```go
		if !o.depsDone(s) {
			continue
		}

		if o.shouldGateAutoRun(s) {
			o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
			continue
		}

		if o.activateAutoStage(s) {
			continue
		}
		if o.activateScriptStage(s) {
			continue
		}
```

- [ ] **Step 5: Wire the gate into `startPlanningForUnblocked`**

In `pkg/orchestrator/scheduling.go`, `startPlanningForUnblocked`, insert right before the `EvStartPlanning` trigger:

```go
		if !o.depsDone(s) {
			continue
		}
		if o.shouldGateAutoRun(s) {
			o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
			continue
		}
		// Synchronous transition out of pending guards against double
		// start: a second call sees "planning" and skips the stage.
		if _, ok := o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{Stage: s}, "deps done"); !ok {
			continue
		}
		o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
```

- [ ] **Step 6: Wire the gate into `recovery.go`'s first (`!NeedsPlanning()`) `default:` branch**

In `pkg/orchestrator/recovery.go`, `startPlanningForPending`'s first switch (`if !s.NeedsPlanning() { switch current { ... default: ...`), insert right after the existing `depsDone` check, before `activateAutoStage`:

```go
			default:
				if !o.depsDone(s) {
					continue
				}

				if o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
					continue
				}

				if o.activateAutoStage(s) {
					continue
				}
```

- [ ] **Step 7: Wire the gate into `recovery.go`'s second `default:` branch**

Same file, the second (unconditional) `switch current { ... default: ...` branch — insert right after the existing `depsDone` guard, before the `EvStartPlanning` trigger:

```go
			default:
				// Pending, planning, or unknown — check if planning already completed
				if s.NeedsPlanning() {
					stageDir := filepath.Join(o.opts.RunDir, s.ID)
					if stagefiles.CheckPlanCompletion(stageDir) == nil {
						o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
						o.autoApproveIfConfigured(ctx, s)
						continue
					}
				}
				// Pending stages wait for depends_on unless eager_planning is set.
				// Interrupted planning (status "planning") always resumes.
				if current == state.StatusPending && !s.EagerPlanning && !o.depsDone(s) {
					continue
				}
				if current == state.StatusPending && o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
					continue
				}
				o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
				o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
```

(The `current == state.StatusPending` guard matters here: this `default:` is also reached by a stage resuming from an interrupted `planning` status after an afm restart — that is Task 6/8's territory, not the auto_run gate, which must only ever fire for a genuinely fresh activation.)

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -v`
Expected: PASS, including the three new tests and the entire pre-existing suite (this touches four shared activation call sites — a regression here would show up broadly).

- [ ] **Step 9: Run `make lint`**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 10: Commit**

```bash
git add pkg/orchestrator/scheduling.go pkg/orchestrator/recovery.go pkg/orchestrator/pause_continue_test.go
git commit -m "feat: гейтим первую активацию стадии по auto_run: false"
```

---

### Task 6: `recovery.go` refactor — `resumePlanningStage`/`resumeStageAtStatus` + `paused` no-op

**Files:**
- Modify: `pkg/orchestrator/recovery.go`
- Test: `pkg/orchestrator/pause_continue_test.go`

**Interfaces:**
- Produces: `(o *Orchestrator) resumePlanningStage(ctx context.Context, s flow.Stage)`, `(o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus)` — both consumed by Task 7's `Continue` (this is the "simulate what afm-restart-recovery already does for this status" dispatcher the spec calls for).
- This task is a **behavior-preserving refactor** of the existing `startPlanningForPending` switch bodies — no new runtime behavior for the restart path, verified by keeping the full existing suite green throughout.

- [ ] **Step 1: Run the full existing suite to record the green baseline**

Run: `go test ./pkg/orchestrator/... -v`
Expected: PASS (this is the baseline the refactor must not break).

- [ ] **Step 2: Write the failing test for the new `paused` no-op behavior**

Append to `pkg/orchestrator/pause_continue_test.go`:

```go
// TestIntegration_ResumeLeavesPausedStageUntouched — afm restarting must not
// auto-resume a paused stage; only an explicit Continue may.
func TestIntegration_ResumeLeavesPausedStageUntouched(t *testing.T) {
	stages := []flow.Stage{
		{ID: "paused-stage", Name: "Paused", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"paused-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = orch.Run(ctx) // expected to idle out on the ctx deadline — nothing else to complete

	final := loadStateJSON(t, stateFile)
	if final.Stages["paused-stage"].Status != state.StatusPaused {
		t.Errorf("expected paused stage to remain untouched by recovery, got %v", final.Stages["paused-stage"].Status)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_ResumeLeavesPausedStageUntouched -v`
Expected: FAIL — today's `switch current { ... default: }` treats an unrecognized status (including `paused`, once it exists) as a fresh-activation candidate.

- [ ] **Step 4: Add `paused` as a no-op in both switches**

In `pkg/orchestrator/recovery.go`, first switch:

```go
			switch current {
			case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusPaused:
				continue
```

Second switch:

```go
		switch current {
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady, state.StatusPaused:
			continue
```

- [ ] **Step 5: Run the new test to verify it passes**

Run: `go test ./pkg/orchestrator/... -run TestIntegration_ResumeLeavesPausedStageUntouched -v`
Expected: PASS

- [ ] **Step 6: Extract `resumePlanningStage`**

In `pkg/orchestrator/recovery.go`, add this function (near `detectInterruptedPhase`):

```go
// resumePlanningStage (re)starts planning for a stage whose recorded status
// says planning should be in progress (or complete on disk) — used both by
// startPlanningForPending's default branch (afm restarted mid-planning) and
// by resumeStageAtStatus below (Continue after a manual pause during
// planning).
func (o *Orchestrator) resumePlanningStage(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if stagefiles.CheckPlanCompletion(stageDir) == nil {
		o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
		o.autoApproveIfConfigured(ctx, s)
		return
	}
	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
}
```

Then simplify the second switch's `default:` branch to use it (behavior identical — `s.NeedsPlanning()` is always true here, since a `!NeedsPlanning()` stage never reaches this second switch in `Pending`/`Planning` status, it's always disposed of by the first switch's own `default:`):

```go
		default:
			stageDir := filepath.Join(o.opts.RunDir, s.ID)
			if stagefiles.CheckPlanCompletion(stageDir) == nil {
				o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
				o.autoApproveIfConfigured(ctx, s)
				continue
			}
			if current == state.StatusPending {
				if !s.EagerPlanning && !o.depsDone(s) {
					continue
				}
				if o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
					continue
				}
			}
			o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
			o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
```

(This keeps the exact same triggers as Task 5 wired in, just deduplicating the `CheckPlanCompletion` check so it isn't repeated inside `resumePlanningStage` a second time — `resumePlanningStage` is called separately below, from `resumeStageAtStatus`, not from here directly, since here the `current == state.StatusPending` gate logic must run in between.)

- [ ] **Step 7: Extract `resumeStageAtStatus`**

Add this function right after `resumePlanningStage`:

```go
// resumeStageAtStatus (re)spawns whatever goroutine a stage recorded as
// running/planning/revising/retrying needs to make progress again — used at
// afm startup (startPlanningForPending, when the recorded status survived a
// crash) and by Continue (Task 7, when a user resumes a stage from paused).
// Both situations reduce to the same question: "the process this status
// implies isn't running right now — start it."
func (o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	switch status {
	case state.StatusPlanning:
		o.resumePlanningStage(ctx, s)
	case state.StatusRetrying:
		if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
			o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
			o.maybeRunAfterHook(ctx, s.ID)
			return
		}
		if stagefiles.CheckPlanCompletion(stageDir) == nil && s.NeedsPlanning() {
			o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
			o.autoApproveIfConfigured(ctx, s)
			return
		}
		o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "restart after retry")
		o.concurrency.SpawnAgent(ctx, s, o.runPlanningAgent)
	case state.StatusRevising:
		switch o.detectInterruptedPhase(stageDir) {
		case phaseImplementation:
			o.concurrency.SpawnAgent(ctx, s, o.runImplementationWithFeedback)
		case phaseReview:
			o.concurrency.SpawnAgent(ctx, s, o.runReviewWithFeedback)
		case phaseAutonomous:
			o.concurrency.SpawnAgent(ctx, s, o.runAutonomousWithFeedback)
		default:
			o.concurrency.SpawnAgent(ctx, s, o.runPlanningWithFeedback)
		}
	case state.StatusRunning:
		if s.IsScript() {
			if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
				o.maybeRunAfterHook(ctx, s.ID)
				return
			}
			o.concurrency.SpawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
			return
		}
		if isAutonomousStage(stageDir) || s.IsAuto() {
			if stagefiles.CheckAutonomousCompletion(stageDir) == nil {
				o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered execution_summary.md")
				o.maybeRunAfterHook(ctx, s.ID)
				return
			}
			o.concurrency.SpawnAgent(ctx, s, o.runAutonomousAgent)
			return
		}
		if err := stagefiles.CheckCompletion(stageDir, ".", s); err == nil {
			o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
			o.maybeRunAfterHook(ctx, s.ID)
			return
		}
		o.concurrency.SpawnAgent(ctx, s, o.runImplementationAgent)
	}
}
```

Now replace the second switch's `case state.StatusRetrying:`, `case state.StatusRevising:`, and `case state.StatusRunning:` bodies with single calls:

```go
		case state.StatusRetrying:
			o.resumeStageAtStatus(ctx, s, state.StatusRetrying)
		case state.StatusRevising:
			o.resumeStageAtStatus(ctx, s, state.StatusRevising)
		case state.StatusRunning:
			o.resumeStageAtStatus(ctx, s, state.StatusRunning)
```

- [ ] **Step 8: Run the full suite to confirm the refactor changed nothing observable**

Run: `go test ./pkg/orchestrator/... -v`
Expected: PASS — every pre-existing recovery/retry/revise integration test (`TestIntegration_ResumeFromRevising`, `TestIntegration_RetryOnServerError`, etc.) still passes unchanged, plus the two new tests from this task and Task 5.

- [ ] **Step 9: `make lint`**

Run: `make lint`
Expected: 0 issues (in particular, no unused-import warnings — `resumePlanningStage`/`resumeStageAtStatus` must still use every import the original inline code used).

- [ ] **Step 10: Commit**

```bash
git add pkg/orchestrator/recovery.go pkg/orchestrator/pause_continue_test.go
git commit -m "refactor: выносим resumeStageAtStatus из recovery.go, добавляем no-op для paused"
```

---

### Task 7: `Orchestrator.Continue`

**Files:**
- Modify: `pkg/orchestrator/control_api.go`
- Test: `pkg/orchestrator/pause_continue_test.go`

**Interfaces:**
- Consumes: `bus.EvContinue`, `GuardCtx.PausedFrom` (Task 3), `Store.PausedFrom` (Task 2), `resumeStageAtStatus` (Task 6), `tryActivatePrePlanned`/`startPlanningForUnblocked` (existing, Task 5-gated).
- Produces: `(o *Orchestrator) Continue(reqCtx context.Context, stageID string) error` — consumed by Task 10 (HTTP handler) via the widened `StageActions` interface.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/orchestrator/pause_continue_test.go`:

```go
// TestContinue_FromPending_StartsScriptStage covers cases 1/3: a stage
// gated by auto_run:false (never actually started — PausedFrom=pending) is
// resumed by Continue exactly like a normal first activation.
func TestContinue_FromPending_StartsScriptStage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// The gate never fired live (status was seeded directly into paused) —
	// nothing to wait for before calling Continue.
	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)
}

// TestContinue_FromRevising_ResumesWithFeedback covers case 2: a stage
// paused mid-revision resumes via resumeStageAtStatus, exactly like an afm
// restart would.
func TestContinue_FromRevising_ResumesWithFeedback(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "revise-stuck")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.v1.md"), []byte("# Plan v1\n\nold content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("please add error handling for edge case X"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "revise-stuck", Name: "Revise Stuck", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	store, err := state.Open(runDir, []string{"revise-stuck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusRevising, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	capture := &capturingPlanningRunner{delegate: mockRunner(t, mockPlanningScript)}
	runner := &doneCreatingRunner{delegate: capture}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ctxCancel()
	go func() { _ = orch.Run(ctx) }()

	if err := orch.Continue(ctx, "revise-stuck"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "revise-stuck", state.StatusDone, 8*time.Second)

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "please add error handling for edge case X") {
		t.Fatalf("expected planning prompt to include feedback.md content, got: %v", prompts)
	}
}

func TestContinue_NotPaused_IsNoOp(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)

	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue on a non-paused stage should be a no-op, got error: %v", err)
	}
	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("Continue on a done stage must not change its status, got %v", final.Stages["s1"].Status)
	}
}
```

`capturingPlanningRunner` (`integration_resume_test.go:186`) and `doneCreatingRunner` (`integration_test.go:149`) already exist in package `orchestrator_test` — no new definitions needed, Go test files in the same package share top-level declarations.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/... -run TestContinue -v`
Expected: FAIL to compile (`Orchestrator.Continue` doesn't exist yet).

- [ ] **Step 3: Implement `Continue`**

In `pkg/orchestrator/control_api.go`, add next to `Revise`:

```go
// Continue resumes a paused stage: for PausedFrom==pending (auto_run:false
// gated the stage before it ever started, or a script stage's only pause
// point) it's exactly a normal first activation; otherwise it's exactly what
// afm-restart recovery already does for a stage recorded as
// running/planning/revising/retrying (resumeStageAtStatus, Task 6) — a
// manually paused stage and a crashed-and-restarted one are, from the
// scheduler's point of view, the same situation: "the process implied by
// this status isn't running right now."
func (o *Orchestrator) Continue(reqCtx context.Context, stageID string) error {
	if o.currentStatus(stageID) != state.StatusPaused {
		return nil
	}
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	pausedFrom := o.opts.Store.PausedFrom(stageID)
	to, ok := o.Trigger(stageID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "")
	if !ok {
		return nil
	}

	ctx := o.runContext(reqCtx) // не reqCtx — иначе HTTP-хендлер убьёт агента при возврате ответа
	if pausedFrom == state.StatusPending {
		o.tryActivatePrePlanned(ctx)
		o.startPlanningForUnblocked(ctx)
		return nil
	}

	o.resumeStageAtStatus(ctx, *stage, to)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -v`
Expected: PASS, including the whole pre-existing suite.

- [ ] **Step 5: `make lint`**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/pause_continue_test.go
git commit -m "feat: добавляем Orchestrator.Continue"
```

---

### Task 8: `Orchestrator.Pause` + `runWithRetry` interrupt handling

**Files:**
- Modify: `pkg/orchestrator/control_api.go`
- Modify: `pkg/orchestrator/retry.go`
- Test: `pkg/orchestrator/pause_continue_test.go`

**Interfaces:**
- Consumes: `bus.EvPause` (Task 3), `interruptChans` (existing), `executor.ErrUserInterrupted` (existing).
- Produces: `(o *Orchestrator) Pause(reqCtx context.Context, stageID string) error` — consumed by Task 10 (HTTP handler).

- [ ] **Step 1: Write the failing tests**

Append to `pkg/orchestrator/pause_continue_test.go`:

```go
// blockingRunner: RunPlanning writes a minimally valid plan.md so the stage
// reaches running (headless auto-approve carries it from
// awaiting_approval->ready->running); RunAgent blocks on the same
// interruptChans channel Revise() already uses for a running stage (see
// orchestrator.InterruptChanForTest, used identically in
// agent_suggest_test.go's blockingThenFeedbackRunner) and returns
// executor.ErrUserInterrupted when signaled — simulating what a real
// executor does after SIGINT.
type blockingRunner struct {
	orch    *orchestrator.Orchestrator
	stageID string
}

func (r *blockingRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *blockingRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	ch, ok := orchestrator.InterruptChanForTest(r.orch, r.stageID)
	if !ok {
		<-ctx.Done()
		return context.Canceled
	}
	select {
	case <-ch:
		return executor.ErrUserInterrupted
	case <-ctx.Done():
		return context.Canceled
	}
}

func (r *blockingRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingRunner)(nil)

func TestPause_RunningStage_StopsAgentAndTransitionsToPaused(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "impl", Name: "Impl", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingRunner{stageID: "impl"}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	if err := orch.Pause(ctx, "impl"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusPaused, 5*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl"].PausedFrom != state.StatusRunning {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["impl"].PausedFrom, state.StatusRunning)
	}
}

// alwaysRetryableAgentRunner: planning succeeds via delegate; every RunAgent
// call fails with a retryable error, keeping the stage in the
// running->retrying backoff loop so the test can Pause it mid-backoff.
type alwaysRetryableAgentRunner struct {
	delegate executor.Runner
}

func (r *alwaysRetryableAgentRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *alwaysRetryableAgentRunner) RunAgent(_ context.Context, _, _, _, _ string) error {
	return errors.New("rate limit exceeded")
}

func (r *alwaysRetryableAgentRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

var _ executor.Runner = (*alwaysRetryableAgentRunner)(nil)

func TestPause_RetryingStage_CancelsBackoffImmediately(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 30 * time.Second
	orchestrator.MaxRetries = 15
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

	stages := []flow.Stage{
		{ID: "impl", Name: "Impl", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &alwaysRetryableAgentRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer ctxCancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "impl", state.StatusRetrying, 10*time.Second)

	if err := orch.Pause(ctx, "impl"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// RetryBackoff is 30s — if Pause didn't cancel the backoff wait, this
	// would time out instead of passing quickly.
	waitForStatus(t, stateFile, "impl", state.StatusPaused, 3*time.Second)
}

func TestPause_AwaitingApproval_IsNoOp(t *testing.T) {
	srv := setupPauseNoOpOrchestrator(t) // see helper below
	if err := srv.orch.Pause(context.Background(), srv.stageID); err != nil {
		t.Fatalf("Pause on awaiting_approval should be a no-op, got error: %v", err)
	}
	if got := srv.store.Get(srv.stageID); got != state.StatusAwaitingApproval {
		t.Errorf("status changed to %v, want unchanged awaiting_approval", got)
	}
}

type pauseNoOpFixture struct {
	orch    *orchestrator.Orchestrator
	store   *state.Store
	stageID string
}

func setupPauseNoOpOrchestrator(t *testing.T) pauseNoOpFixture {
	t.Helper()
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning}}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})
	return pauseNoOpFixture{orch: orch, store: store, stageID: "s1"}
}
```

Check for a naming collision: if `orchestrator_test` already defines `mockPlanningScript`/`mockRunner`/`setupOrchestratorWithRunner`/`autoApprove`/`waitForStatus`/`loadStateJSON` in `integration_test.go` (confirmed present), do not redefine them here — this task only adds `blockingRunner`, `alwaysRetryableAgentRunner`, and the four new test functions.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/orchestrator/... -run TestPause -v`
Expected: FAIL to compile (`Orchestrator.Pause` doesn't exist yet).

- [ ] **Step 3: Implement `Pause`**

In `pkg/orchestrator/control_api.go`, next to `Revise`:

```go
// Pause synchronously transitions a stage to paused and, if it has a live
// agent or is waiting out a retry backoff, signals the same interruptChans
// channel Revise() already uses for a running stage. The only difference
// from Revise is what runWithRetry does when it wakes up: Revise restarts
// with feedback, Pause doesn't restart anything — the durable transition to
// paused already happened here, synchronously, before the signal was sent.
func (o *Orchestrator) Pause(_ context.Context, stageID string) error {
	switch o.currentStatus(stageID) {
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
	default:
		return nil
	}
	if stage := o.graph.Stage(stageID); stage != nil && stage.IsScript() && o.currentStatus(stageID) == state.StatusRunning {
		return nil // mid-script pause не поддержан — RunScript не принимает InterruptCh
	}

	if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		return nil
	}
	if ch, ok := o.interruptChans.Load(stageID); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
		default: // уже сигнализирован — не блокируемся
		}
	}
	return nil
}
```

- [ ] **Step 4: Update `runWithRetry`'s `ErrUserInterrupted` branch**

In `pkg/orchestrator/retry.go`, `runWithRetry`, change:

```go
		if errors.Is(err, executor.ErrUserInterrupted) {
			onUserInterrupted()
			return
		}
```

to:

```go
		if errors.Is(err, executor.ErrUserInterrupted) {
			if o.currentStatus(s.ID) == state.StatusPaused {
				return // Pause() already recorded the durable transition — nothing to restart
			}
			onUserInterrupted()
			return
		}
```

(`retry.go` already imports `"github.com/akopichin/afm/pkg/state"` — no import changes needed.)

- [ ] **Step 5: Add the backoff-wait interrupt case**

Still in `runWithRetry`, the retry-backoff `select`:

```go
			select {
			case <-time.After(retryBackoff):
			case <-interruptCh:
				return // Pause() already transitioned to paused — nothing to resume here
			case <-ctx.Done():
				o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "cancelled during retry")
				o.failBlockedStages()
				return
			}
```

(`Revise` cannot reach a `retrying` stage — its precondition is only `awaiting_approval`/`running` — so a signal on `interruptCh` during this specific wait can only ever come from `Pause`; no status check is needed here, unlike the `ErrUserInterrupted` branch above which is reachable from both Pause and Revise.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/... -v`
Expected: PASS, including the whole pre-existing suite — in particular `TestAgentSuggest_InterruptRestartsWithFeedback` must still pass (Revise's path is untouched: it never puts the stage in `StatusPaused`, so the new status check in Step 4 is always false for it).

- [ ] **Step 7: `make lint`**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 8: Commit**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/retry.go pkg/orchestrator/pause_continue_test.go
git commit -m "feat: добавляем Orchestrator.Pause и обработку ручной паузы в runWithRetry"
```

---

### Task 9: `StageView.IsScript`/`PausedFrom` + `stageIsScript` wiring

**Files:**
- Modify: `pkg/server/stageview.go`
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/handlers.go` (the `buildStageViews` call site)
- Modify: `cmd/afm/run.go`
- Test: `pkg/server/stageview_test.go`

**Interfaces:**
- Produces: `StageView.IsScript bool` (json `is_script`), `StageView.PausedFrom state.StageStatus` (json `paused_from,omitempty`), `Server.stageIsScript map[string]bool`, `Config.StageIsScript map[string]bool` — consumed by Task 10 (the `/pause` handler's script+running 409 check) and Task 11 (frontend `Stage.isScript`/`pausedFrom`).

- [ ] **Step 1: Write the failing test**

Update the existing call site and add a dedicated test in `pkg/server/stageview_test.go`. First, fix the existing test's call (it will fail to compile once the signature changes in Step 3):

```go
	views := buildStageViews(rs, runDir, map[string]bool{"a": true}, map[string]bool{"a": true}, map[string]bool{"a": false}, nil)
```

Then append:

```go
func TestBuildStageViews_IsScriptAndPausedFrom(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	rs := state.RunState{
		StageOrder: []string{"a", "b"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusPaused, PausedFrom: state.StatusRunning},
			"b": {Status: state.StatusRunning}, // never paused — PausedFrom must not leak into the view
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, map[string]bool{"a": true}, nil)

	a, b := views[0], views[1]
	if !a.IsScript {
		t.Error("stage a: IsScript = false, want true")
	}
	if a.PausedFrom != state.StatusRunning {
		t.Errorf("stage a: PausedFrom = %q, want %q", a.PausedFrom, state.StatusRunning)
	}
	if b.IsScript {
		t.Error("stage b: IsScript = true, want false")
	}
	if b.PausedFrom != "" {
		t.Errorf("stage b: PausedFrom = %q, want empty (never paused)", b.PausedFrom)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/... -run TestBuildStageViews -v`
Expected: FAIL to compile (`buildStageViews` doesn't accept a 5th map param yet, `StageView` has no `IsScript`/`PausedFrom` fields).

- [ ] **Step 3: Extend `StageView` and `buildStageViews`**

In `pkg/server/stageview.go`:

```go
type StageView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Status      state.StageStatus `json:"status"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Interactive bool              `json:"interactive"`
	Autonomous  bool              `json:"autonomous"`
	AutoApprove bool              `json:"auto_approve"`
	HasDialog   bool              `json:"has_dialog"`
	// IsScript — статический конфиг флоу (Stage.IsScript()): нужен фронту,
	// чтобы скрывать пункт "Pause" в кебаб-меню, пока скриптовая стадия
	// реально выполняется (mid-script graceful stop не поддержан).
	IsScript bool `json:"is_script"`
	// PausedFrom заполняется только когда Status == paused (см.
	// state.StageState.PausedFrom, которое, в отличие от этого поля,
	// остаётся непустым и после Continue) — панель паузы в дашборде решает
	// по нему, какой текст показать.
	PausedFrom state.StageStatus `json:"paused_from,omitempty"`
	ShowPlan   bool              `json:"show_plan"`
	ShowDialog bool              `json:"show_dialog"`
}

func buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove, stageIsScript map[string]bool, dependsOn map[string][]string) []StageView {
	order := topoOrder(rs.StageOrder, dependsOn)
	views := make([]StageView, 0, len(order))
	for _, id := range order {
		st := rs.Stages[id]
		autonomous := stageIsAutonomous(runDir, id)
		hasDialog := stageHasDialog(runDir, id)
		interactive := stageInteractive[id]
		showPlan := !autonomous || st.Status == state.StatusFailed
		showDialog := interactive || autonomous || hasDialog
		pausedFrom := state.StageStatus("")
		if st.Status == state.StatusPaused {
			pausedFrom = st.PausedFrom
		}

		views = append(views, StageView{
			ID:          id,
			Name:        rs.StageNames[id],
			Status:      st.Status,
			UpdatedAt:   st.UpdatedAt,
			Interactive: interactive,
			Autonomous:  autonomous,
			AutoApprove: stageAutoApprove[id],
			HasDialog:   hasDialog,
			IsScript:    stageIsScript[id],
			PausedFrom:  pausedFrom,
			ShowPlan:    showPlan,
			ShowDialog:  showDialog,
		})
	}
	return views
}
```

- [ ] **Step 4: Update the call site in `handlers.go`**

In `pkg/server/handlers.go`:

```go
		Stages:               buildStageViews(rs, s.runDir, s.stageInteractive, s.stageAutoApprove, s.stageIsScript, s.stageDependsOn),
```

- [ ] **Step 5: Add `stageIsScript` to `Server`/`Config`**

In `pkg/server/server.go`:

```go
type Server struct {
	runDir           string
	Description      string
	stageInteractive map[string]bool
	stageAutoApprove map[string]bool
	stageIsScript    map[string]bool     // id стадии → IsScript() (статический конфиг флоу)
	stageDependsOn   map[string][]string
	...
```

```go
type Config struct {
	Port             int
	RunDir           string
	Description      string
	StageInteractive map[string]bool
	StageAutoApprove map[string]bool
	StageIsScript    map[string]bool
	StageDependsOn   map[string][]string
	...
```

```go
	s := &Server{
		runDir:           cfg.RunDir,
		Description:      cfg.Description,
		stageInteractive: cfg.StageInteractive,
		stageAutoApprove: cfg.StageAutoApprove,
		stageIsScript:    cfg.StageIsScript,
		stageDependsOn:   cfg.StageDependsOn,
		...
```

- [ ] **Step 6: Wire it from `cmd/afm/run.go`**

```go
				stageInteractive := make(map[string]bool, len(f.Stages))
				stageAutoApprove := make(map[string]bool, len(f.Stages))
				stageIsScript := make(map[string]bool, len(f.Stages))
				stageDependsOn := make(map[string][]string, len(f.Stages))
				for _, st := range f.Stages {
					stageInteractive[st.ID] = st.Interactive
					stageAutoApprove[st.ID] = st.AutoApprove
					stageIsScript[st.ID] = st.IsScript()
					stageDependsOn[st.ID] = st.DependsOn
				}
				srv = server.New(server.Config{
					Port:             cfg.Server.GetPort(),
					RunDir:           runDir,
					Description:      f.Description,
					StageInteractive: stageInteractive,
					StageAutoApprove: stageAutoApprove,
					StageIsScript:    stageIsScript,
					StageDependsOn:   stageDependsOn,
					Store:            store,
					Theme:            cfg.EffectiveTheme(),
					SkinDir:          cfg.SkinDir,
					UIBus:            orch.UIBus(),
					Actions:          orch,
					Secondary:        orch,
				})
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./pkg/server/... ./cmd/... -v`
Expected: PASS

- [ ] **Step 8: `make lint`**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 9: Commit**

```bash
git add pkg/server/stageview.go pkg/server/server.go pkg/server/handlers.go pkg/server/stageview_test.go cmd/afm/run.go
git commit -m "feat: отдаём is_script и paused_from в StageView"
```

---

### Task 10: HTTP `/pause`/`/continue`

**Files:**
- Modify: `pkg/server/actions.go` (`StageActions` interface)
- Modify: `pkg/server/handlers.go` (new handlers)
- Modify: `pkg/server/server.go` (`routeStages`)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `Orchestrator.Pause`/`Orchestrator.Continue` (Tasks 7-8, already satisfy the widened interface since `*Orchestrator` is passed as `cfg.Actions` in `cmd/afm/run.go`), `Server.stageIsScript` (Task 9).
- Produces: `POST /api/stages/{id}/pause`, `POST /api/stages/{id}/continue`.

- [ ] **Step 1: Write the failing tests**

In `pkg/server/handlers_test.go`, extend `fakeStageActions`:

```go
type fakeStageActions struct {
	approve  func(ctx context.Context, stageID string) error
	revise   func(ctx context.Context, stageID, feedback string) error
	retry    func(ctx context.Context, stageID string) error
	pause    func(ctx context.Context, stageID string) error
	continue_ func(ctx context.Context, stageID string) error
}
```

(Go reserves `continue` as a keyword — the struct field is named `continue_`; the interface method itself is `Continue`, which is fine since it's a method name, not a field.)

```go
func (f fakeStageActions) Pause(ctx context.Context, stageID string) error {
	if f.pause == nil {
		return nil
	}
	return f.pause(ctx, stageID)
}

func (f fakeStageActions) Continue(ctx context.Context, stageID string) error {
	if f.continue_ == nil {
		return nil
	}
	return f.continue_(ctx, stageID)
}
```

Then add:

```go
func TestHandlePause(t *testing.T) {
	var pausedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.actions = fakeStageActions{pause: func(ctx context.Context, id string) error { pausedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if pausedID != testStageID {
		t.Errorf("pause not called with %s, got %q", testStageID, pausedID)
	}
}

func TestHandlePause_WrongStatus(t *testing.T) {
	srv, _ := setupTestServer(t) // seeded at awaiting_approval by setupTestServer itself
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandlePause_ScriptStageRunning_Returns409(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.stageIsScript = map[string]bool{testStageID: true}
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/pause", nil)
	w := httptest.NewRecorder()
	srv.handlePause(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
}

func TestHandleContinue(t *testing.T) {
	var continuedID string
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	srv.actions = fakeStageActions{continue_: func(ctx context.Context, id string) error { continuedID = id; return nil }}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil)
	w := httptest.NewRecorder()
	srv.handleContinue(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if continuedID != testStageID {
		t.Errorf("continue not called with %s, got %q", testStageID, continuedID)
	}
}

func TestHandleContinue_NotPaused(t *testing.T) {
	srv, _ := setupTestServer(t) // seeded at awaiting_approval, not paused
	srv.actions = fakeStageActions{}

	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/continue", nil)
	w := httptest.NewRecorder()
	srv.handleContinue(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/server/... -run 'TestHandlePause|TestHandleContinue' -v`
Expected: FAIL to compile.

- [ ] **Step 3: Widen `StageActions`**

In `pkg/server/actions.go`:

```go
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	Revise(ctx context.Context, stageID, feedback string) error
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
}
```

- [ ] **Step 4: Add the handlers**

In `pkg/server/handlers.go`, next to `handleRetry`:

```go
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/pause")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	rs := s.store.Snapshot()
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	switch st.Status {
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
	default:
		http.Error(w, fmt.Sprintf("stage is %s, cannot be paused", st.Status), http.StatusBadRequest)
		return
	}
	if s.stageIsScript[stageID] && st.Status == state.StatusRunning {
		http.Error(w, "pause is not supported mid-script execution", http.StatusConflict)
		return
	}

	if err := s.actions.Pause(r.Context(), stageID); err != nil {
		http.Error(w, "pause failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "paused", keyStageID: stageID})
}

func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/continue")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	rs := s.store.Snapshot()
	st, ok := rs.Stages[stageID]
	if !ok {
		http.Error(w, "stage not found", http.StatusNotFound)
		return
	}
	if st.Status != state.StatusPaused {
		http.Error(w, fmt.Sprintf("stage is %s, not paused", st.Status), http.StatusBadRequest)
		return
	}

	if err := s.actions.Continue(r.Context(), stageID); err != nil {
		http.Error(w, "continue failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "continued", keyStageID: stageID})
}
```

- [ ] **Step 5: Wire the routes**

In `pkg/server/server.go`'s `routeStages`:

```go
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	case strings.HasSuffix(path, "/pause") && r.Method == http.MethodPost:
		s.handlePause(w, r)
	case strings.HasSuffix(path, "/continue") && r.Method == http.MethodPost:
		s.handleContinue(w, r)
	case strings.HasSuffix(path, "/retry-hook") && r.Method == http.MethodPost:
		s.handleRetryHook(w, r)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/server/... -v`
Expected: PASS, including the whole pre-existing suite (`cmd/afm` already passes `orch` — a real `*Orchestrator` — as `Actions`, so it automatically satisfies the widened interface once Tasks 7-8 landed; no other production caller of `StageActions` exists per `pkg/server/actions.go`'s own doc comment).

- [ ] **Step 7: `make lint`**

Run: `make lint`
Expected: 0 issues.

- [ ] **Step 8: Commit**

```bash
git add pkg/server/actions.go pkg/server/handlers.go pkg/server/server.go pkg/server/handlers_test.go
git commit -m "feat: добавляем HTTP /pause и /continue"
```

---

### Task 11: Frontend `Stage` type — `isScript`/`pausedFrom`

**Files:**
- Modify: `pkg/web/dashboard/src/types/stage.ts`
- Modify every existing test file that constructs a `Stage` object literal (they all specify every field, per Task 8's research note) — grep for `interactive: false` across `pkg/web/dashboard/src` test files and add the two new fields to each literal.

**Interfaces:**
- Consumes: `is_script`/`paused_from` JSON fields (Task 9).
- Produces: `Stage.isScript: boolean`, `Stage.pausedFrom: StageStatus | ''` — consumed by Task 13 (kebab menu) and Task 14 (paused panel).

- [ ] **Step 1: Find every existing `Stage` literal that needs updating**

Run: `grep -rln "interactive: false" pkg/web/dashboard/src`
Expected: a list of test files including at least `StagesList.test.tsx` (raw object literals, several per file) and `plan-panel/PlanPanel.test.tsx` (one `makeStage(overrides: Partial<Stage> = {}) { return { ..., showPlan: true, showDialog: false, ...overrides } }` helper — add `isScript: false, pausedFrom: '',` to that base object once, not per test-call, since every test in that file goes through the helper).

- [ ] **Step 2: Update the type**

In `pkg/web/dashboard/src/types/stage.ts`:

```ts
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
  autoApprove: boolean
  hasDialog: boolean
  showPlan: boolean
  showDialog: boolean
  // isScript: true, если стадия скриптовая (Stage.IsScript() в Go) — кебаб-
  // меню скрывает пункт "Pause", пока такая стадия реально выполняется (нет
  // graceful mid-script stop).
  isScript: boolean
  // pausedFrom заполнен только когда status === 'paused' (см. Go
  // StageView.PausedFrom) — пустая строка иначе. Панель паузы использует его
  // для текста причины.
  pausedFrom: StageStatus | ''
}
```

- [ ] **Step 3: Run the frontend test suite to see every now-broken literal**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: FAIL — TypeScript compile errors / test failures listing every file with an incomplete `Stage` literal from Step 1.

- [ ] **Step 4: Fix every listed literal**

For each `Stage` object literal found in Step 1, add `isScript: false, pausedFrom: ''` (or a value the specific test needs) right after `showDialog`. Example (`StagesList.test.tsx`):

```ts
{ id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS

- [ ] **Step 6: Run the build**

Run: `cd pkg/web/dashboard && npm run build`
Expected: builds cleanly.

- [ ] **Step 7: Commit**

```bash
git add pkg/web/dashboard/src/types/stage.ts pkg/web/dashboard/src/components
git commit -m "feat: добавляем isScript/pausedFrom в тип Stage"
```

---

### Task 12: `run-client.ts` — `pauseStage`/`continueStage`

**Files:**
- Modify: `pkg/web/dashboard/src/api/run-client.ts`
- Test: `pkg/web/dashboard/src/api/run-client.test.ts` (create if it doesn't already exist — check first)

**Interfaces:**
- Consumes: `/api/stages/{id}/pause`, `/api/stages/{id}/continue` (Task 10).
- Produces: `pauseStage(stageId: string): Promise<void>`, `continueStage(stageId: string): Promise<void>` — consumed by Tasks 13-14.

- [ ] **Step 1: Check for an existing test file**

Run: `ls pkg/web/dashboard/src/api/run-client.test.ts 2>/dev/null || echo "does not exist"`

If it doesn't exist, this task's test step covers `retryStage` too (for parity) plus the two new functions; if it does exist, just add to it following its existing mocking pattern (`vi.fn` on global `fetch`).

- [ ] **Step 2: Write the failing test**

```ts
import { afterEach, describe, expect, test, vi } from 'vitest'
import { continueStage, pauseStage } from './run-client'

describe('run-client pause/continue', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('pauseStage POSTs to /pause with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await pauseStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/pause',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('continueStage POSTs to /continue with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await continueStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/continue',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })
})
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run run-client`
Expected: FAIL to compile (`pauseStage`/`continueStage` don't exist yet).

- [ ] **Step 4: Add the functions**

In `pkg/web/dashboard/src/api/run-client.ts`, next to `retryStage`:

```ts
export async function pauseStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'pause'), null)
}

export async function continueStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'continue'), null)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run run-client`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/api/run-client.test.ts
git commit -m "feat: добавляем pauseStage/continueStage в run-client"
```

---

### Task 13: `StagesList.tsx` — "Pause" kebab item

**Files:**
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.tsx`
- Test: `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx`

**Interfaces:**
- Consumes: `Stage.isScript` (Task 11), `pauseStage` (Task 12, called from the parent via a new `onPause` prop — matching the existing `onAddNote` pattern, not called directly from this component, keeping `StagesList` free of API-client imports as it is today).
- Produces: `StagesListProps.onPause?: (stageId: string) => void`.

- [ ] **Step 1: Write the failing tests**

Append to `StagesList.test.tsx`:

```ts
test('shows the kebab for planning/revising/retrying too, not just running/awaiting_approval', () => {
  const stages: Stage[] = [
    { id: 'a', name: '', status: 'planning', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    { id: 'b', name: '', status: 'revising', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    { id: 'c', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
  ]
  render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
  expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(3)
})

test('Pause menu item calls onPause and is hidden for a running script stage', () => {
  const onPause = vi.fn()
  const stages: Stage[] = [
    { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    { id: 'b', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '' },
  ]
  render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={onPause} />)

  const buttons = screen.getAllByRole('button', { name: /more actions/i })
  fireEvent.click(buttons[0]!) // stage a: regular running stage
  expect(screen.getByText('Pause')).toBeInTheDocument()
  fireEvent.click(screen.getByText('Pause'))
  expect(onPause).toHaveBeenCalledWith('a')

  fireEvent.click(buttons[1]!) // stage b: running SCRIPT stage
  expect(screen.queryByText('Pause')).not.toBeInTheDocument()
})

test('"Add note for agent" stays limited to running/awaiting_approval even though the kebab now also opens for planning/revising/retrying', () => {
  const stages: Stage[] = [
    { id: 'a', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
  ]
  render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
  fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
  expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run StagesList`
Expected: FAIL — kebab doesn't open for `planning`/`revising`/`retrying` yet, no "Pause" item exists.

- [ ] **Step 3: Widen `KEBAB_STATUSES` and add the "Pause" item**

In `StagesList.tsx`:

```tsx
// Статусы, при которых у стадии доступен кебаб хоть с одним пунктом.
// "Add note for agent" остаётся ограничен running/awaiting_approval (см.
// ниже, отдельное условие на сам пункт) — "Pause" доступен на остальных.
const KEBAB_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval', 'planning', 'revising', 'retrying'])

// Статусы, из которых можно поставить стадию на паузу вручную.
const PAUSABLE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'planning', 'revising', 'retrying'])

// "Add note for agent" (Revise) — только для running/awaiting_approval, как и раньше.
const ADD_NOTE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval'])
```

```tsx
type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  onAddNote?: (stageId: string) => void
  onPause?: (stageId: string) => void
}
```

```tsx
export function StagesList({ stages, selectedStageId, onSelect, onAddNote, onPause }: StagesListProps): ReactElement {
```

Replace the single-`<li>` menu with a menu whose items are each independently gated:

```tsx
                {openMenuStageId === stage.id &&
                  menuPos !== null &&
                  createPortal(
                    <ul
                      className="stage-kebab-menu"
                      ref={menuRef}
                      style={{ position: 'fixed', top: menuPos.top, left: menuPos.left }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      {ADD_NOTE_STATUSES.has(stage.status) && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onAddNote?.(stage.id)
                            }}
                          >
                            Add note for agent
                          </button>
                        </li>
                      )}
                      {PAUSABLE_STATUSES.has(stage.status) && !(stage.isScript && stage.status === 'running') && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onPause?.(stage.id)
                            }}
                          >
                            Pause
                          </button>
                        </li>
                      )}
                    </ul>,
                    document.body,
                  )}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run StagesList`
Expected: PASS, including the pre-existing "shows the kebab menu only when status is running or awaiting_approval" test — update its name/assertion since the kebab now also opens for three more statuses:

```ts
test('shows the kebab menu for running/awaiting_approval/planning/revising/retrying only', () => {
  const stages: Stage[] = [
    { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
    { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' },
  ]
  render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
  expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b
})
```

- [ ] **Step 5: Wire `onPause` in `App.tsx`**

Find where `StagesList` is rendered in `pkg/web/dashboard/src/app/App.tsx` (it already passes `onAddNote`) and add:

```tsx
onPause={(stageId) => { void pauseStage(stageId) }}
```

importing `pauseStage` from `../api/run-client` alongside whatever's already imported there.

- [ ] **Step 6: Run the full frontend suite and build**

Run: `cd pkg/web/dashboard && npx vitest run && npm run build`
Expected: PASS / builds cleanly.

- [ ] **Step 7: Commit**

```bash
git add pkg/web/dashboard/src/components/stages-list/StagesList.tsx pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx pkg/web/dashboard/src/app/App.tsx
git commit -m "feat: добавляем пункт Pause в кебаб-меню стадий"
```

---

### Task 14: `PlanPanel.tsx` — paused section + Continue button

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`

**Interfaces:**
- Consumes: `Stage.status === 'paused'`, `Stage.pausedFrom` (Task 11), `continueStage` (Task 12).

- [ ] **Step 1: Write the failing tests**

This file already has a `makeStage(overrides: Partial<Stage> = {})` helper (top of file) and mocks `fetch` per-test via `vi.spyOn(globalThis, 'fetch')` — match that pattern exactly, the same way the existing `retry()`/`skip()` tests do. `makeStage`'s base object needs `isScript: false, pausedFrom: ''` added to it as part of Task 11's literal sweep (this file matched that task's `grep -rln "interactive: false"`) — that must land before this step compiles.

Add to `PlanPanel.test.tsx`:

```tsx
test('paused, pending: shows the pending-specific reason and a Continue button', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

  render(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'pending' })} />)

  expect(await screen.findByText(/before its first run/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
})

test('paused, running: shows the running-specific reason', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

  render(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'running' })} />)

  expect(await screen.findByText(/manually paused while it was running/i)).toBeInTheDocument()
})

test('paused, retrying: shows the retrying-specific reason', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

  render(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'retrying' })} />)

  expect(await screen.findByText(/waiting to retry/i)).toBeInTheDocument()
})

test('Continue: posts to the continue endpoint', async () => {
  const calls: string[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    calls.push(url)
    return textResponse('')
  })

  render(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'pending' })} />)

  const continueBtn = await screen.findByRole('button', { name: 'Continue' })
  fireEvent.click(continueBtn)

  await waitFor(() => expect(calls.some((c) => c.endsWith('/continue'))).toBe(true))
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run PlanPanel`
Expected: FAIL — no paused section exists yet.

- [ ] **Step 3: Add the paused flag, reason text, and action handler**

In `PlanPanel.tsx`, widen the `clicked` state type and `flashButton`'s parameter to include `'continue'`:

```tsx
  const [clicked, setClicked] = useState<'approve' | 'revise' | 'retry' | 'continue' | null>(null)
```

```tsx
  function flashButton(which: 'approve' | 'revise' | 'retry' | 'continue') {
    setClicked(which)
    window.setTimeout(() => setClicked(null), 1200)
  }
```

Add the flag next to the other status flags (`showRetry`, `showHookFailed`):

```tsx
  const showPaused = stage?.status === 'paused'
```

Add a small reason-text helper (co-located with the other render helpers in this file, not exported):

```tsx
function pausedReasonText(pausedFrom: Stage['pausedFrom']): string {
  switch (pausedFrom) {
    case 'pending':
      return 'This stage is paused before its first run (auto_run: false). Click Continue to start it.'
    case 'retrying':
      return 'This stage was paused while waiting to retry.'
    case 'planning':
      return 'This stage was manually paused while it was planning.'
    case 'revising':
      return 'This stage was manually paused while it was revising.'
    default:
      return 'This stage was manually paused while it was running.'
  }
}
```

Add a `continue` action, following the exact same shape as `retry` (lines 158-167 of this file):

```tsx
  async function doContinue() {
    if (stage === null) return
    flashButton('continue')
    setBusy(true)
    try {
      await continueStage(stage.id)
    } finally {
      setBusy(false)
    }
  }
```

Import `continueStage`:

```tsx
import { approveStage, continueStage, retryHookStage, retryStage, reviseStage, skipHookStage } from '../../api/run-client'
```

Add the JSX block, next to `showRetry`'s section:

```tsx
        {showPaused && (
          <div id="paused-section" className="section">
            <p className="paused-reason">{pausedReasonText(stage?.pausedFrom ?? '')}</p>
            <div className="actions-row">
              <button id="btn-continue" className={`btn btn-approve${clicked === 'continue' ? ' ok' : ''}`} type="button" disabled={busy} onClick={doContinue}>
                <span className="btn-ripple" aria-hidden="true" />
                <span className="btn-label">Continue</span>
                <span className="btn-done" aria-hidden="true">✓</span>
              </button>
            </div>
          </div>
        )}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run PlanPanel`
Expected: PASS

- [ ] **Step 5: Run the full frontend suite and build**

Run: `cd pkg/web/dashboard && npx vitest run && npm run build`
Expected: PASS / builds cleanly.

- [ ] **Step 6: Manual smoke check**

Start a real flow with a stage that has `auto_run: false`, confirm in a browser that: the stage shows `paused`, the panel shows the pending-specific reason and a Continue button, clicking it starts the stage. Then manually pause a running stage via the kebab menu and confirm the panel shows the running-specific reason and Continue resumes it. Report this step's actual result — don't claim success without having run it.

- [ ] **Step 7: Commit**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx
git commit -m "feat: показываем панель paused с кнопкой Continue"
```
