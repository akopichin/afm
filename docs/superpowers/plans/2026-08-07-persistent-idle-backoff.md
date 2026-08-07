# Persistent IDLE/BACKOFF footer metrics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashboard footer's IDLE and BACKOFF counters survive a dashboard/process restart with the same accuracy STARTED/ELAPSED already have, and stop ticking while the WebSocket is disconnected.

**Architecture:** Move the IDLE/BACKOFF computation from a client-side event-replay reducer (capped at 200 events, reset on reload) into `pkg/state.RunState`, updated incrementally at the same single choke point every real transition already passes through for durability (`Store.Apply`) and reconstructed the same way on resume (the shared `parseEventLog` replay path used by both `Store.Open` and `LoadRunState`). The two accumulated-so-far counters are exposed via `/api/status`; the frontend hooks shrink to simple anchor+tick math (like `useElapsed` already is) and freeze while `useEventFeed`'s existing `connected` flag is false.

**Tech Stack:** Go (`pkg/state`, `pkg/server`), React 18 + TypeScript (`pkg/web/dashboard`), Vitest + @testing-library/react, Go `testing`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-07-persistent-idle-backoff-design.md`
- `ELAPSED`/`STARTED` are unchanged — they are already correct and keep ticking through a disconnect; do not touch `useElapsed` or its wiring.
- The dialog/event-feed's static per-row time gaps are unchanged — explicitly out of scope.
- No new persisted "since" timestamp fields (`IdleSince`, `BackoffOpenSince` as stored state) — both are derived on read from the existing `Stages[].UpdatedAt` map, per the plan's grounding (see Task 1). Only two new `int64` accumulator fields are persisted.
- `RunState.Snapshot()`'s pre-existing, unrelated omission of copying `LastSeq` is out of scope — do not touch it.
- Commit messages must be in Russian, no `Co-Authored-By` trailer.

---

### Task 1: Backend — durable idle/backoff accumulation in `pkg/state`

**Files:**
- Modify: `pkg/state/state.go` (add fields to `RunState`, add `isIdle`/`maxUpdatedAt`/`accountIdleAndBackoff`/`IdleSince`/`BackoffOpenSince`, wire into `parseEventLog`)
- Modify: `pkg/state/store.go` (wire into `Store.Apply`, copy new fields in `Store.Snapshot`)
- Test: `pkg/state/state_test.go` (pure-function tests), `pkg/state/store_test.go` (resume-consistency test)

**Interfaces:**
- Produces: two new `RunState` fields — `IdleAccumulatedMs int64` (json `idle_accumulated_ms`), `BackoffAccumulatedMs int64` (json `backoff_accumulated_ms`) — plus two new `RunState` methods: `IdleSince() *time.Time` and `BackoffOpenSince() []time.Time`. Task 2 calls these two methods directly on the `RunState` returned by `Store.Snapshot()`.

- [ ] **Step 1: Write the failing pure-function tests**

Add to `pkg/state/state_test.go` (append to the existing file, keep the existing `package state` and imports — add `"time"` to the import block if not already present, it already is per the file's existing use of `time.Now()`):

```go
func TestIsIdle_QuestionStatusAlwaysIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusRunning},
		"b": {Status: StatusAwaitingApproval},
	}
	if !isIdle(stages) {
		t.Error("want idle=true when any stage is awaiting_approval, regardless of another running stage")
	}
}

func TestIsIdle_FailedAloneIsIdle(t *testing.T) {
	stages := map[string]StageState{
		"a": {Status: StatusFailed},
	}
	if !isIdle(stages) {
		t.Error("want idle=true when a stage is failed and nothing else is active")
	}
}

func TestIsIdle_FailedWhileAnotherRunningIsNotIdle(t *testing.T) {
	// Регрессия из use-idle-time.ts: каскадно упавшая downstream-стадия не
	// должна копить Idle, пока другой агент реально работает.
	stages := map[string]StageState{
		"a": {Status: StatusRunning},
		"b": {Status: StatusFailed},
	}
	if isIdle(stages) {
		t.Error("want idle=false when a stage is failed but another is running")
	}
}

func TestIsIdle_RetryingAloneIsNotIdle(t *testing.T) {
	// retrying — пассивный бэкофф-таймер, не «активная работа», но и не Idle
	// сам по себе (это отдельная метрика, см. BackoffOpenSince).
	stages := map[string]StageState{
		"a": {Status: StatusRetrying},
	}
	if isIdle(stages) {
		t.Error("want idle=false for a lone retrying stage")
	}
}

func TestMaxUpdatedAt_ReturnsLatest(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	stages := map[string]StageState{
		"a": {UpdatedAt: t1},
		"b": {UpdatedAt: t2},
	}
	if got := maxUpdatedAt(stages); !got.Equal(t2) {
		t.Errorf("maxUpdatedAt = %v, want %v", got, t2)
	}
}

