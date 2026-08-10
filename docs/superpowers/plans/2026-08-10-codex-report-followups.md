# Codex-report follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the four follow-ups from the codex architecture report that were judged adequate and low-risk: narrow the server's callback-bag into two interfaces, replace `/api/status`'s five parallel per-stage maps with one ordered `StageView` array (computing `show_plan`/`show_dialog` server-side), have the frontend consume that array directly (killing the `NO_STAGE` sentinel and extracting a small API client), and generate the frontend's `StageStatus` union from the Go source of truth instead of hand-syncing it.

**Architecture:** Each task is a self-contained vertical slice inside the existing `pkg/server` / `pkg/orchestrator` / dashboard structure — no new packages, no domain-model rewrite. Task 1 and 2 touch only `pkg/server` (plus one new `Orchestrator.CancelDialog` method). Task 3 is frontend-only and depends on Task 2's wire format. Task 4 is independent and can run any time after Task 1 (kept last because it's the lowest-value item of the four).

**Tech Stack:** Go 1.26 (backend, `net/http`, no framework), React + TypeScript + Vite (dashboard, `pkg/web/dashboard`), Vitest for frontend tests, `go test ./...` for backend tests.

## Global Constraints

- Do not change `go.mod`'s `go` directive (currently `1.26.4`) — user's standing rule, unrelated to this work.
- Every task must leave `go build ./...`, `go test ./... -race`, `make lint` (golangci-lint + `tools/setstatuslinter`), and (for frontend tasks) `npm run build` / `npm test` (Vitest, run via `cd pkg/web/dashboard && npm test`) green before moving to the next task.
- No new abstraction beyond what's specified below — do not introduce a generic `Command`/`Query` dispatch type, do not create a `pkg/domain` package, do not touch `pkg/state`'s persisted `RunState`/`StageState` structs (only `pkg/server`'s wire-level view types change).
- Wire format changes to `GET /api/status` are allowed to be breaking — the only consumer is this repo's own dashboard, which is updated in the same set of tasks. No versioning/back-compat shim needed.
- Commit messages in Russian (project convention), no `Co-Authored-By` trailer.

---

## Task 1: Collapse `server.Config`'s 7 callback fields into 2 interfaces

`pkg/server/server.go` currently has 7 `func(...)` fields on both `Config` and `Server` (`ApproveFn`, `ReviseFn`, `RetryFn`, `RetryHookFn`, `SkipHookFn`, `DialogAnswerFn`, `DialogCancelFn`), all wired from `cmd/afm/run.go` to `*orchestrator.Orchestrator` methods (two of them via inline closures because `Orchestrator` doesn't yet expose a matching method for dialog-cancel). This task replaces the 7 fields with 2 narrow interfaces that `*orchestrator.Orchestrator` satisfies directly.

**Files:**
- Modify: `pkg/orchestrator/control_api.go` (add `CancelDialog` method)
- Test: `pkg/orchestrator/control_api_test.go` (add `TestCancelDialog_FailsStage`)
- Create: `pkg/server/actions.go` (the two interfaces)
- Modify: `pkg/server/server.go:70-231` (`Server`/`Config` structs, `New`)
- Modify: `pkg/server/handlers.go` (7 call sites: `handleApprove`, `handleRevise`, `handleRetry`, `handleRetryHook`, `handleSkipHook`, `handleDialogAnswer`, `handleDialogCancel`)
- Modify: `pkg/server/handlers_test.go` (`setupTestServerWithWS` + the retry-hook/skip-hook/dialog tests that set callback fields directly)
- Modify: `cmd/afm/run.go:256-277` (`server.Config{...}` construction)

**Interfaces:**
- Consumes: `orchestrator.Orchestrator.{Approve(ctx, stageID) error, Revise(ctx, stageID, feedback string) error, Retry(ctx, stageID) error, RetryHook(stageID string) error, SkipHook(stageID string) error, NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error}` — all already exist with exactly the signatures the new interfaces need.
- Produces: `server.StageActions` (methods `Approve`, `Revise`, `Retry`) and `server.SecondaryActions` (methods `RetryHook`, `SkipHook`, `NotifyAnswer`, `CancelDialog`) — later tasks and `cmd/afm/run.go` wire `*orchestrator.Orchestrator` to both.

- [ ] **Step 1: Write the failing test for the new `CancelDialog` method**

`Orchestrator.FailStage(stageID, reason string)` already exists and is what today's `DialogCancelFn` closure calls (`orch.FailStage(stageID, "cancelled by user")`). `CancelDialog` just needs to be a real method with that behavior baked in, so the interface can require it directly instead of the caller building a closure.

Model the test on `TestApproveStage_DurableTransition` in `pkg/orchestrator/approve_test.go` (same manually-constructed `Orchestrator` pattern). Add to `pkg/orchestrator/control_api_test.go`:

```go
func TestCancelDialog_FailsStage(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stages := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentImplementation}}}
	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts:        Options{RunDir: dir, Stages: stages, Store: store},
		graph:       graph.NewGraph(stages),
		fsm:         bus.NewFSM(store),
		ui:          bus.NewUIBus(),
		critical:    cb,
		concurrency: concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{}, ""),
	}
	o.Trigger("a", bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger("a", bus.EvAskUser, bus.GuardCtx{}, "")

	if err := o.CancelDialog("a"); err != nil {
		t.Fatalf("CancelDialog returned error: %v", err)
	}

	rs, err := state.LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != state.StatusFailed {
		t.Fatalf("status = %q, want failed", rs.Stages["a"].Status)
	}
}
```

Add the missing imports to `control_api_test.go` if not already present: `"github.com/akopichin/afm/pkg/flow"`, `"github.com/akopichin/afm/pkg/orchestrator/graph"`, `"github.com/akopichin/afm/pkg/orchestrator/concurrency"`, `"github.com/akopichin/afm/pkg/state"` (check the existing `import` block first — `context` and `testing` are already there; add only what's missing).

- [ ] **Step 2: Run the test, confirm it fails to compile**

Run: `go test ./pkg/orchestrator/... -run TestCancelDialog_FailsStage -v`
Expected: compile error `o.CancelDialog undefined (type *Orchestrator has no field or method CancelDialog)`.

- [ ] **Step 3: Implement `CancelDialog`**

In `pkg/orchestrator/control_api.go`, add right after `FailStage` (currently lines 14-18):

```go
// CancelDialog fails a stage that's awaiting user input because the user
// cancelled the dialog from the dashboard. Thin wrapper so server.SecondaryActions
// can require a real method instead of the HTTP layer building a closure around
// FailStage with a hardcoded reason string.
func (o *Orchestrator) CancelDialog(stageID string) error {
	o.FailStage(stageID, "cancelled by user")
	return nil
}
```

- [ ] **Step 4: Run the test, confirm it passes**

Run: `go test ./pkg/orchestrator/... -run TestCancelDialog_FailsStage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/control_api.go pkg/orchestrator/control_api_test.go
git commit -m "feat(orchestrator): добавляем CancelDialog как отдельный метод вместо inline-closure"
```

- [ ] **Step 6: Define the two interfaces**

Create `pkg/server/actions.go`:

```go
package server

import "context"

// StageActions are the commands the dashboard can always trigger for any
// stage: approve/revise a plan, or retry a failed stage. Every production
// Config wires all three (there is exactly one caller, cmd/afm/run.go) —
// this interface has no meaningful "partially nil" state, unlike SecondaryActions.
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	Revise(ctx context.Context, stageID, feedback string) error
	Retry(ctx context.Context, stageID string) error
}

// SecondaryActions are dashboard commands that are optional as a group: hook
// retry/skip (only meaningful for stages with before/after hooks) and
// file-based dialog answer/cancel notification (the critical write to
// answer.json already happened before NotifyAnswer/CancelDialog are
// consulted — these are best-effort FSM/restart notifications, not the
// source of truth). A nil SecondaryActions on Config makes retry-hook/
// skip-hook respond 501 and makes dialog answer/cancel a silent no-op notify —
// same behavior as today's four independently-nilable Config fields, now
// grouped under one nil check instead of four.
type SecondaryActions interface {
	RetryHook(stageID string) error
	SkipHook(stageID string) error
	NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error
	CancelDialog(stageID string) error
}
```

- [ ] **Step 7: Wire the interfaces into `Server`/`Config`**

In `pkg/server/server.go`, replace the 7 callback fields on `Server` (lines 77-83):

```go
	approveFn        func(ctx context.Context, stageID string) error
	reviseFn         func(ctx context.Context, stageID, feedback string) error
	retryFn          func(ctx context.Context, stageID string) error
	retryHookFn      func(stageID string) error
	skipHookFn       func(stageID string) error
	dialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn   func(stageID string) error
```

with:

```go
	actions          StageActions      // never nil in practice — see StageActions doc
	secondary        SecondaryActions  // may be nil — see SecondaryActions doc
```

Replace the matching 7 fields on `Config` (lines 106-112):

```go
	ApproveFn        func(ctx context.Context, stageID string) error
	ReviseFn         func(ctx context.Context, stageID, feedback string) error
	RetryFn          func(ctx context.Context, stageID string) error
	RetryHookFn      func(stageID string) error
	SkipHookFn       func(stageID string) error
	DialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn   func(stageID string) error
```

with:

```go
	Actions   StageActions
	Secondary SecondaryActions
```

And in `New` (lines 143-149), replace the 7 assignments:

```go
		approveFn:        cfg.ApproveFn,
		reviseFn:         cfg.ReviseFn,
		retryFn:          cfg.RetryFn,
		retryHookFn:      cfg.RetryHookFn,
		skipHookFn:       cfg.SkipHookFn,
		dialogAnswerFn:   cfg.DialogAnswerFn,
		dialogCancelFn:   cfg.DialogCancelFn,
```

with:

```go
		actions:          cfg.Actions,
		secondary:        cfg.Secondary,
```

`context` stays imported (still used by `StageActions`' method signatures and `http.Request`'s context elsewhere in the file).

- [ ] **Step 8: Update the 7 call sites in `pkg/server/handlers.go`**

`handleApprove` (line 179): `s.approveFn(r.Context(), stageID)` → `s.actions.Approve(r.Context(), stageID)`

`handleRevise` (line 217): `s.reviseFn(r.Context(), stageID, req.Feedback)` → `s.actions.Revise(r.Context(), stageID, req.Feedback)`

`handleRetry` (line 243): `s.retryFn(r.Context(), stageID)` → `s.actions.Retry(r.Context(), stageID)`

`handleRetryHook` (lines 263-267):

```go
	if s.retryHookFn == nil {
		http.Error(w, "retry-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.retryHookFn(stageID); err != nil {
```

becomes:

```go
	if s.secondary == nil {
		http.Error(w, "retry-hook not supported", http.StatusNotImplemented)
		return
	}
	if err := s.secondary.RetryHook(stageID); err != nil {
```

`handleSkipHook` (lines 283-287): same substitution, `s.skipHookFn` → `s.secondary`, `s.skipHookFn(stageID)` → `s.secondary.SkipHook(stageID)`.

`handleDialogAnswer` (lines 505-509):

```go
	if s.dialogAnswerFn != nil {
		if err := s.dialogAnswerFn(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
```

becomes:

```go
	if s.secondary != nil {
		if err := s.secondary.NotifyAnswer(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
```

`handleDialogCancel` (lines 527-530):

```go
	if s.dialogCancelFn != nil {
		if err := s.dialogCancelFn(stageID); err != nil {
```

becomes:

```go
	if s.secondary != nil {
		if err := s.secondary.CancelDialog(stageID); err != nil {
```

- [ ] **Step 9: Add test doubles and update `pkg/server/handlers_test.go`**

Add near the top of the file (after `setupTestServerWithWS`, before `TestHandleStatus`):

```go
// fakeStageActions is the StageActions test double — every method is
// backed by an overridable func field; the zero value succeeds and does
// nothing, matching what most handler tests need.
type fakeStageActions struct {
	approve func(ctx context.Context, stageID string) error
	revise  func(ctx context.Context, stageID, feedback string) error
	retry   func(ctx context.Context, stageID string) error
}

func (f fakeStageActions) Approve(ctx context.Context, stageID string) error {
	if f.approve == nil {
		return nil
	}
	return f.approve(ctx, stageID)
}

func (f fakeStageActions) Revise(ctx context.Context, stageID, feedback string) error {
	if f.revise == nil {
		return nil
	}
	return f.revise(ctx, stageID, feedback)
}

func (f fakeStageActions) Retry(ctx context.Context, stageID string) error {
	if f.retry == nil {
		return nil
	}
	return f.retry(ctx, stageID)
}

// fakeSecondaryActions is the SecondaryActions test double. Tests that only
// care about one method (e.g. TestHandleRetryHook_Success) leave the rest nil
// — those methods simply aren't called by the code path under test.
type fakeSecondaryActions struct {
	retryHook    func(stageID string) error
	skipHook     func(stageID string) error
	notifyAnswer func(stageID, phase, qID, answer string, fromOptions bool) error
	cancelDialog func(stageID string) error
}

func (f fakeSecondaryActions) RetryHook(stageID string) error {
	if f.retryHook == nil {
		return nil
	}
	return f.retryHook(stageID)
}

func (f fakeSecondaryActions) SkipHook(stageID string) error {
	if f.skipHook == nil {
		return nil
	}
	return f.skipHook(stageID)
}

func (f fakeSecondaryActions) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if f.notifyAnswer == nil {
		return nil
	}
	return f.notifyAnswer(stageID, phase, qID, answer, fromOptions)
}

func (f fakeSecondaryActions) CancelDialog(stageID string) error {
	if f.cancelDialog == nil {
		return nil
	}
	return f.cancelDialog(stageID)
}
```

Update `setupTestServerWithWS` (lines 55-65): replace

```go
	srv := New(Config{
		Port:         0,
		RunDir:       runDir,
		Store:        store,
		UIBus:        uiBus,
		ApproveFn:    func(ctx context.Context, id string) error { return nil },
		ReviseFn:     func(ctx context.Context, id, fb string) error { return nil },
		RetryFn:      func(ctx context.Context, id string) error { return nil },
		WSPongWait:   pongWait,
		WSPingPeriod: pingPeriod,
	})
```

with:

```go
	srv := New(Config{
		Port:         0,
		RunDir:       runDir,
		Store:        store,
		UIBus:        uiBus,
		Actions:      fakeStageActions{},
		WSPongWait:   pongWait,
		WSPingPeriod: pingPeriod,
	})
```

(`fakeStageActions{}`'s zero value already no-ops successfully, matching the old three inline closures.)

Update the retry-hook/skip-hook tests (lines 383-464) — replace direct field assignment with a `Secondary` fake assigned onto the already-built `srv`:

`TestHandleRetryHook_Success` (line 386): `srv.retryHookFn = func(stageID string) error { called = stageID; return nil }` → `srv.secondary = fakeSecondaryActions{retryHook: func(stageID string) error { called = stageID; return nil }}`

`TestHandleSkipHook_Success` (line 406): same pattern with `skipHook:`.

`TestHandleRetryHook_FnReturnsError` (line 425) and `TestHandleSkipHook_FnReturnsError` (line 440): same pattern, function body unchanged (still returns the error), just wrapped in `fakeSecondaryActions{retryHook: ...}` / `fakeSecondaryActions{skipHook: ...}`.

`TestHandleRetryHook_NotConfigured` (line 453-464): no change needed — `srv.secondary` stays nil (the comment "retryHookFn intentionally left nil" becomes "secondary intentionally left nil"), the 501 assertion still holds because `handleRetryHook` now checks `s.secondary == nil`.

Update the two `Config{...}` literals with `DialogAnswerFn`/`DialogCancelFn` (around lines 596, 673, 791 — search for `DialogAnswerFn:` and `DialogCancelFn:` to find exact current line numbers after step 8's edits shift them): replace

```go
		DialogAnswerFn: func(s, p, q, a string, fo bool) error {
			// ... existing body ...
		},
```

with a `Secondary: fakeSecondaryActions{notifyAnswer: func(s, p, q, a string, fo bool) error { /* same existing body */ }}` field, and similarly `DialogCancelFn: func(id string) error { cancelled = id; return nil }` becomes `Secondary: fakeSecondaryActions{cancelDialog: func(id string) error { cancelled = id; return nil }}`. Where a test sets both `DialogAnswerFn` and needs `Actions` too (any test also exercising approve/revise/retry), add `Actions: fakeStageActions{}` alongside.

- [ ] **Step 10: Run the full server package test suite**

Run: `go test ./pkg/server/... -v -race`
Expected: all tests PASS, including `TestHandleRetryHook_NotConfigured` (501), `TestHandleRetryHook_Success`/`TestHandleSkipHook_Success` (200 + callback invoked), and the dialog-answer/cancel tests.

- [ ] **Step 11: Update `cmd/afm/run.go`'s `server.Config{...}` construction**

Replace lines 256-277 (the `server.New(server.Config{...})` block):

```go
				srv := server.New(server.Config{
					Port:             cfg.Server.GetPort(),
					RunDir:           runDir,
					Description:      f.Description,
					StageInteractive: stageInteractive,
					StageAutoApprove: stageAutoApprove,
					Store:            store,
					Theme:            cfg.EffectiveTheme(),
					SkinDir:          cfg.SkinDir,
					UIBus:            orch.UIBus(),
					ApproveFn:        orch.Approve,
					ReviseFn:         orch.Revise,
					RetryFn:          orch.Retry,
					RetryHookFn:      orch.RetryHook,
					SkipHookFn:       orch.SkipHook,
					DialogAnswerFn: func(stageID, phase, qID, answer string, fromOptions bool) error {
						return orch.NotifyAnswer(stageID, phase, qID, answer, fromOptions)
					},
					DialogCancelFn: func(stageID string) error {
						orch.FailStage(stageID, "cancelled by user")
						return nil
```

with:

```go
				srv := server.New(server.Config{
					Port:             cfg.Server.GetPort(),
					RunDir:           runDir,
					Description:      f.Description,
					StageInteractive: stageInteractive,
					StageAutoApprove: stageAutoApprove,
					Store:            store,
					Theme:            cfg.EffectiveTheme(),
					SkinDir:          cfg.SkinDir,
					UIBus:            orch.UIBus(),
					Actions:          orch,
					Secondary:        orch,
```

(Read the rest of the original block — check for a closing `},` and any trailing fields like `WSPongWait` after line 277 — preserve them unchanged; only the callback-field lines are replaced. `*orchestrator.Orchestrator` (`orch`'s type) now satisfies both `server.StageActions` and `server.SecondaryActions` directly via its existing `Approve`/`Revise`/`Retry`/`RetryHook`/`SkipHook`/`NotifyAnswer` methods plus the new `CancelDialog` from Step 3 — no closures needed.)

Add compile-time assertions right after the `server.Config{...}` block (or near the top of `run.go` in a `var _` block) so a future signature drift fails the build immediately instead of surfacing as a runtime `nil` panic:

```go
var (
	_ server.StageActions   = (*orchestrator.Orchestrator)(nil)
	_ server.SecondaryActions = (*orchestrator.Orchestrator)(nil)
)
```

- [ ] **Step 12: Build and test everything**

Run: `go build ./... && go test ./... -race`
Expected: builds cleanly, all tests PASS.

- [ ] **Step 13: Lint**

Run: `make lint`
Expected: no new findings (in particular `tools/setstatuslinter` should stay clean — this task doesn't touch `Store.Apply` call sites).

- [ ] **Step 14: Commit**

```bash
git add pkg/server/actions.go pkg/server/server.go pkg/server/handlers.go pkg/server/handlers_test.go cmd/afm/run.go
git commit -m "refactor(server): заменяем 7 callback-полей Config на 2 интерфейса StageActions/SecondaryActions"
```

---

## Task 2: `StageView` read-model for `/api/status`

`pkg/server/handlers.go`'s `handleStatus` currently scans the filesystem twice (once for `autonomous.flag`, once for `*.dialog.jsonl` across all phases) and returns a `statusResponse` that embeds `state.RunState` (a `map[string]StageState` under `stages`) plus four more parallel `map[string]bool` fields (`stage_interactive`, `stage_autonomous`, `stage_has_dialog`, `stage_auto_approve`). The frontend (`use-status.ts`) then re-joins all five by stage id to build its `Stage[]`, and separately (`App.tsx`) recomputes `showPlan`/`showDialog` visibility booleans from those same raw flags. This task moves both the join and the visibility computation to the server, producing one ordered `[]StageView` with `show_plan`/`show_dialog` already computed.

**Files:**
- Create: `pkg/server/stageview.go`
- Modify: `pkg/server/handlers.go:24-73` (`statusResponse` type + `handleStatus`)
- Modify: `pkg/server/handlers_test.go` (`TestHandleStatus`, `TestHandleStatus_IncludesStageNames`, `TestHandleStatus_IncludesInteractiveAndAutonomous`, `TestHandleStatus_IncludesHasDialog`, and any other test decoding `statusResponse`/`state.RunState` from `/api/status`)

**Interfaces:**
- Consumes: `state.RunState{FlowName, StartedAt, StageOrder []string, StageNames map[string]string, Stages map[string]StageState, LastSeq uint64, IdleAccumulatedMs, BackoffAccumulatedMs int64}`, `state.RunState.IdleSince() *time.Time`, `state.RunState.BackoffOpenSince() []time.Time` (all unchanged, from `pkg/state/state.go`).
- Produces: `server.StageView` (exported for the sake of the handler test decoding it) with fields `ID, Name string; Status state.StageStatus; UpdatedAt time.Time; Interactive, Autonomous, AutoApprove, HasDialog, ShowPlan, ShowDialog bool` (json tags: `id, name, status, updated_at, interactive, autonomous, auto_approve, has_dialog, show_plan, show_dialog`), and `server.buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove map[string]bool) []StageView`.

- [ ] **Step 1: Write the failing test for `buildStageViews`**

Create `pkg/server/stageview_test.go`:

```go
package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestBuildStageViews_OrdersAndComputesCapabilities(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// "b" is autonomous (has autonomous.flag) and failed → plan panel must
	// still show (Retry lives there), dialog panel shows too (autonomous track
	// is always dialog-capable).
	if err := os.WriteFile(filepath.Join(runDir, "b", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	rs := state.RunState{
		StageOrder: []string{"b", "a"}, // deliberately not alphabetical — order must be preserved
		StageNames: map[string]string{"a": "Stage A"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusPending},
			"b": {Status: state.StatusFailed},
		},
	}

	views := buildStageViews(rs, runDir, map[string]bool{"a": true}, map[string]bool{"a": true})

	if len(views) != 2 || views[0].ID != "b" || views[1].ID != "a" {
		t.Fatalf("order not preserved: %+v", views)
	}

	a, b := views[1], views[0]

	if a.Name != "Stage A" || !a.Interactive || !a.AutoApprove {
		t.Errorf("stage a view wrong: %+v", a)
	}
	if !a.ShowPlan {
		t.Errorf("stage a (not autonomous): ShowPlan should be true, got %+v", a)
	}
	if a.ShowDialog != true { // interactive:true → dialog shown
		t.Errorf("stage a: ShowDialog should be true (interactive), got %+v", a)
	}

	if !b.Autonomous {
		t.Errorf("stage b: Autonomous should be true, got %+v", b)
	}
	if !b.ShowPlan {
		t.Errorf("stage b (autonomous but failed): ShowPlan should still be true, got %+v", b)
	}
	if !b.ShowDialog {
		t.Errorf("stage b (autonomous): ShowDialog should be true, got %+v", b)
	}
}
```

- [ ] **Step 2: Run the test, confirm it fails**

Run: `go test ./pkg/server/... -run TestBuildStageViews -v`
Expected: FAIL — `undefined: buildStageViews` (and `StageView` unresolved).

- [ ] **Step 3: Implement `StageView` and `buildStageViews`**

Create `pkg/server/stageview.go`:

```go
package server

import (
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// StageView is the per-stage read model served by GET /api/status. It joins
// state.StageState (event-log-derived) with the flow's static config
// (Interactive/AutoApprove) and two filesystem-derived runtime flags
// (Autonomous/HasDialog), and precomputes the two dashboard visibility
// capabilities (ShowPlan/ShowDialog) that pkg/web/dashboard's App.tsx used to
// recompute client-side from the same four raw flags — one source of truth
// for "can this stage's plan/dialog panel be shown" instead of two
// (Go here, TypeScript there) that had to be kept in sync by hand.
type StageView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Status      state.StageStatus `json:"status"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Interactive bool              `json:"interactive"`
	Autonomous  bool              `json:"autonomous"`
	AutoApprove bool              `json:"auto_approve"`
	HasDialog   bool              `json:"has_dialog"`
	// ShowPlan/ShowDialog — the two visibility capabilities pkg/web/dashboard's
	// App.tsx computed from Autonomous/Status/Interactive/HasDialog. See that
	// file's showPlan/showDialog comment (removed in the frontend task of this
	// same plan) for the original rationale this mirrors exactly.
	ShowPlan   bool `json:"show_plan"`
	ShowDialog bool `json:"show_dialog"`
}

// buildStageViews joins rs.Stages (event-log state) with the flow's static
// interactive/auto_approve config and two on-disk runtime flags
// (autonomous.flag presence, any <phase>.dialog.jsonl presence) into one
// ordered slice, following rs.StageOrder. Replaces handleStatus's previous
// five-parallel-map construction.
func buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove map[string]bool) []StageView {
	views := make([]StageView, 0, len(rs.StageOrder))
	for _, id := range rs.StageOrder {
		st := rs.Stages[id]
		autonomous := stageIsAutonomous(runDir, id)
		hasDialog := stageHasDialog(runDir, id)
		interactive := stageInteractive[id]
		showPlan := !autonomous || st.Status == state.StatusFailed
		showDialog := interactive || autonomous || hasDialog

		views = append(views, StageView{
			ID:          id,
			Name:        rs.StageNames[id],
			Status:      st.Status,
			UpdatedAt:   st.UpdatedAt,
			Interactive: interactive,
			Autonomous:  autonomous,
			AutoApprove: stageAutoApprove[id],
			HasDialog:   hasDialog,
			ShowPlan:    showPlan,
			ShowDialog:  showDialog,
		})
	}
	return views
}

func stageIsAutonomous(runDir, stageID string) bool {
	_, err := os.Stat(filepath.Join(runDir, stageID, "autonomous.flag"))
	return err == nil
}

func stageHasDialog(runDir, stageID string) bool {
	for _, p := range flow.Phases() {
		if _, err := os.Stat(filepath.Join(runDir, stageID, string(p)+".dialog.jsonl")); err == nil {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the test, confirm it passes**

Run: `go test ./pkg/server/... -run TestBuildStageViews -v`
Expected: PASS.

- [ ] **Step 5: Rewrite `statusResponse` and `handleStatus` to use `buildStageViews`**

In `pkg/server/handlers.go`, replace the `statusResponse` type (lines 30-42):

```go
type statusResponse struct {
	state.RunState
	Description      string          `json:"description,omitempty"`
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
	StageHasDialog   map[string]bool `json:"stage_has_dialog,omitempty"`
	StageAutoApprove map[string]bool `json:"stage_auto_approve,omitempty"`
	IdleSince        *time.Time  `json:"idle_since,omitempty"`
	BackoffOpenSince []time.Time `json:"backoff_open_since,omitempty"`
}
```

with:

```go
// statusResponse is GET /api/status's wire shape: run-level fields plus one
// ordered []StageView (see stageview.go) instead of five parallel per-stage
// maps the frontend used to re-join by id.
type statusResponse struct {
	FlowName             string      `json:"flow_name"`
	StartedAt            time.Time   `json:"started_at"`
	Description          string      `json:"description,omitempty"`
	Stages               []StageView `json:"stages"`
	LastSeq              uint64      `json:"last_seq"`
	IdleAccumulatedMs    int64       `json:"idle_accumulated_ms"`
	IdleSince            *time.Time  `json:"idle_since,omitempty"`
	BackoffAccumulatedMs int64       `json:"backoff_accumulated_ms"`
	BackoffOpenSince     []time.Time `json:"backoff_open_since,omitempty"`
}
```

Replace `handleStatus` (lines 44-73):

```go
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()
	autonomous := make(map[string]bool, len(rs.Stages))
	for id := range rs.Stages {
		if _, err := os.Stat(filepath.Join(s.runDir, id, "autonomous.flag")); err == nil {
			autonomous[id] = true
		}
	}
	hasDialog := make(map[string]bool, len(rs.Stages))
	for id := range rs.Stages {
		for _, p := range flow.Phases() {
			if _, err := os.Stat(filepath.Join(s.runDir, id, string(p)+".dialog.jsonl")); err == nil {
				hasDialog[id] = true
				break
			}
		}
	}
	resp := statusResponse{
		RunState:         rs,
		Description:      s.Description,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
		StageHasDialog:   hasDialog,
		StageAutoApprove: s.stageAutoApprove,
		IdleSince:        rs.IdleSince(),
		BackoffOpenSince: rs.BackoffOpenSince(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
```

with:

```go
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()
	resp := statusResponse{
		FlowName:             rs.FlowName,
		StartedAt:            rs.StartedAt,
		Description:          s.Description,
		Stages:               buildStageViews(rs, s.runDir, s.stageInteractive, s.stageAutoApprove),
		LastSeq:              rs.LastSeq,
		IdleAccumulatedMs:    rs.IdleAccumulatedMs,
		IdleSince:            rs.IdleSince(),
		BackoffAccumulatedMs: rs.BackoffAccumulatedMs,
		BackoffOpenSince:     rs.BackoffOpenSince(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
```

Remove now-unused imports from `handlers.go` if `goimports`/the compiler flags them (`os`, `path/filepath`, `flow` may still be used elsewhere in the same file by other handlers — check before removing; `handleLog`/`handlePlan` still use `os`/`filepath`/`flow`, so those imports stay).

- [ ] **Step 6: Update the four affected tests in `pkg/server/handlers_test.go`**

`TestHandleStatus` (lines 69-85): decodes into `state.RunState` and checks `rs.Stages[testStageID]` exists (a map lookup) — the wire format is no longer a flat `RunState`. Replace the decode target and assertion:

```go
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, sv := range resp.Stages {
		if sv.ID == testStageID {
			found = true
		}
	}
	if !found {
		t.Error("stage s1 missing from status")
	}
```

`TestHandleStatus_IncludesStageNames` (lines 87-105): same decode-target change, then find the stage by id and check `.Name`:

```go
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got string
	for _, sv := range resp.Stages {
		if sv.ID == testStageID {
			got = sv.Name
		}
	}
	if got != "Backend Stage" {
		t.Errorf("stage name = %q, want %q", got, "Backend Stage")
	}
```

`TestHandleStatus_IncludesInteractiveAndAutonomous` (lines 107-135): replace the `resp.Stages[testStageID]` / `resp.StageInteractive[testStageID]` / `resp.StageAutonomous[testStageID]` map lookups (lines 126-134) with a find-by-id over the `[]StageView` and check `.Interactive`/`.Autonomous` on the found element:

```go
	var found *StageView
	for i := range resp.Stages {
		if resp.Stages[i].ID == testStageID {
			found = &resp.Stages[i]
		}
	}
	if found == nil {
		t.Fatal("stage missing from status")
	}
	if !found.Interactive {
		t.Errorf("Interactive = false, want true")
	}
	if !found.Autonomous {
		t.Errorf("Autonomous = false, want true")
	}
```

`TestHandleStatus_IncludesHasDialog` (starting line 137): same find-by-id substitution, checking `.HasDialog` instead of `resp.StageHasDialog[testStageID]`. Read the rest of that test (it continues past line 140) before editing — apply the same map→slice-lookup pattern to whatever assertion follows.

Search the rest of `handlers_test.go` for any other `resp.Stage*[` or decode into bare `state.RunState` from a `/api/status` response and apply the same fix — `grep -n "StageInteractive\|StageAutonomous\|StageHasDialog\|StageAutoApprove" pkg/server/handlers_test.go` to find all remaining call sites after the edits above.

- [ ] **Step 7: Run the server package tests**

Run: `go test ./pkg/server/... -v -race`
Expected: all PASS.

- [ ] **Step 8: Build, full test suite, lint**

Run: `go build ./... && go test ./... -race && make lint`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add pkg/server/stageview.go pkg/server/stageview_test.go pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "refactor(server): заменяем 5 параллельных map статуса на один упорядоченный []StageView с show_plan/show_dialog"
```

---

## Task 3: Frontend consumes `StageView` directly — kill `NO_STAGE`, extract API client

With Task 2 done, `/api/status` returns `{ ..., stages: [{id, name, status, updated_at, interactive, autonomous, auto_approve, has_dialog, show_plan, show_dialog}, ...] }` instead of `stages` (map) + `stage_order` + `stage_names` + `stage_interactive` + `stage_autonomous` + `stage_has_dialog` + `stage_auto_approve`. This task updates `use-status.ts` to consume the new array directly (deleting the parallel-map join logic), adds `showPlan`/`showDialog` to the `Stage` type, extracts the dashboard's mutating `fetch()` calls (approve/revise/retry/retry-hook/skip-hook/dialog-answer/dialog-cancel) into one `api/run-client.ts`, and removes `App.tsx`'s `NO_STAGE` sentinel by making `PlanPanel`/`DialogChannel` accept `Stage | null`.

**Files:**
- Modify: `pkg/web/dashboard/src/types/stage.ts:21-30` (add `showPlan`/`showDialog` to `Stage`)
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts` (rewrite `normalizeStatus`/`toStage`/`resolveOrder`)
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts` (update fixtures to the new wire shape)
- Create: `pkg/web/dashboard/src/api/run-client.ts`
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx` (accept `Stage | null`, use `run-client.ts`)
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx` (add a null-stage test, update `makeStage`)
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx` (accept `Stage | null`, use `run-client.ts`)
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx` (add a null-stage test)
- Modify: `pkg/web/dashboard/src/app/App.tsx` (remove `NO_STAGE`, use `selectedStage?.showPlan`/`showDialog`, use `run-client.ts` for the revise-note submit)
- Modify: `pkg/web/dashboard/src/app/App.test.tsx` (adjust to whatever `makeStage`/status fixture it uses — mirror Task 3's `use-status.test.ts` fixture change)

**Interfaces:**
- Consumes: Task 2's `GET /api/status` wire shape (`{flow_name, started_at, description?, stages: StageView[], last_seq, idle_accumulated_ms, idle_since?, backoff_accumulated_ms, backoff_open_since}` where each `StageView` is `{id, name?, status, updated_at, interactive, autonomous, auto_approve, has_dialog, show_plan, show_dialog}`).
- Produces: `Stage` type gains `showPlan: boolean; showDialog: boolean`. `api/run-client.ts` exports `approveStage(stageId: string): Promise<void>`, `reviseStage(stageId: string, feedback: string): Promise<void>`, `retryStage(stageId: string): Promise<void>`, `retryHookStage(stageId: string): Promise<void>`, `skipHookStage(stageId: string): Promise<void>`, `answerDialog(stageId: string, phase: string, id: string, answer: string, fromOptions: boolean): Promise<void>`, `cancelDialog(stageId: string): Promise<void>`.

- [ ] **Step 1: Write the failing test for the new `normalizeStatus` wire shape**

In `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts`, find the existing fixtures building the old shape (`stages: {}`, `stage_order`, `stage_names`, `stage_interactive`, etc. — read the file first to find the exact test names) and add a new test using the Task-2 wire shape:

```ts
test('normalizeStatus: reads the ordered stages array directly (no per-id maps)', () => {
  const raw = {
    flow_name: 'demo',
    started_at: '2026-08-10T00:00:00Z',
    stages: [
      { id: 'b', name: 'Stage B', status: 'running', updated_at: '2026-08-10T00:01:00Z',
        interactive: false, autonomous: true, auto_approve: false, has_dialog: false,
        show_plan: false, show_dialog: true },
      { id: 'a', name: '', status: 'pending', updated_at: '',
        interactive: true, autonomous: false, auto_approve: true, has_dialog: false,
        show_plan: true, show_dialog: true },
    ],
    idle_accumulated_ms: 0,
    backoff_accumulated_ms: 0,
  }

  const status = normalizeStatus(raw)

  expect(status.stages.map((s) => s.id)).toEqual(['b', 'a']) // order preserved, not sorted
  expect(status.stages[0]).toMatchObject({
    id: 'b', name: 'Stage B', status: 'running', autonomous: true, showPlan: false, showDialog: true,
  })
  expect(status.stages[1]).toMatchObject({
    id: 'a', interactive: true, autoApprove: true, showPlan: true, showDialog: true,
  })
})
```

- [ ] **Step 2: Run the test, confirm it fails**

Run: `cd pkg/web/dashboard && npm test -- use-status`
Expected: FAIL — old `normalizeStatus` reads `obj.stages` as a map and `obj.stage_order`/`obj.stage_interactive`/etc., so `status.stages` comes back empty against the new array-shaped `raw.stages`.

- [ ] **Step 3: Rewrite `normalizeStatus` for the array wire format**

Replace `use-status.ts`'s `normalizeStatus`, `resolveOrder`, and `toStage` (lines 96-159) with:

```ts
export function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}

  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  const startedAt = typeof obj.started_at === 'string' ? obj.started_at : ''
  const description = typeof obj.description === 'string' ? obj.description : undefined

  const stages: Stage[] = Array.isArray(obj.stages) ? obj.stages.map(toStage).filter((s): s is Stage => s !== null) : []

  const idleAccumulatedMs = typeof obj.idle_accumulated_ms === 'number' ? obj.idle_accumulated_ms : 0
  const idleSince = typeof obj.idle_since === 'string' ? obj.idle_since : null
  const backoffAccumulatedMs = typeof obj.backoff_accumulated_ms === 'number' ? obj.backoff_accumulated_ms : 0
  const backoffOpenSince = Array.isArray(obj.backoff_open_since)
    ? obj.backoff_open_since.filter((v): v is string => typeof v === 'string')
    : []

  return { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince }
}

function toStage(raw: unknown): Stage | null {
  const obj = isRecord(raw) ? raw : null
  if (obj === null || typeof obj.id !== 'string') return null

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof obj.name === 'string' ? obj.name : ''

  return {
    id: obj.id,
    name,
    status,
    updatedAt,
    interactive: obj.interactive === true,
    autonomous: obj.autonomous === true,
    autoApprove: obj.auto_approve === true,
    hasDialog: obj.has_dialog === true,
    showPlan: obj.show_plan === true,
    showDialog: obj.show_dialog === true,
  }
}
```

Delete the now-unused `resolveOrder` function and the `STAGE_STATUSES` import stays (still used by `isStageStatus`). Remove the `Stage` import's now-stale usage note comment above `normalizeStatus` (the "Сырой ответ... normalizeStatus" comment at lines 40-41 describes the old shape — update it to describe the new one: "Сырой ответ GET /api/status приводится к FlowStatus в normalizeStatus: stages — уже упорядоченный массив StageView (см. pkg/server/stageview.go), маппинг 1:1 через toStage.").

- [ ] **Step 4: Run the test, confirm it passes; run the full `use-status` suite**

Run: `cd pkg/web/dashboard && npm test -- use-status`
Expected: PASS for the new test and all pre-existing ones you kept (delete/update any old test whose fixture used the removed map+parallel-arrays shape — same find-and-replace as the Go test updates in Task 2 Step 6, just in TS: old fixtures with `stages: {a: {...}}, stage_order: [...], stage_interactive: {...}` become `stages: [{id: 'a', ...flattened fields...}]`).

- [ ] **Step 5: Add `showPlan`/`showDialog` to the `Stage` type**

In `pkg/web/dashboard/src/types/stage.ts`, extend the `Stage` type (lines 21-30):

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
}
```

- [ ] **Step 6: Commit the wire-format change**

```bash
cd pkg/web/dashboard && npm run build
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/web/dashboard/src/types/stage.ts pkg/web/dashboard/src/hooks/use-status/use-status.ts pkg/web/dashboard/src/hooks/use-status/use-status.test.ts
git commit -m "refactor(dashboard): normalizeStatus читает готовый []StageView вместо пяти параллельных map"
```

- [ ] **Step 7: Extract the mutating dashboard commands into `api/run-client.ts`**

Create `pkg/web/dashboard/src/api/run-client.ts`:

```ts
// Мутирующие команды дашборда к afm-серверу (approve/revise/retry/dialog).
// Вынесено из PlanPanel/DialogChannel/App, которые раньше дублировали один и
// тот же postJson-паттерн по месту использования.
async function postJson(url: string, body: unknown): Promise<void> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })

  if (!response.ok) {
    throw new Error(`POST ${url} -> ${response.status}`)
  }
}

function stageUrl(stageId: string, action: string): string {
  return `/api/stages/${encodeURIComponent(stageId)}/${action}`
}

export async function approveStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'approve'), null)
}

export async function reviseStage(stageId: string, feedback: string): Promise<void> {
  await postJson(stageUrl(stageId, 'revise'), { feedback })
}

export async function retryStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'retry'), null)
}

export async function retryHookStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'retry-hook'), null)
}

export async function skipHookStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'skip-hook'), null)
}

export async function answerDialog(
  stageId: string,
  phase: string,
  id: string,
  answer: string,
  fromOptions: boolean,
): Promise<void> {
  await postJson(stageUrl(stageId, 'dialog/answer'), { id, phase, answer, from_options: fromOptions })
}

export async function cancelDialog(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'dialog/cancel'), null)
}
```

- [ ] **Step 8: Wire `PlanPanel.tsx` to `run-client.ts` and to `Stage | null`**

In `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`:

Replace the local `postJson` function (lines 406-416) — delete it, add the import instead:

```ts
import { approveStage, reviseStage, retryStage, retryHookStage, skipHookStage } from '../../api/run-client'
```

Change the prop type (line 8-11) and the `stage.id === ''` sentinel check (line 45) to a null check:

```ts
type PlanPanelProps = {
  stage: Stage | null
  attention?: boolean
}
```

```ts
export function PlanPanel({ stage, attention = false }: PlanPanelProps): ReactElement {
```

The body currently reads `stage.status`/`stage.id`/`stage.autoApprove` unconditionally in several places (lines 30-35, the `useEffect` at 42-75, and the five action functions at 129-182). Add an early return in the component right after the hooks that must always run (React rules of hooks — all `useState` calls at the top stay unconditional), then guard the derived values:

Change lines 30-35 from:

```ts
  const isReview = stage.status === 'awaiting_approval'
  const showActions = isReview && !stage.autoApprove
  const showAutoApprovedBadge = stage.autoApprove && planMarkdown.trim() !== ''
  const showRetry = stage.status === 'failed'
  const showHookFailed = stage.status === 'hook_failed'
  const commentCount = Object.keys(comments).length
```

to:

```ts
  const isReview = stage?.status === 'awaiting_approval'
  const showActions = isReview && stage !== null && !stage.autoApprove
  const showAutoApprovedBadge = stage !== null && stage.autoApprove && planMarkdown.trim() !== ''
  const showRetry = stage?.status === 'failed'
  const showHookFailed = stage?.status === 'hook_failed'
  const commentCount = Object.keys(comments).length
```

Change the `useEffect`'s guard (line 42-45) from `if (stage.id === '') return` to `if (stage === null) return` (leave everything else in the effect body unchanged — `stage.id`/`stage.status` inside are now only reached when `stage` is non-null, which TypeScript can't narrow across the closure boundary automatically; capture it once at the top of the effect: `const current = stage; if (current === null) return`, then use `current.id`/`current.status` for the rest of the effect body instead of `stage.id`/`stage.status`). Update the dependency array from `[stage.id, stage.status]` to `[stage?.id, stage?.status]`.

Each of the five action functions (`approve`, `sendRevision`, `retry`, `retryHook`, `skipHook`, lines 129-182) is only reachable from `onClick` handlers on buttons that are themselves gated behind `showActions`/`showRetry`/`showHookFailed` (all `false` when `stage === null`), so they're dead code when `stage` is null — add a defensive `if (stage === null) return` as their first line and replace the `postJson(...)` calls with the extracted functions:

```ts
  async function approve() {
    if (stage === null) return
    flashButton('approve')
    setBusy(true)
    try {
      await approveStage(stage.id)
    } finally {
      setBusy(false)
    }
  }
```

(repeat the same `if (stage === null) return` + swap-in pattern for `sendRevision` → `reviseStage(stage.id, feedback)`, `retry` → `retryStage(stage.id)`, `retryHook` → `retryHookStage(stage.id)`, `skipHook` → `skipHookStage(stage.id)`).

- [ ] **Step 9: Add the null-stage test for `PlanPanel`**

In `PlanPanel.test.tsx`, add:

```ts
test('stage=null: renders the panel shell without fetching or crashing', () => {
  const fetchSpy = vi.spyOn(globalThis, 'fetch')

  const { container } = render(<PlanPanel stage={null} />)

  expect(fetchSpy).not.toHaveBeenCalled()
  expect(container.querySelector('#plan-empty')).not.toBeNull()
  expect(container.querySelector('#actions-section')).toBeNull()
  expect(container.querySelector('#retry-section')).toBeNull()
})
```

- [ ] **Step 10: Run `PlanPanel` tests**

Run: `cd pkg/web/dashboard && npm test -- PlanPanel`
Expected: all PASS, including the new null-stage test.

- [ ] **Step 11: Wire `DialogChannel.tsx` to `run-client.ts` and to `Stage | null`**

Read the full file first (only partial content was captured during planning — the `stage.` usages at lines 67, 79, 100, 103-109, 147, 176, 201, 253 are known reference points). Apply the same pattern as Step 8: change the prop type to `stage: Stage | null`, change the `stage.id === ''` sentinel guard (line 67) to `stage === null`, capture `stage` into a local `const current = stage` at the top of each effect/handler that needs null-narrowing, replace the three `postJson` call sites (lines 176, 201, 253 — dialog answer/answer-custom/cancel) with `answerDialog(...)`/`cancelDialog(...)` imported from `../../api/run-client`, and delete `DialogChannel.tsx`'s own copy of `postJson` if it has one (check — `PlanPanel.tsx` had its own; `DialogChannel.tsx` may import a shared one or duplicate it, confirm before editing). The `hasContent` computation (line 109) already reads `stage.hasDialog`/`stage.status` — guard it as `stage !== null && (entries.length > 0 || stage.status === 'awaiting_user_input' || stage.hasDialog)`.

- [ ] **Step 12: Add the null-stage test for `DialogChannel`, run the suite**

Mirror Step 9's test shape (render with `stage={null}`, assert no fetch call, assert the panel renders its empty state without crashing — check what `DialogChannel`'s empty-state DOM looks like by reading the component's render return before writing the assertion).

Run: `cd pkg/web/dashboard && npm test -- DialogChannel`
Expected: all PASS.

- [ ] **Step 13: Remove `NO_STAGE` from `App.tsx`**

Replace lines 97-129:

```ts
  // Панели (PlanPanel/DialogChannel) требуют Stage, а не Stage | null. Sentinel
  // NO_STAGE нужен только на случай, когда стадия не выбрана: ...
  const NO_STAGE: Stage = {
    id: '',
    name: '',
    status: 'pending',
    updatedAt: '',
    interactive: false,
    autonomous: false,
    autoApprove: false,
    hasDialog: false,
  }
  const stageForPanels = selectedStage ?? NO_STAGE

  // Видимость панелей для выбранной стадии. Когда стадия не выбрана — показываем обе
  // (нейтральное состояние). plan скрыт у автономной стадии (нет plan.md) — КРОМЕ
  // случая failed: ...
  const showPlan = selectedStage === null || !selectedStage.autonomous || selectedStage.status === 'failed'
  const showDialog =
    selectedStage === null || selectedStage.interactive || selectedStage.autonomous || selectedStage.hasDialog
```

with:

```ts
  // showPlan/showDialog capabilities are computed server-side per stage (see
  // pkg/server/stageview.go's StageView.ShowPlan/ShowDialog) — the client only
  // adds the "nothing selected → show both, neutral state" rule on top.
  const showPlan = selectedStage === null || selectedStage.showPlan
  const showDialog = selectedStage === null || selectedStage.showDialog
```

Update the JSX (lines 240-241) from:

```tsx
            plan={showPlan ? <PlanPanel stage={stageForPanels} attention={attention.kind === 'plan'} /> : null}
            dialog={showDialog ? <DialogChannel stage={stageForPanels} attention={attention.kind === 'dialog'} /> : null}
```

to:

```tsx
            plan={showPlan ? <PlanPanel stage={selectedStage} attention={attention.kind === 'plan'} /> : null}
            dialog={showDialog ? <DialogChannel stage={selectedStage} attention={attention.kind === 'dialog'} /> : null}
```

Update `handleSubmitNote` (lines 35-61) to use the extracted client instead of its own inline `fetch`:

```ts
  async function handleSubmitNote(note: string): Promise<void> {
    if (noteModalStageId === null) return

    try {
      await reviseStage(noteModalStageId, note)
      setNoteModalStageId(null)
    } catch (err) {
      console.error('Failed to submit agent note:', err)
    }
  }
```

(keep the existing comment above the `catch` block explaining why the modal stays open on failure — it's still accurate.) Add `import { reviseStage } from '../api/run-client'` to the top imports. Remove `import type { Stage } from '../types'` if `Stage` is no longer referenced directly in `App.tsx` after removing `NO_STAGE` (check — `Stage['status']` is still used at line 141 for `prevSelectedStatus`, so keep the import).

- [ ] **Step 14: Update `App.test.tsx` and run it**

Read `App.test.tsx` first to find its status-fixture shape (it likely mocks `fetch('/api/status')` with the old shape, same as `use-status.test.ts` did before Step 3-4) and update it to the new `stages: StageView[]` wire format, matching Step 3's fixture pattern. Add `show_plan`/`show_dialog` to every fixture stage object.

Run: `cd pkg/web/dashboard && npm test -- App`
Expected: all PASS.

- [ ] **Step 15: Full frontend build + test suite**

Run: `cd pkg/web/dashboard && npm run build && npm test`
Expected: build succeeds, all tests PASS (report says 214 tests currently pass across 26 files — expect the same count minus/plus the tests added/removed in this task).

- [ ] **Step 16: Build the Go binary (embeds the dashboard) and run the Go suite once more end-to-end**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make build && go test ./... -race`
Expected: clean.

- [ ] **Step 17: Commit**

```bash
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/components/plan-panel pkg/web/dashboard/src/components/dialog-channel pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/src/app/App.test.tsx
git commit -m "refactor(dashboard): убираем NO_STAGE sentinel, панели принимают Stage | null, сетевые команды — в api/run-client.ts"
```

---

## Task 4: Generate the frontend `StageStatus` union from the Go source of truth

Today `pkg/state/state.go`'s `StageStatus` consts and `pkg/web/dashboard/src/types/stage.ts`'s `STAGE_STATUSES` array are two hand-maintained lists that must be kept in sync manually — the report's exact "next status addition requires updating Go FSM + JSON API + TS union + labels + active-set + UI conditions" complaint. This task closes the Go→TS half of that gap: it generates `STAGE_STATUSES`/`StageStatus` from Go, following the same `tools/<name>/main.go` + Makefile-target convention the repo already uses for `tools/setstatuslinter`. Hand-written UI concerns (`STAGE_STATUS_LABELS`, `ACTIVE_STAGE_STATUSES`) stay manual in `stage.ts` — a generator can't invent English copy or decide which statuses count as "active" — but because `STAGE_STATUS_LABELS` is typed `Record<StageStatus, string>`, the TypeScript compiler now force-fails the build if a new Go status is added without a label, instead of relying on someone remembering to update five different places.

**Files:**
- Modify: `pkg/state/state.go` (add `AllStatuses()`)
- Test: `pkg/state/state_test.go` (add `TestAllStatuses_MatchesConsts`)
- Create: `tools/genstagestatus/main.go`
- Modify: `Makefile` (add `generate` target, wire into `lint-ci`)
- Create: `pkg/web/dashboard/src/types/stage-status.generated.ts` (generated file, committed like any other generated artifact in this repo — there's no separate "generated/" gitignore convention here, confirm by checking `.gitignore` before assuming; if none exists, commit it)
- Modify: `pkg/web/dashboard/src/types/stage.ts` (import `STAGE_STATUSES`/`StageStatus` from the generated file instead of declaring them)

**Interfaces:**
- Consumes: `state.StageStatus` consts (`StatusPending` ... `StatusHookFailed`, `pkg/state/state.go:19-34`).
- Produces: `state.AllStatuses() []StageStatus` (Go, in `pkg/state/state.go`), the generated `pkg/web/dashboard/src/types/stage-status.generated.ts` exporting `STAGE_STATUSES`/`StageStatus`, consumed by `stage.ts`.

- [ ] **Step 1: Write the failing test for `AllStatuses`**

Add to `pkg/state/state_test.go` (create the assertion near existing status-related tests, or as a new function anywhere in the file):

```go
func TestAllStatuses_MatchesConsts(t *testing.T) {
	want := []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusAwaitingUserInput,
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

- [ ] **Step 2: Run the test, confirm it fails to compile**

Run: `go test ./pkg/state/... -run TestAllStatuses_MatchesConsts -v`
Expected: compile error `undefined: AllStatuses`.

- [ ] **Step 3: Implement `AllStatuses`**

In `pkg/state/state.go`, add right after the `const (...)` block (after line 34, before `type StageState struct`):

```go
// AllStatuses returns every StageStatus in declaration order. This is the
// single source of truth tools/genstagestatus reads to generate the
// frontend's StageStatus TypeScript union — add a new status here (and to
// the const block above) and both stay in sync automatically instead of
// requiring a matching hand-edit in pkg/web/dashboard/src/types/stage.ts.
func AllStatuses() []StageStatus {
	return []StageStatus{
		StatusPending, StatusPlanning, StatusAwaitingApproval, StatusRevising,
		StatusReady, StatusRunning, StatusRetrying, StatusAwaitingUserInput,
		StatusDone, StatusFailed, StatusHookFailed,
	}
}
```

- [ ] **Step 4: Run the test, confirm it passes**

Run: `go test ./pkg/state/... -run TestAllStatuses_MatchesConsts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "feat(state): добавляем AllStatuses() как единый источник для генератора TS-статусов"
```

- [ ] **Step 6: Write the generator**

Create `tools/genstagestatus/main.go`:

```go
// Command genstagestatus generates
// pkg/web/dashboard/src/types/stage-status.generated.ts from
// state.AllStatuses(), the single Go source of truth for stage status names.
// Run via `make generate`; `make lint-ci` fails the build if the committed
// file is stale relative to pkg/state/state.go.
package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/akopichin/afm/pkg/state"
)

const outPath = "pkg/web/dashboard/src/types/stage-status.generated.ts"

const header = `// Code generated by tools/genstagestatus from pkg/state.AllStatuses(). DO NOT EDIT.
// Run 'make generate' after adding/removing a state.StageStatus const.
`

func main() {
	var buf bytes.Buffer
	buf.WriteString(header)
	buf.WriteString("\nexport const STAGE_STATUSES = [\n")
	for _, s := range state.AllStatuses() {
		fmt.Fprintf(&buf, "  %q,\n", string(s))
	}
	buf.WriteString("] as const\n\n")
	buf.WriteString("export type StageStatus = (typeof STAGE_STATUSES)[number]\n")

	if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "genstagestatus: write %s: %v\n", outPath, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 7: Run the generator once and inspect the output**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go run ./tools/genstagestatus`
Expected: creates/overwrites `pkg/web/dashboard/src/types/stage-status.generated.ts` with content matching the header format above, one quoted status per line, exactly the 11 current statuses in `AllStatuses()`'s order.

- [ ] **Step 8: Update `stage.ts` to import the generated file**

Replace lines 1-17 of `pkg/web/dashboard/src/types/stage.ts`:

```ts
// Полный набор статусов стадии afm (см. statusLabels в текущем app.js).
// done — завершена (не completed).
export const STAGE_STATUSES = [
  'pending',
  'planning',
  'awaiting_approval',
  'revising',
  'ready',
  'running',
  'done',
  'failed',
  'retrying',
  'awaiting_user_input',
  'hook_failed',
] as const

export type StageStatus = (typeof STAGE_STATUSES)[number]
```

with:

```ts
// STAGE_STATUSES/StageStatus сгенерированы из pkg/state.AllStatuses() —
// см. tools/genstagestatus и 'make generate'. Раньше это был отдельный
// вручную поддерживаемый список, который приходилось синхронизировать с Go
// FSM руками при каждом новом статусе.
export { STAGE_STATUSES, type StageStatus } from './stage-status.generated'
```

Note: the generated file's order (`AllStatuses()`'s declaration order: `pending, planning, awaiting_approval, revising, ready, running, retrying, awaiting_user_input, done, failed, hook_failed`) differs slightly from the old hand-written array's order (`done, failed, retrying, awaiting_user_input, hook_failed` — `done`/`failed` came before `retrying` in the old list). This has zero runtime effect (`STAGE_STATUSES` is only ever used as a lookup set via `.includes()` in `isStageStatus`, and to build `STAGE_STATUS_LABELS`/`ACTIVE_STAGE_STATUSES` which are keyed objects/Sets, order-independent) — no code change needed elsewhere, just confirm by running the frontend test suite in Step 10.

- [ ] **Step 9: Add the Makefile `generate` target and wire it into `lint-ci`**

In `Makefile`, add near the `SETSTATUSLINTER_BIN` block (after the `test:` target, before `SETSTATUSLINTER_BIN`):

```makefile
.PHONY: generate
generate:
	$(GOENV) go run ./tools/genstagestatus

# generate-check — фейлит билд, если сгенерированный TS-файл устарел
# относительно pkg/state.AllStatuses() (забыли make generate после правки
# статусов). Используется lint-ci, не lint (тот тихо чинит --fix'ом, здесь
# нужно явное падение).
.PHONY: generate-check
generate-check: generate
	git diff --exit-code -- pkg/web/dashboard/src/types/stage-status.generated.ts
```

Add `generate-check` to the `lint-ci` target's prerequisites/body:

```makefile
.PHONY: lint-ci
lint-ci: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN) generate-check
	$(GOENV) $(GOLANGCI_BIN) run ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...
```

- [ ] **Step 10: Run the frontend suite to confirm the reordered union changes nothing observable**

Run: `cd pkg/web/dashboard && npm run build && npm test`
Expected: build succeeds (no TS error from the re-exported type), all tests PASS.

- [ ] **Step 11: Run `make generate-check` to confirm it's a no-op right after generating**

Run: `make generate-check`
Expected: exits 0 (no diff — the file was just generated in Step 7 and hasn't been hand-edited since).

- [ ] **Step 12: Full verification**

Run: `go build ./... && go test ./... -race && make lint && cd pkg/web/dashboard && npm run build && npm test`
Expected: everything green.

- [ ] **Step 13: Commit**

```bash
git add tools/genstagestatus Makefile pkg/web/dashboard/src/types/stage.ts pkg/web/dashboard/src/types/stage-status.generated.ts
git commit -m "feat: генерируем STAGE_STATUSES/StageStatus во фронтенде из pkg/state.AllStatuses()"
```

---

## Post-plan verification checklist

- [ ] `go build ./...` clean
- [ ] `go test ./... -race` clean
- [ ] `make lint-ci` clean (includes the new `generate-check`)
- [ ] `cd pkg/web/dashboard && npm run build && npm test` clean
- [ ] Manually smoke-test the dashboard once (per this repo's own `verify`/`run` skill conventions): start a flow, confirm the stage list, plan panel, and dialog panel still render and their action buttons (Approve/Revise/Retry/Retry-hook/Skip-hook/dialog answer/cancel) still work end-to-end — this plan changed the wire format and the component prop contracts, and unit tests alone don't prove the real dashboard renders correctly against a live `afm run`.