func TestAccountIdleAndBackoff_AccumulatesIdleGap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusAwaitingUserInput, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, t1)
	rs.SetStageStatusAt("a", StatusRunning, t1)

	if rs.IdleAccumulatedMs != 5000 {
		t.Errorf("IdleAccumulatedMs = %d, want 5000", rs.IdleAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_NoAccumulationWhenNotIdle(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRunning, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusDone, t1)
	rs.SetStageStatusAt("a", StatusDone, t1)

	if rs.IdleAccumulatedMs != 0 {
		t.Errorf("IdleAccumulatedMs = %d, want 0", rs.IdleAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_ClosesBackoffEpisode(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, t1)
	rs.SetStageStatusAt("a", StatusRunning, t1)

	if rs.BackoffAccumulatedMs != 3000 {
		t.Errorf("BackoffAccumulatedMs = %d, want 3000", rs.BackoffAccumulatedMs)
	}
}

func TestAccountIdleAndBackoff_SumsParallelBackoffEpisodes(t *testing.T) {
	// Два параллельных ретрая суммируются, не мёржатся (осознанное упрощение,
	// см. use-status-duration.ts) — сумма может быть чуть больше wall-clock.
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	tCloseA := t0.Add(2 * time.Second)
	tCloseB := t0.Add(4 * time.Second)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t0},
		"b": {Status: StatusRetrying, UpdatedAt: t0},
	}}

	accountIdleAndBackoff(rs, "a", StatusRunning, tCloseA)
	rs.SetStageStatusAt("a", StatusRunning, tCloseA)
	accountIdleAndBackoff(rs, "b", StatusRunning, tCloseB)
	rs.SetStageStatusAt("b", StatusRunning, tCloseB)

	if rs.BackoffAccumulatedMs != 6000 {
		t.Errorf("BackoffAccumulatedMs = %d, want 6000 (2000+4000)", rs.BackoffAccumulatedMs)
	}
}

func TestRunState_IdleSince_NilWhenNotIdle(t *testing.T) {
	rs := &RunState{Stages: map[string]StageState{"a": {Status: StatusRunning}}}
	if got := rs.IdleSince(); got != nil {
		t.Errorf("IdleSince() = %v, want nil", got)
	}
}

func TestRunState_IdleSince_MatchesLatestUpdatedAtWhenIdle(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusFailed, UpdatedAt: t1},
	}}
	got := rs.IdleSince()
	if got == nil || !got.Equal(t1) {
		t.Errorf("IdleSince() = %v, want %v", got, t1)
	}
}

func TestRunState_BackoffOpenSince_OneEntryPerRetryingStage(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 10, 0, 1, 0, time.UTC)
	rs := &RunState{Stages: map[string]StageState{
		"a": {Status: StatusRetrying, UpdatedAt: t1},
		"b": {Status: StatusDone, UpdatedAt: t2},
		"c": {Status: StatusRetrying, UpdatedAt: t2},
	}}
	got := rs.BackoffOpenSince()
	if len(got) != 2 {
		t.Fatalf("BackoffOpenSince() len = %d, want 2", len(got))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run 'TestIsIdle|TestMaxUpdatedAt|TestAccountIdleAndBackoff|TestRunState_IdleSince|TestRunState_BackoffOpenSince' -v`
Expected: FAIL — compile error, `isIdle`/`maxUpdatedAt`/`accountIdleAndBackoff`/`IdleSince`/`BackoffOpenSince`/`IdleAccumulatedMs`/`BackoffAccumulatedMs` don't exist yet.

- [ ] **Step 3: Implement the pure functions and RunState fields**

In `pkg/state/state.go`, add two fields to the `RunState` struct (currently ending at `LastSeq uint64` around line 55):

```go
// RunState is the top-level state persisted in state.json.
type RunState struct {
	FlowName   string    `json:"flow_name"`
	StartedAt  time.Time `json:"started_at"`
	StageOrder []string  `json:"stage_order"`
	// StageNames maps stage id → human-readable name from the flow file.
	// omitempty keeps old state.json files (without stage_names) compatible
	// and only emits the field when it has been populated.
	StageNames map[string]string     `json:"stage_names,omitempty"`
	Stages     map[string]StageState `json:"stages"`
	// LastSeq is the Seq of the last transition applied to this run,
	// mirrored from the event log so consumers of the snapshot alone
	// (e.g. UI) can detect staleness without replaying events.jsonl.
	LastSeq uint64 `json:"last_seq"`
	// IdleAccumulatedMs/BackoffAccumulatedMs — накопленное время простоя/бэкоффа
	// на момент последнего примененного перехода. Текущий ОТКРЫТЫЙ эпизод (если
	// флоу простаивает/стадия ретраится прямо сейчас) НЕ хранится отдельным
	// полем — он добавляется при чтении через IdleSince()/BackoffOpenSince(),
	// потому что момент его начала всегда совпадает с UpdatedAt той стадии,
	// которая последней сменила статус (см. accountIdleAndBackoff).
	IdleAccumulatedMs    int64 `json:"idle_accumulated_ms"`
	BackoffAccumulatedMs int64 `json:"backoff_accumulated_ms"`
}
```

Then add these functions and methods anywhere in `pkg/state/state.go` (e.g. right after the `RunState` struct and its existing methods, before `LoadRunState`):

```go
// isIdle сообщает, ждёт ли флоу реакции пользователя прямо сейчас — единое
// состояние на весь флоу (в отличие от backoff, который считается по каждой
// стадии отдельно, см. accountIdleAndBackoff). Порт isIdle() из
// use-idle-time.ts (pkg/web/dashboard/src/hooks/use-idle-time/use-idle-time.ts):
//
//   idle = есть вопрос к пользователю на любой стадии (awaiting_user_input,
//          awaiting_approval), ИЛИ (есть failed-стадия И ни один агент не
//          активен: running/planning/revising). retrying намеренно не
//          считается «активной работой» — это пассивный бэкофф-таймер,
//          отдельная метрика (см. BackoffOpenSince).
func isIdle(stages map[string]StageState) bool {
	hasFailed := false
	anyActive := false
	for _, st := range stages {
		switch st.Status {
		case StatusAwaitingUserInput, StatusAwaitingApproval:
			return true
		case StatusFailed:
			hasFailed = true
		case StatusRunning, StatusPlanning, StatusRevising:
			anyActive = true
		}
	}
	return hasFailed && !anyActive
}

// maxUpdatedAt возвращает самый свежий UpdatedAt среди всех стадий — момент
// последнего примененного во флоу перехода. Используется и как «время
// предыдущего события» при накоплении Idle, и как idle_since для API (см.
// RunState.IdleSince).
func maxUpdatedAt(stages map[string]StageState) time.Time {
	var max time.Time
	for _, st := range stages {
		if st.UpdatedAt.After(max) {
			max = st.UpdatedAt
		}
	}
	return max
}

// accountIdleAndBackoff обновляет RunState.IdleAccumulatedMs/BackoffAccumulatedMs
// ДО применения перехода {stageID, to, t} к rs — читает rs.Stages как оно было
// ПЕРЕД этим переходом. Вызывается из ОБОИХ мест, применяющих переходы к
// RunState (parseEventLog при replay и Store.Apply при живой работе), чтобы
// восстановление после перезапуска (Store.Open → replayEvents → parseEventLog)
// давало те же накопленные значения, что и живой прогон.
func accountIdleAndBackoff(rs *RunState, stageID string, to StageStatus, t time.Time) {
	if isIdle(rs.Stages) {
		if prev := maxUpdatedAt(rs.Stages); !prev.IsZero() && t.After(prev) {
			rs.IdleAccumulatedMs += t.Sub(prev).Milliseconds()
		}
	}

	before := rs.Stages[stageID]
	if before.Status == StatusRetrying && to != StatusRetrying && t.After(before.UpdatedAt) {
		rs.BackoffAccumulatedMs += t.Sub(before.UpdatedAt).Milliseconds()
	}
}

// IdleSince возвращает момент начала текущего периода простоя, если флоу
// простаивает сейчас (см. isIdle) — иначе nil.
func (rs *RunState) IdleSince() *time.Time {
	if !isIdle(rs.Stages) {
		return nil
	}
	t := maxUpdatedAt(rs.Stages)
	if t.IsZero() {
		return nil
	}
	return &t
}

// BackoffOpenSince возвращает момент входа в retrying для каждой стадии,
// которая сейчас в этом статусе — параллельные эпизоды суммируются на чтении
// (фронтенд), а не мёржатся здесь (осознанное упрощение, см.
// use-status-duration.ts).
func (rs *RunState) BackoffOpenSince() []time.Time {
	var out []time.Time
	for _, st := range rs.Stages {
		if st.Status == StatusRetrying {
			out = append(out, st.UpdatedAt)
		}
	}
	return out
}
```

Now wire `accountIdleAndBackoff` into the replay path. In `parseEventLog` (currently):

```go
		var t Transition
		if json.Unmarshal(line, &t) != nil {
			res.goodOffset = goodOffset
			res.corrupted = true
			return res
		}
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		res.history = append(res.history, t)
```

change to:

```go
		var t Transition
		if json.Unmarshal(line, &t) != nil {
			res.goodOffset = goodOffset
			res.corrupted = true
			return res
		}
		accountIdleAndBackoff(rs, t.StageID, t.To, t.Time)
		rs.SetStageStatusAt(t.StageID, t.To, t.Time)
		res.history = append(res.history, t)
```

- [ ] **Step 4: Run the pure-function tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run 'TestIsIdle|TestMaxUpdatedAt|TestAccountIdleAndBackoff|TestRunState_IdleSince|TestRunState_BackoffOpenSince' -v`
Expected: PASS, all cases.

- [ ] **Step 5: Write the failing resume-consistency test**

Add to `pkg/state/store_test.go` (append; the file already imports `"os"`, `"path/filepath"`, `"testing"`, `"time"` — no new imports needed):

```go
// Живой прогон копит Idle через Apply; закрытие и повторное Open той же
// run-директории должно восстановить ТОЧНО ТО ЖЕ значение через replay
// (parseEventLog) — это и есть гарантия "восстановить при продолжении
// работы afm" из спеки.
func TestOpen_ResumeReconstructsIdleAndBackoffFromReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	apply := func(stageID string, from, to StageStatus) {
		if err := store.Apply(&Transition{StageID: stageID, From: from, To: to, Event: "test"}); err != nil {
			t.Fatalf("Apply(%s, %s->%s): %v", stageID, from, to, err)
		}
	}

	apply("a", StatusPending, StatusRunning)
	apply("a", StatusRunning, StatusFailed) // a failed, nothing else active → idle starts
	time.Sleep(10 * time.Millisecond)
	apply("b", StatusPending, StatusRetrying) // b starts a backoff episode
	time.Sleep(10 * time.Millisecond)
	apply("b", StatusRetrying, StatusPending) // b's backoff episode closes

	before := store.Snapshot()
	if before.BackoffAccumulatedMs <= 0 {
		t.Fatalf("BackoffAccumulatedMs before close = %d, want > 0", before.BackoffAccumulatedMs)
	}
	if before.IdleSince() == nil {
		t.Fatal("IdleSince() before close = nil, want non-nil (a is failed, nothing active)")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer reopened.Close()

	after := reopened.Snapshot()
	if after.BackoffAccumulatedMs != before.BackoffAccumulatedMs {
		t.Errorf("BackoffAccumulatedMs after resume = %d, want %d (unchanged from replay)", after.BackoffAccumulatedMs, before.BackoffAccumulatedMs)
	}
	if after.IdleAccumulatedMs != before.IdleAccumulatedMs {
		t.Errorf("IdleAccumulatedMs after resume = %d, want %d (unchanged from replay)", after.IdleAccumulatedMs, before.IdleAccumulatedMs)
	}
	beforeSince, afterSince := before.IdleSince(), after.IdleSince()
	if (beforeSince == nil) != (afterSince == nil) {
		t.Fatalf("IdleSince() presence mismatch: before=%v after=%v", beforeSince, afterSince)
	}
	if beforeSince != nil && !beforeSince.Equal(*afterSince) {
		t.Errorf("IdleSince() after resume = %v, want %v", afterSince, beforeSince)
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -run TestOpen_ResumeReconstructsIdleAndBackoffFromReplay -v`
Expected: FAIL — `store.Snapshot()` doesn't have `BackoffAccumulatedMs`/`IdleAccumulatedMs`/`IdleSince()` populated yet (Task 1's wiring into `Store.Apply`/`Snapshot` isn't done until Step 7).

- [ ] **Step 7: Wire `accountIdleAndBackoff` into `Store.Apply` and fix `Store.Snapshot`**

In `pkg/state/store.go`, inside `Apply` — currently:

```go
	s.snapshot.SetStageStatus(t.StageID, t.To)
	s.snapshot.LastSeq = s.lastSeq
```

change to:

```go
	accountIdleAndBackoff(s.snapshot, t.StageID, t.To, t.Time)
	s.snapshot.SetStageStatus(t.StageID, t.To)
	s.snapshot.LastSeq = s.lastSeq
```

In `Snapshot()` — currently:

```go
func (s *Store) Snapshot() RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RunState{
		FlowName:   s.snapshot.FlowName,
		StartedAt:  s.snapshot.StartedAt,
		StageOrder: append([]string(nil), s.snapshot.StageOrder...),
		Stages:     make(map[string]StageState, len(s.snapshot.Stages)),
	}
```

change to:

```go
func (s *Store) Snapshot() RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RunState{
		FlowName:             s.snapshot.FlowName,
		StartedAt:            s.snapshot.StartedAt,
		StageOrder:           append([]string(nil), s.snapshot.StageOrder...),
		Stages:               make(map[string]StageState, len(s.snapshot.Stages)),
		IdleAccumulatedMs:    s.snapshot.IdleAccumulatedMs,
		BackoffAccumulatedMs: s.snapshot.BackoffAccumulatedMs,
	}
```

(The rest of `Snapshot`'s body — the `StageNames`/`Stages` copy loop and `return out` — is unchanged. Do not add `LastSeq` here; its omission is a separate, pre-existing, unrelated gap — out of scope per this plan's Global Constraints.)

- [ ] **Step 8: Run all `pkg/state` tests to verify everything passes**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/state/... -v`
Expected: PASS, every test in the package, including all pre-existing ones (confirms nothing regressed).

- [ ] **Step 9: Commit**

```bash
git add pkg/state/state.go pkg/state/store.go pkg/state/state_test.go pkg/state/store_test.go
git commit -m "feat(state): считаем IDLE/BACKOFF инкрементально и переживаем restart через replay"
```

---

### Task 2: Backend — expose IDLE/BACKOFF via `/api/status`

**Files:**
- Modify: `pkg/server/handlers.go`
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `RunState.IdleAccumulatedMs`/`BackoffAccumulatedMs` (JSON-embedded automatically via `statusResponse`'s embedded `state.RunState`) and `RunState.IdleSince()`/`BackoffOpenSince()` from Task 1.
- Produces: `/api/status` JSON gains `idle_since` (RFC3339 string or absent) and `backoff_open_since` (array of RFC3339 strings, possibly empty/absent). Task 3 parses these exact field names.

- [ ] **Step 1: Write the failing test**

Add to `pkg/server/handlers_test.go` (append; reuses the existing `setupTestServer`/`testStageID` helpers already in the file):

```go
func TestHandleStatus_IncludesIdleSinceWhenIdle(t *testing.T) {
	srv, _ := setupTestServer(t)
	// setupTestServer already moved s1 to awaiting_approval — that's a
	// QUESTION_STATUSES-equivalent, so the flow is idle right now.

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp struct {
		IdleSince        *time.Time `json:"idle_since"`
		BackoffOpenSince []time.Time `json:"backoff_open_since"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IdleSince == nil {
		t.Error("idle_since = nil, want a timestamp (stage is awaiting_approval)")
	}
	if len(resp.BackoffOpenSince) != 0 {
		t.Errorf("backoff_open_since = %v, want empty (no stage retrying)", resp.BackoffOpenSince)
	}
}

func TestHandleStatus_IncludesBackoffOpenSinceWhenRetrying(t *testing.T) {
	srv, _ := setupTestServer(t)
	if err := srv.store.Apply(&state.Transition{StageID: testStageID, From: state.StatusAwaitingApproval, To: state.StatusRetrying, Event: "test"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	var resp struct {
		BackoffOpenSince []time.Time `json:"backoff_open_since"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.BackoffOpenSince) != 1 {
		t.Fatalf("backoff_open_since len = %d, want 1", len(resp.BackoffOpenSince))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -run 'TestHandleStatus_IncludesIdleSinceWhenIdle|TestHandleStatus_IncludesBackoffOpenSinceWhenRetrying' -v`
Expected: FAIL — `idle_since`/`backoff_open_since` are absent from the current response, so `resp.IdleSince` stays nil and the length checks fail.

- [ ] **Step 3: Add the fields to `statusResponse` and populate them**

In `pkg/server/handlers.go`, add `"time"` to the import block (currently `encoding/json`, `fmt`, `log`, `net/http`, `os`, `path/filepath`, `regexp`, `strings`, plus the four internal packages):

```go
import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/state"
)
```

Change `statusResponse` (currently):

```go
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
	StageAutoApprove map[string]bool `json:"stage_auto_approve,omitempty"`
}
```

to:

```go
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
	StageAutoApprove map[string]bool `json:"stage_auto_approve,omitempty"`
	// IdleSince/BackoffOpenSince — computed from RunState.Stages on read (see
	// RunState.IdleSince/BackoffOpenSince), not stored fields. nil/empty when
	// not currently idle/retrying.
	IdleSince        *time.Time  `json:"idle_since,omitempty"`
	BackoffOpenSince []time.Time `json:"backoff_open_since,omitempty"`
}
```

Change `handleStatus` (currently):

```go
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
		StageAutoApprove: s.stageAutoApprove,
	}
```

to:

```go
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
		StageAutoApprove: s.stageAutoApprove,
		IdleSince:        rs.IdleSince(),
		BackoffOpenSince: rs.BackoffOpenSince(),
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/server/... -v`
Expected: PASS, including all pre-existing tests in the package (in particular `TestHandleStatus`, `TestHandleStatus_IncludesStageNames`, `TestHandleStatus_IncludesInteractiveAndAutonomous`, `TestHandleStatus_IncludesAutoApprove`, `TestHandleStatus_IncludesDescription` unchanged).

- [ ] **Step 5: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "feat(server): отдаём idle_since/backoff_open_since в /api/status"
```

---

### Task 3: Frontend — parse the new fields, add anchor-based hooks, delete the old ones

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts`
- Test: `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts`
- Create: `pkg/web/dashboard/src/hooks/use-idle-ms/use-idle-ms.ts`, `pkg/web/dashboard/src/hooks/use-idle-ms/index.ts`
- Test: `pkg/web/dashboard/src/hooks/use-idle-ms/use-idle-ms.test.ts`
- Create: `pkg/web/dashboard/src/hooks/use-backoff-ms/use-backoff-ms.ts`, `pkg/web/dashboard/src/hooks/use-backoff-ms/index.ts`
- Test: `pkg/web/dashboard/src/hooks/use-backoff-ms/use-backoff-ms.test.ts`
- Delete: `pkg/web/dashboard/src/hooks/use-idle-time/` (whole directory: `use-idle-time.ts`, `use-idle-time.test.ts`, `index.ts`)
- Delete: `pkg/web/dashboard/src/hooks/use-status-duration/` (whole directory: `use-status-duration.ts`, `use-status-duration.test.ts`, `index.ts`)

**Interfaces:**
- Consumes: the new `/api/status` fields from Task 2 (`idle_accumulated_ms`, `idle_since`, `backoff_accumulated_ms`, `backoff_open_since`).
- Produces: `FlowStatus` (from `useStatus`) gains four new fields: `idleAccumulatedMs: number`, `idleSince: string | null`, `backoffAccumulatedMs: number`, `backoffOpenSince: string[]`. Two new hooks: `useIdleMs(accumulatedMs: number, since: string | null, connected: boolean): number` and `useBackoffMs(accumulatedMs: number, openSince: string[], connected: boolean): number`. Task 4 imports both from `'../hooks/use-idle-ms'`/`'../hooks/use-backoff-ms'` and passes them `connected` from `useEventFeed`.

- [ ] **Step 1: Write the failing `use-status` parsing tests**

Read `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts` first to match its existing test style (it tests `normalizeStatus` directly with raw objects), then add:

```ts
test('normalizeStatus parses idle/backoff fields', () => {
  const result = normalizeStatus({
    flow_name: 'demo',
    stage_order: ['s1'],
    stages: { s1: { status: 'running', updated_at: '' } },
    idle_accumulated_ms: 5000,
    idle_since: '2026-08-07T10:00:00Z',
    backoff_accumulated_ms: 3000,
    backoff_open_since: ['2026-08-07T10:05:00Z', '2026-08-07T10:06:00Z'],
  })

  expect(result.idleAccumulatedMs).toBe(5000)
  expect(result.idleSince).toBe('2026-08-07T10:00:00Z')
  expect(result.backoffAccumulatedMs).toBe(3000)
  expect(result.backoffOpenSince).toEqual(['2026-08-07T10:05:00Z', '2026-08-07T10:06:00Z'])
})

test('normalizeStatus defaults idle/backoff fields when absent', () => {
  const result = normalizeStatus({ flow_name: 'demo', stage_order: [], stages: {} })

  expect(result.idleAccumulatedMs).toBe(0)
  expect(result.idleSince).toBeNull()
  expect(result.backoffAccumulatedMs).toBe(0)
  expect(result.backoffOpenSince).toEqual([])
})
```

(The file's existing import line already covers this: `import { normalizeStatus, useStatus } from './use-status'` — no import changes needed.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-status/use-status.test.ts`
Expected: FAIL — `result.idleAccumulatedMs` etc. are `undefined`, not the expected values.

- [ ] **Step 3: Extend `FlowStatus` and `normalizeStatus`**

In `pkg/web/dashboard/src/hooks/use-status/use-status.ts`, change the `FlowStatus` type (currently):

```ts
export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  // Описание флоу (из корня flow.yaml) — опциональное поле GET /api/status для
  // подзаголовка в шапке (см. FlowHeader). Бэкенд пока его не отдаёт, поле
  // читается защитно (undefined, если отсутствует), без нового API-вызова —
  // как только бэкенд начнёт присылать description, подзаголовок появится сам.
  description?: string
}

const EMPTY_STATUS: FlowStatus = { flowName: '', stages: [], startedAt: '' }
```

to:

```ts
export type FlowStatus = {
  flowName: string
  stages: Stage[]
  startedAt: string
  // Описание флоу (из корня flow.yaml) — опциональное поле GET /api/status для
  // подзаголовка в шапке (см. FlowHeader). Бэкенд пока его не отдаёт, поле
  // читается защитно (undefined, если отсутствует), без нового API-вызова —
  // как только бэкенд начнёт присылать description, подзаголовок появится сам.
  description?: string
  // idle/backoff — накопленное на бэкенде время (пережившее restart) плюс
  // необязательный анкер ТЕКУЩЕГО открытого периода/эпизодов, см.
  // useIdleMs/useBackoffMs. idleSince — null, если флоу не простаивает
  // прямо сейчас; backoffOpenSince — по одному значению на каждую стадию,
  // сейчас находящуюся в retrying (может быть пустым).
  idleAccumulatedMs: number
  idleSince: string | null
  backoffAccumulatedMs: number
  backoffOpenSince: string[]
}

const EMPTY_STATUS: FlowStatus = {
  flowName: '',
  stages: [],
  startedAt: '',
  idleAccumulatedMs: 0,
  idleSince: null,
  backoffAccumulatedMs: 0,
  backoffOpenSince: [],
}
```

Change `normalizeStatus` (currently):

```ts
export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  const namesObj = isRecord(obj.stage_names) ? obj.stage_names : {}
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}
  const autoApproveObj = isRecord(obj.stage_auto_approve) ? obj.stage_auto_approve : {}

  const order = resolveOrder(obj.stage_order, stagesObj)

  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true, autoApproveObj[id] === true),
  )

  return { flowName, stages, startedAt, description }
}
```

to:

```ts
export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stagesObj = isRecord(obj.stages) ? obj.stages : {}
  const namesObj = isRecord(obj.stage_names) ? obj.stage_names : {}
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}
  const autoApproveObj = isRecord(obj.stage_auto_approve) ? obj.stage_auto_approve : {}

  const order = resolveOrder(obj.stage_order, stagesObj)

  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true, autoApproveObj[id] === true),
  )

  const idleAccumulatedMs = typeof obj.idle_accumulated_ms === 'number' ? obj.idle_accumulated_ms : 0
  const idleSince = typeof obj.idle_since === 'string' ? obj.idle_since : null
  const backoffAccumulatedMs = typeof obj.backoff_accumulated_ms === 'number' ? obj.backoff_accumulated_ms : 0
  const backoffOpenSince = Array.isArray(obj.backoff_open_since)
    ? obj.backoff_open_since.filter((v): v is string => typeof v === 'string')
    : []

  return { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince }
}
```

- [ ] **Step 4: Run the `use-status` tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-status/use-status.test.ts`
Expected: PASS, including all pre-existing tests in the file unchanged.

- [ ] **Step 5: Write the failing `useIdleMs` tests**

Create `pkg/web/dashboard/src/hooks/use-idle-ms/use-idle-ms.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useIdleMs } from './use-idle-ms'

describe('useIdleMs', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns just the accumulated value when not currently idle (since=null)', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:00Z').getTime() })

    const { result } = renderHook(() => useIdleMs(5000, null, true))

    expect(result.current).toBe(5000)
  })

  test('adds live delta since the anchor while idle and connected, ticking every second', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:02Z').getTime() })

    const { result } = renderHook(() => useIdleMs(5000, '2026-08-07T10:00:00Z', true))

    expect(result.current).toBe(7000) // 5000 accumulated + 2000 live

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(8000)
  })

  test('freezes the displayed value while disconnected', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:02Z').getTime() })

    const { result, rerender } = renderHook(({ connected }) => useIdleMs(5000, '2026-08-07T10:00:00Z', connected), {
      initialProps: { connected: true },
    })
    expect(result.current).toBe(7000)

    rerender({ connected: false })
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    // Отключено — тик не идёт, значение держится на последнем вычисленном.
    expect(result.current).toBe(7000)

    rerender({ connected: true })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    // Переподключились — тик продолжается от той же точки (в реальном
    // приложении к этому моменту accumulatedMs/since уже обновились свежим
    // /api/status; здесь параметры не менялись, поэтому просто +1s).
    expect(result.current).toBe(13000)
  })
})
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-idle-ms/use-idle-ms.test.ts`
Expected: FAIL — the module `./use-idle-ms` doesn't exist yet.

- [ ] **Step 7: Implement `useIdleMs`**

Create `pkg/web/dashboard/src/hooks/use-idle-ms/use-idle-ms.ts`:

```ts
import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// Секундомер накопленного Idle-времени: accumulatedMs (пережившее restart,
// см. RunState.IdleAccumulatedMs на бэкенде) плюс живая дельта с since, пока
// флоу простаивает прямо сейчас (since не null). Пока connected=false —
// отображаемое значение просто держится на месте: сокет не обновляет
// accumulatedMs/since, поэтому дальнейший локальный тик мог бы показать
// неверное значение (стадия могла давно перестать простаивать).
export function useIdleMs(accumulatedMs: number, since: string | null, connected: boolean): number {
  const [displayMs, setDisplayMs] = useState(accumulatedMs)

  useEffect(() => {
    function compute(): number {
      if (since === null) return accumulatedMs
      const sinceMs = Date.parse(since)
      if (Number.isNaN(sinceMs)) return accumulatedMs
      return accumulatedMs + Math.max(0, Date.now() - sinceMs)
    }

    if (!connected) {
      setDisplayMs(compute())
      return
    }

    setDisplayMs(compute())
    const timer = setInterval(() => setDisplayMs(compute()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [accumulatedMs, since, connected])

  return displayMs
}
```

Create `pkg/web/dashboard/src/hooks/use-idle-ms/index.ts`:

```ts
export { useIdleMs } from './use-idle-ms'
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-idle-ms/use-idle-ms.test.ts`
Expected: PASS, all 3 tests.

- [ ] **Step 9: Write the failing `useBackoffMs` tests**

Create `pkg/web/dashboard/src/hooks/use-backoff-ms/use-backoff-ms.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useBackoffMs } from './use-backoff-ms'

describe('useBackoffMs', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns just the accumulated value when no stage is currently retrying', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:00Z').getTime() })

    const { result } = renderHook(() => useBackoffMs(3000, [], true))

    expect(result.current).toBe(3000)
  })

  test('sums live deltas for every currently-open episode', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:04Z').getTime() })

    const { result } = renderHook(() => useBackoffMs(1000, ['2026-08-07T10:00:00Z', '2026-08-07T10:00:02Z'], true))

    // 1000 accumulated + 4000 (first episode) + 2000 (second episode)
    expect(result.current).toBe(7000)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(9000)
  })

  test('freezes the displayed value while disconnected', () => {
    vi.useFakeTimers({ now: new Date('2026-08-07T10:00:04Z').getTime() })

    const { result, rerender } = renderHook(({ connected }) => useBackoffMs(1000, ['2026-08-07T10:00:00Z'], connected), {
      initialProps: { connected: true },
    })
    expect(result.current).toBe(5000)

    rerender({ connected: false })
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(5000)
  })
})
```

- [ ] **Step 10: Run the test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-backoff-ms/use-backoff-ms.test.ts`
Expected: FAIL — the module `./use-backoff-ms` doesn't exist yet.

- [ ] **Step 11: Implement `useBackoffMs`**

Create `pkg/web/dashboard/src/hooks/use-backoff-ms/use-backoff-ms.ts`:

```ts
import { useEffect, useState } from 'react'

const TICK_INTERVAL_MS = 1000

// Секундомер накопленного Backoff-времени: accumulatedMs (пережившее restart)
// плюс сумма живых дельт для каждого сейчас открытого эпизода в openSince —
// параллельные ретраи суммируются, а не мёржатся (см. use-status-duration.ts,
// которую этот хук заменяет). Как и useIdleMs, замораживает отображаемое
// значение при connected=false.
export function useBackoffMs(accumulatedMs: number, openSince: string[], connected: boolean): number {
  const [displayMs, setDisplayMs] = useState(accumulatedMs)

  useEffect(() => {
    function compute(): number {
      const now = Date.now()
      let liveMs = 0
      for (const since of openSince) {
        const sinceMs = Date.parse(since)
        if (!Number.isNaN(sinceMs)) {
          liveMs += Math.max(0, now - sinceMs)
        }
      }
      return accumulatedMs + liveMs
    }

    if (!connected) {
      setDisplayMs(compute())
      return
    }

    setDisplayMs(compute())
    const timer = setInterval(() => setDisplayMs(compute()), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [accumulatedMs, openSince, connected])

  return displayMs
}
```

Create `pkg/web/dashboard/src/hooks/use-backoff-ms/index.ts`:

```ts
export { useBackoffMs } from './use-backoff-ms'
```

- [ ] **Step 12: Run the test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-backoff-ms/use-backoff-ms.test.ts`
Expected: PASS, all 3 tests.

- [ ] **Step 13: Delete the old hooks**

```bash
rm -rf pkg/web/dashboard/src/hooks/use-idle-time pkg/web/dashboard/src/hooks/use-status-duration
```

(Task 4 removes the last remaining imports of these two hooks from `App.tsx`. Do not run the full test suite yet — `App.test.tsx` still references the old behavior until Task 4 updates it. That's expected and handled next task.)

- [ ] **Step 14: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-status/ pkg/web/dashboard/src/hooks/use-idle-ms/ pkg/web/dashboard/src/hooks/use-backoff-ms/
git rm -r pkg/web/dashboard/src/hooks/use-idle-time pkg/web/dashboard/src/hooks/use-status-duration
git commit -m "feat(dashboard): парсим idle/backoff из /api/status, заводим анкорные хуки useIdleMs/useBackoffMs"
```

---

### Task 4: Frontend — wire into `App.tsx`, migrate its idle regression tests, add the disconnect-freeze test

**Files:**
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Modify: `pkg/web/dashboard/src/app/App.test.tsx`

**Interfaces:**
- Consumes: `useIdleMs`/`useBackoffMs` from Task 3, `FlowStatus`'s four new fields from Task 3, `connected` from the already-existing `useEventFeed`.

- [ ] **Step 1: Update the two existing idle regression tests to the new poll-based model**

In `pkg/web/dashboard/src/app/App.test.tsx`, the two tests currently named `'accumulates Idle time across an awaiting_user_input episode and shows it in the footer'` and `'regression: a cascaded-failed downstream stage does not count as Idle while another stage is actively running'` simulate WS `stage_status_changed` events to drive `useIdleTime`'s replay logic. That logic no longer exists in the frontend — replace both tests with versions that simulate the same scenarios via `/api/status` poll responses instead (matching how `started_at`'s appearance is already simulated via the `started` flag pattern in this file). Replace the first test's body with:

```tsx
  test('shows accumulated Idle time from /api/status and lets it tick while an idle episode is open', async () => {
    let idleSince: string | null = null
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Propose' },
      stages: { s1: { status: 'running', updated_at: '' } },
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 5000,
      idle_since: idleSince,
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))
    await waitFor(() => expect(document.getElementById('idle')).toHaveTextContent('00:05'))

    idleSince = '2026-07-29T10:00:00.000Z'
    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })

    await waitFor(() => {
      const text = document.getElementById('idle')?.textContent ?? ''
      expect(text).not.toBe('00:05')
    })
  })
```

Replace the second test's body with:

```tsx
  test('regression: idle_accumulated_ms with idle_since=null does not tick — the backend, not the client, decides what counts as idle', async () => {
    // Реальный баг, который чинил старый useIdleTime на фронте, теперь чинится
    // на бэкенде (см. Task 1's TestIsIdle_FailedWhileAnotherRunningIsNotIdle) —
    // здесь достаточно проверить, что фронт просто показывает то, что
    // прислал бэкенд, и не тикает, если idle_since=null (флоу не простаивает).
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1', 's2'],
      stage_names: { s1: 'Upstream', s2: 'Downstream' },
      stages: {
        s1: { status: 'running', updated_at: '' },
        s2: { status: 'failed', updated_at: '' },
      },
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 0,
      idle_since: null,
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Upstream'))
    await waitFor(() => expect(document.getElementById('idle')).toHaveTextContent('00:00'))
  })
```

- [ ] **Step 2: Write the failing WebSocket-disconnect-freezes-the-footer test**

Add to `pkg/web/dashboard/src/app/App.test.tsx`, after the two tests updated above:

```tsx
  test('IDLE stops ticking while the WebSocket is disconnected and resumes once reconnected', async () => {
    vi.useFakeTimers()
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Propose' },
      stages: { s1: { status: 'awaiting_approval', updated_at: '2026-07-29T10:00:00.000Z' } },
      started_at: '2026-07-29T09:59:00.000Z',
      idle_accumulated_ms: 0,
      idle_since: '2026-07-29T10:00:00.000Z',
    }))

    render(<App />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    const idleTextConnected = document.getElementById('idle')?.textContent ?? ''

    act(() => {
      ws?.onclose?.()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })

    // Сокет разорван — значение держится на месте, а не продолжает тикать.
    expect(document.getElementById('idle')).toHaveTextContent(idleTextConnected)

    vi.useRealTimers()
  })
```

(This is the first test in this file to use fake timers — it is self-contained, calling `vi.useRealTimers()` itself at the end, so it needs no changes to the file's existing top-level `afterEach` at the top of the `describe('App', ...)` block, which only does `vi.restoreAllMocks()`/`vi.unstubAllGlobals()`.)

- [ ] **Step 3: Run all three updated/new tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/app/App.test.tsx`
Expected: FAIL — `App.tsx` still imports the deleted `useIdleTime`/`useStatusDuration` modules (compile/import error) and doesn't read `connected` into these hooks yet.

- [ ] **Step 4: Wire the new hooks into `App.tsx`**

Change the imports (currently):

```tsx
import { useElapsed } from '../hooks/use-elapsed'
import { useStatusDuration } from '../hooks/use-status-duration'
import { useIdleTime } from '../hooks/use-idle-time'
```

to:

```tsx
import { useElapsed } from '../hooks/use-elapsed'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
```

Remove `BACKOFF_STATUSES` from the existing types import line (currently):

```tsx
import { ACTIVE_STAGE_STATUSES, BACKOFF_STATUSES, SIGNIFICANT_EVENT_TYPES, STAGE_STATUS_LABELS } from '../types'
```

to:

```tsx
import { ACTIVE_STAGE_STATUSES, SIGNIFICANT_EVENT_TYPES, STAGE_STATUS_LABELS } from '../types'
```

Change the hook-call line (currently):

```tsx
  const { flowName, stages, startedAt, description, refresh } = useStatus()
```

to:

```tsx
  const { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince, refresh } = useStatus()
```

Change the metrics block (currently):

```tsx
  const logEntries = useStageLog(selectedStageId)
  const elapsedMs = useElapsed(startedAt)
  const idleMs = useIdleTime(events)
  const backoffMs = useStatusDuration(events, BACKOFF_STATUSES)
```

to:

```tsx
  const logEntries = useStageLog(selectedStageId)
  const elapsedMs = useElapsed(startedAt)
  const idleMs = useIdleMs(idleAccumulatedMs, idleSince, connected)
  const backoffMs = useBackoffMs(backoffAccumulatedMs, backoffOpenSince, connected)
```

(`connected` is already destructured earlier in the file at `const { events, connected } = useEventFeed(wsUrl)` — this line is unchanged; `events` is still used elsewhere in `App.tsx` for the event feed panel and significant-event refresh logic, only its use for idle/backoff computation is removed.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/app/App.test.tsx`
Expected: PASS, including every pre-existing test in the file unchanged.

- [ ] **Step 6: Run the full dashboard test suite**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS, all test files — confirms the deleted `use-idle-time`/`use-status-duration` hooks have no other lingering references anywhere in the codebase.

- [ ] **Step 7: Commit**

```bash
git add pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/src/app/App.test.tsx
git commit -m "feat(dashboard): футер берёт IDLE/BACKOFF из /api/status и замирает при обрыве соединения"
```

---

### Task 5: Full verification pass

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Run the full lint + test + build pipeline**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make lint && make build && make test`
Expected: all three succeed with no new warnings/failures.

- [ ] **Step 2: Manually verify in a real browser**

Using the same kind of native (non-Docker) `afm run` setup with a mock agent used for the previous two plans' manual verification in this session (check `~/.afm/config.yaml` for `docker.enabled: true` and override with `docker: { enabled: false }` in the scratch project's own `.afm/config.yaml`):

1. Drive a flow's stage into a state that makes it idle (e.g. `awaiting_approval`, or make the mock agent fail so the stage goes `failed`) and confirm the footer's `Idle` counter starts ticking up from whatever `idle_accumulated_ms` the server reports.
2. Reload the dashboard page while that idle episode is still open — confirm `Idle` does NOT reset to `00:00`; it should show a value at least as large as it was before reload (the server kept counting through the reload, and the accumulated-so-far value already reflects it).
3. With the WebSocket connected and `Idle` ticking, use the browser devtools (Network conditions → Offline, or close the dashboard's WS connection directly via devtools) to simulate a disconnect — confirm `Idle` (and `Backoff`, if you can drive a stage into `retrying`) stops advancing and the `LINK`/`OFFLINE` badge in the header flips to offline. Restore connectivity and confirm the counter resumes (and jumps to the correct value if time passed while disconnected, rather than silently continuing to have counted that gap incorrectly).
4. Confirm `Elapsed` keeps ticking normally through the same disconnect (per the explicit decision that it's out of scope for freezing).
5. Retry a failed/retrying stage until it succeeds, and confirm `Backoff` stops growing once the stage leaves `retrying`, holding at its final accumulated value.

Report the outcome of each of these five checks explicitly; do not report the task as complete without having exercised them in an actual browser.
