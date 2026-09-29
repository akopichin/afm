# Pause on focus — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Pause on focus" composer toggle that freezes a running stage while
you write a note and resumes it (delivering the note) on send — built on a
reliability-hardened, single shared resume primitive.

**Architecture:** Bottom-up. First harden the concurrency core (an epoch guard in the
spawn path + ref-counted drain), then a per-stage resume claim making `EvPause`+gen
atomic, then one shared `resumePaused` core with per-caller strategies (plain
Continue, send-from-paused feedback, review-owner) that Continue/Revise-from-paused/
`resumeOwner` all route through. Then the frontend hook + composer.

**Tech Stack:** Go (orchestrator, `pkg/orchestrator`, `pkg/orchestrator/concurrency`,
`pkg/server`), React/TypeScript + Vitest (`pkg/web/dashboard`), CSS skins.

**Spec:** `docs/superpowers/specs/2026-09-29-pause-on-focus-design.md` (read it first;
this plan argues from it).

## Global Constraints
- Do NOT change the Go version in `go.mod`.
- `make lint-ci` must pass (two golangci passes + setstatuslinter + generate-check);
  gofmt/goimports struct-field alignment matters.
- Frontend: edit only `skins/base/*`; the build mirrors to `public/skins`. Rebuild the
  embedded bundle (`npm run build`) + stage `index.html`/`assets/` before the final
  commit.
- Commits in Russian; no `Co-Authored-By`.
- CHANGELOG entries in English.
- The concurrency `Manager` stays orchestrator-agnostic: it receives generic seams
  (`shouldRun`, new `epochOf`), never imports orchestrator types.
- Deterministic fake-runner tests are the acceptance gate; the live run is
  confirmation, not the gate. The user requires a COMPLEX live run (multi-stage, real
  agent, exercising overlap), not a trivial smoke.

## Review Focus
- **Queued-behind-semaphore old runner** (paused while still waiting on the command
  semaphore): must abdicate via the epoch guard — Task 1.
- **Admitted (past-lease) old subprocess** overlapping the new runner: must be fully
  drained before transition; A's `markDone` must not clear B — Task 2.
- **Continue interleaving between `EvPause` and `bumpPauseGen`**: must not strand the
  stage (running/revising with no runner) — Task 3.
- **Concurrent Continue vs send-from-paused**, and **review-pause beginning
  mid-resume**: exactly one runner, resume aborts if review-pause won — Tasks 4/7.
- **Durable feedback persist failure**: stage stays `paused` (retryable), never a
  runnerless `revising` — Task 6.

---

## File structure

Backend:
- `pkg/orchestrator/concurrency/concurrency.go` (modify) — `epochOf` seam, epoch
  capture/recheck, ref-counted active tracking + `WaitDrained`.
- `pkg/orchestrator/orchestrator.go` (modify) — wire `epochOf: loadPauseGen`; per-stage
  claim map; construction.
- `pkg/orchestrator/resume.go` (create) — shared `resumePaused` core + `resumeStrategy`
  interface + the three strategies. Consolidation lives here.
- `pkg/orchestrator/control_api.go` (modify) — `Pause` returns applied + atomic under
  claim; `Continue` and `Revise` delegate to `resumePaused`.
- `pkg/orchestrator/retry.go` (modify) — thread spawn epoch; staleness check before
  every outcome side effect.
- `pkg/orchestrator/reviewpause.go` (modify) — `resumeOwner` tail calls shared core.
- `pkg/server/handlers.go` (modify) — `handleRevise` accepts paused; pause handler
  returns `{applied}`.

Frontend:
- `src/api/run-client.ts` (modify) — `pauseStage` returns `{applied}`.
- `src/components/pasteable-textarea/PasteableTextarea.tsx` (modify) — `onFocus`/`onBlur`.
- `src/hooks/use-pause-on-focus/` (create) — the hook + tests.
- `src/hooks/use-workspace-view/*` (modify) — targeted deferred-attention.
- `src/app/App.tsx` (modify) — instantiate hook before `useWorkspaceView`; `noteTarget`.
- `src/components/feed-workspace/FeedComposer.tsx` (modify) — toggle + banner, props-driven.
- `skins/base/feed-workspace.css` (modify) — toggle/banner/paused styles.
- `CHANGELOG.md` (modify).

---

## Task 1: Epoch guard in the concurrency Manager

**Files:**
- Modify: `pkg/orchestrator/concurrency/concurrency.go` (`SpawnAgentLease`, `Manager`, `New`)
- Modify: `pkg/orchestrator/orchestrator.go` (pass `epochOf`)
- Test: `pkg/orchestrator/concurrency/concurrency_test.go`

**Interfaces:**
- Consumes: existing `Manager`, `pauseGen`/`loadPauseGen` (orchestrator.go).
- Produces: `Manager.epochOf func(stageID string) uint64` field (nil ⇒ epoch always 0,
  guard inert); run callback signature gains `epoch uint64`:
  `run func(ctx context.Context, s flow.Stage, lease *Lease, epoch uint64)`.
  `SpawnAgent`'s wrapper ignores it (unchanged behavior for non-epoch callers).

- [ ] **Step 1: Write the failing test** — a queued runner whose epoch was bumped abdicates.

```go
// concurrency_test.go
func TestSpawnAgentLease_SupersededEpochAbdicates(t *testing.T) {
    var epoch atomic.Uint64
    m := NewWithSemaphores(map[string]Semaphore{"": newBlockingSem(1)}, "")
    m.epochOf = func(string) uint64 { return epoch.Load() }
    // occupy the single slot so the next spawn queues
    release := m.occupy(t, "") // helper: acquires the sole slot, returns releaser
    ran := make(chan struct{}, 1)
    m.SpawnAgentLease(context.Background(), flow.Stage{ID: "s1"},
        func(context.Context, flow.Stage, *Lease, uint64) { ran <- struct{}{} })
    epoch.Add(1)          // "pause" happens while the runner is queued
    release()             // free the slot; queued runner now acquires the lease
    select {
    case <-ran:
        t.Fatal("superseded queued runner must not execute")
    case <-time.After(200 * time.Millisecond):
    }
    m.WaitAgents(context.Background())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/concurrency/ -run SupersededEpoch -v`
Expected: FAIL (runner executes — no guard yet; also compile error on the 4-arg callback until Step 3).

- [ ] **Step 3: Implement the epoch capture + recheck**

In `SpawnAgentLease`: capture the epoch synchronously BEFORE `go`, recheck after lease.

```go
func (m *Manager) SpawnAgentLease(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage, *Lease, uint64)) {
    epoch := m.epoch(s.ID) // synchronous capture, before the goroutine
    m.agentWG.Add(1)
    go func() {
        defer m.agentWG.Done()
        lease, err := m.AcquireLease(ctx, s.Command)
        if err != nil { return }
        m.markActive(s.ID)
        defer func() { m.markDone(s.ID); lease.Release() }()
        if m.epoch(s.ID) != epoch || (m.shouldRun != nil && !m.shouldRun(s.ID)) {
            return
        }
        run(ctx, s, lease, epoch)
    }()
}

func (m *Manager) epoch(stageID string) uint64 {
    if m.epochOf == nil { return 0 }
    return m.epochOf(stageID)
}
```

Add `epochOf func(string) uint64` to `Manager` (settable, like `shouldRun`). Update
`SpawnAgent` wrapper to the 4-arg callback (ignore `epoch`). In
`orchestrator.New`, set `mgr.epochOf = o.loadPauseGen`.

- [ ] **Step 4: Run test to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/concurrency/ -run SupersededEpoch -v` → PASS.
Also add & pass `TestSpawnAgentLease_SameEpochRuns` (no bump ⇒ runs).

- [ ] **Step 5: Update all `run` call sites to the new signature and build**

Run: `~/homebrew/bin/go build ./... && ~/homebrew/bin/go test ./pkg/orchestrator/... ./pkg/orchestrator/concurrency/`
Expected: PASS (existing behavior preserved; callers ignore `epoch` for now — retry.go uses it in Task 5).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/concurrency/concurrency.go pkg/orchestrator/concurrency/concurrency_test.go pkg/orchestrator/orchestrator.go
git commit -m "concurrency: epoch-guard в SpawnAgentLease против queued-раннеров"
```

---

## Task 2: Ref-counted active tracking + `WaitDrained`

**Files:**
- Modify: `pkg/orchestrator/concurrency/concurrency.go` (`markActive`/`markDone`/`IsActive`, add `WaitDrained`)
- Test: `pkg/orchestrator/concurrency/concurrency_test.go`

**Interfaces:**
- Produces: `Manager.WaitDrained(ctx context.Context, stageID string) error` — blocks
  until zero admitted (past-lease) callbacks for `stageID`, or ctx cancels
  (`ctx.Err()`); `IsActive` becomes count>0. Active tracking is a per-stage counter so
  overlapping A/B don't clobber each other's marker.

- [ ] **Step 1: Write the failing test** — overlap: A active, B admitted; A's markDone leaves B active; WaitDrained waits for both.

```go
func TestActiveTracking_RefCountedOverlap(t *testing.T) {
    m := NewWithSemaphores(map[string]Semaphore{"": noopSemaphore{}}, "")
    m.markActive("s1"); m.markActive("s1") // A and B
    if !m.IsActive("s1") { t.Fatal("active") }
    m.markDone("s1")                        // A done
    if !m.IsActive("s1") { t.Fatal("B still active — must not be cleared by A") }
    done := make(chan struct{})
    go func() { _ = m.WaitDrained(context.Background(), "s1"); close(done) }()
    select { case <-done: t.Fatal("drained too early"); case <-time.After(50*time.Millisecond): }
    m.markDone("s1")                        // B done
    select { case <-done: case <-time.After(time.Second): t.Fatal("WaitDrained never returned") }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/concurrency/ -run RefCountedOverlap -v`
Expected: FAIL (boolean Store/Delete: A's markDone clears the marker; WaitDrained missing).

- [ ] **Step 3: Implement ref-counted tracking + WaitDrained**

```go
// activeAgents: sync.Map[stageID]*atomic.Int64
func (m *Manager) markActive(id string) { m.counter(id).Add(1) }
func (m *Manager) markDone(id string)   { m.counter(id).Add(-1) }
func (m *Manager) IsActive(id string) bool { return m.counter(id).Load() > 0 }
func (m *Manager) WaitDrained(ctx context.Context, id string) error {
    for m.IsActive(id) {
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-time.After(20 * time.Millisecond):
        }
    }
    return nil
}
// counter(id) LoadOrStore's a *atomic.Int64
```

- [ ] **Step 4: Run to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/concurrency/ -run 'RefCountedOverlap|SupersededEpoch|SameEpoch' -v` → PASS.

- [ ] **Step 5: Full package build/test (no regressions)**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/concurrency/concurrency.go pkg/orchestrator/concurrency/concurrency_test.go
git commit -m "concurrency: ref-counted active tracking + WaitDrained для надёжного drain"
```

---

## Task 3: Per-stage resume claim + atomic `Pause`+gen + `Pause` returns applied

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (per-stage claim map; replace `continueMu`)
- Modify: `pkg/orchestrator/control_api.go` (`Pause` signature + atomic EvPause+bumpPauseGen under claim)
- Test: `pkg/orchestrator/pause_atomic_test.go`

**Interfaces:**
- Produces: `(o *Orchestrator) resumeClaim(stageID string) *sync.Mutex` (per-stage,
  from a `sync.Map`); `(o *Orchestrator) Pause(ctx, stageID) (applied bool, err error)`.
  `EvPause` and `bumpPauseGen` happen while holding `resumeClaim(stageID)`.

- [ ] **Step 1: Write the failing test** — Pause returns applied; a Continue cannot interleave between EvPause and bumpPauseGen.

```go
func TestPause_AtomicWithGen_NoStrand(t *testing.T) {
    o := newTestOrchestrator(t, /* one running stage s1 with a fake runner */)
    applied, err := o.Pause(context.Background(), "s1")
    if err != nil || !applied { t.Fatalf("applied=%v err=%v", applied, err) }
    // second Pause on an already-paused stage: CAS miss → applied=false
    applied2, _ := o.Pause(context.Background(), "s1")
    if applied2 { t.Fatal("second pause must report applied=false") }
    // gen bumped exactly once for the successful pause
    if o.loadPauseGen("s1") == 0 { t.Fatal("pauseGen must be bumped under the claim") }
}
```
(The interleave-strand property is proven end-to-end in Task 6's concurrent test; here
assert the atomic bump + applied semantics.)

- [ ] **Step 2: Run to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run AtomicWithGen -v`
Expected: FAIL (compile: `Pause` currently returns only `error`).

- [ ] **Step 3: Implement claim + atomic Pause**

Add `claims sync.Map` to `Orchestrator`; `resumeClaim` LoadOrStore's a `*sync.Mutex`.
Rewrite `Pause`:

```go
func (o *Orchestrator) Pause(_ context.Context, stageID string) (bool, error) {
    if err := o.rejectIfReviewPaused(); err != nil { return false, err }
    mu := o.resumeClaim(stageID); mu.Lock(); defer mu.Unlock()
    switch o.currentStatus(stageID) { case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
    default: return false, nil }
    if s := o.graph.Stage(stageID); s != nil && s.IsScript() && o.currentStatus(stageID) == state.StatusRunning {
        return false, nil
    }
    if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
        return false, nil
    }
    o.bumpPauseGen(stageID) // atomic with EvPause under the claim
    if ch, ok := o.interruptChans.Load(stageID); ok { select { case ch.(chan struct{}) <- struct{}{}: default: } }
    return true, nil
}
```
Replace `continueMu` usages with `resumeClaim(stageID)` (Continue in Task 4). Update all
`Pause(` callers (server handler in Task 8, tests) to the 2-value signature.

- [ ] **Step 4: Run to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run AtomicWithGen -v` → PASS.

- [ ] **Step 5: Build (callers updated)**

Run: `~/homebrew/bin/go build ./... && ~/homebrew/bin/go test ./pkg/orchestrator/...`
Expected: PASS (server handler still compiles — adjust its `Pause` call to ignore/log applied until Task 8).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/control_api.go pkg/orchestrator/pause_atomic_test.go
git commit -m "orchestrator: per-stage resume-claim; атомарные EvPause+bumpPauseGen; Pause возвращает applied"
```

---

## Task 4: Shared `resumePaused` core + plain-Continue strategy

**Files:**
- Create: `pkg/orchestrator/resume.go`
- Modify: `pkg/orchestrator/control_api.go` (`Continue` delegates)
- Test: `pkg/orchestrator/resume_test.go`

**Interfaces:**
- Produces:
  ```go
  type resumeStrategy interface {
      // prepare runs durably BEFORE the FSM transition; error aborts (stage stays paused).
      prepare(stageID, stageDir string) error
      event() bus.FSMEvent                 // EvContinue (plain) | EvRevise (feedback)
      guard(pausedFrom state.StageStatus) bus.GuardCtx
      // spawn is called after the transition with the resolved kind; plain delegates
      // to resumeStageAtStatus, feedback/review spawn the *WithFeedback runner.
      spawn(ctx context.Context, s flow.Stage, pausedFrom state.StageStatus)
      // acceptsPausedFrom reports whether this strategy runs for a given PausedFrom
      // (feedback rejects "" / pending).
      acceptsPausedFrom(pausedFrom state.StageStatus) bool
  }
  func (o *Orchestrator) resumePaused(reqCtx context.Context, stageID string, st resumeStrategy) (applied bool, seq uint64, err error)
  ```
  `resumePaused` owns: `flowPauseMu → resumeClaim` admission + `rejectIfReviewPaused`;
  status recheck; `WaitDrained`; `st.prepare`; unique-token `continuedThisProcess`
  marker; FSM CAS (`st.event`); `spawnAgentLeased(runContext, stage, ...)` via
  `st.spawn`. `Continue` becomes `resumePaused(ctx, id, plainContinueStrategy{o})`.

- [ ] **Step 1: Write the failing test** — Continue via the shared core still resumes; concurrent double-Continue spawns once.

```go
func TestResumePaused_PlainContinueSingleSpawn(t *testing.T) {
    o := newTestOrchestrator(t /* s1 running, fake runner */)
    _, _ = o.Pause(context.Background(), "s1")
    var spawns atomic.Int64
    o.testSpawnHook = func(string) { spawns.Add(1) } // counts spawnAgentLeased dispatches
    var wg sync.WaitGroup
    for i := 0; i < 2; i++ { wg.Add(1); go func(){ defer wg.Done(); _ = o.Continue(context.Background(), "s1") }() }
    wg.Wait()
    if spawns.Load() != 1 { t.Fatalf("want exactly one spawn, got %d", spawns.Load()) }
    if o.currentStatus("s1") == state.StatusPaused { t.Fatal("stage should have resumed") }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run PlainContinueSingleSpawn -v`
Expected: FAIL (no `resumePaused`/`testSpawnHook` yet).

- [ ] **Step 3: Implement `resumePaused` + `plainContinueStrategy`**

`resume.go`:
```go
func (o *Orchestrator) resumePaused(reqCtx context.Context, stageID string, st resumeStrategy) (bool, uint64, error) {
    o.flowPauseMu.Lock()
    if err := o.rejectIfReviewPaused(); err != nil { o.flowPauseMu.Unlock(); return false, 0, err }
    mu := o.resumeClaim(stageID); mu.Lock()
    o.flowPauseMu.Unlock() // hold stageClaim through drain→transition→spawn; order flowPauseMu→claim preserved
    defer mu.Unlock()

    if o.currentStatus(stageID) != state.StatusPaused { return false, 0, nil }
    pausedFrom := o.opts.Store.PausedFrom(stageID)
    if !st.acceptsPausedFrom(pausedFrom) { return false, 0, nil }
    stage := o.graph.Stage(stageID); if stage == nil { return false, 0, nil }

    // drain admitted old runners (bounded, ctx-escapable) BEFORE the transition
    drainCtx, cancel := context.WithTimeout(o.runContext(reqCtx), drainTimeout); defer cancel()
    if err := o.concurrency.WaitDrained(drainCtx, stageID); err != nil { return false, 0, nil } // abort: stays paused

    stageDir := filepath.Join(o.opts.RunDir, stageID)
    if err := st.prepare(stageID, stageDir); err != nil { return false, 0, err } // durable; failure leaves paused

    token := newResumeToken()
    o.continuedThisProcess.Store(stageID, token)
    _, seq, ok := o.triggerWithSeq(stageID, st.event(), st.guard(pausedFrom), "")
    if !ok { o.continuedThisProcess.CompareAndDelete(stageID, token); return false, 0, nil }
    st.spawn(o.runContext(reqCtx), *stage, pausedFrom)
    return true, seq, nil
}
```
`plainContinueStrategy`: `prepare` no-op; `event` EvContinue; `guard` sets PausedFrom;
`spawn` calls the existing `resumeStageAtStatus(ctx, s, pausedFrom)`;
`acceptsPausedFrom` true for all non-empty (Continue handles pending too — first
activation). Refactor `Continue` to call `resumePaused` (preserve the
`continuedThisProcess`/startup-recovery contract via the token). Add `testSpawnHook`
seam (nil in prod).

- [ ] **Step 4: Run to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run 'PlainContinueSingleSpawn' -v` → PASS.

- [ ] **Step 5: Regression — existing Continue/recovery tests**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/...` → PASS (Continue behavior preserved).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/resume.go pkg/orchestrator/control_api.go pkg/orchestrator/resume_test.go
git commit -m "orchestrator: единый resumePaused core + plain-Continue strategy"
```

---

## Task 5: Epoch-aware side effects in `runWithRetry`

**Files:**
- Modify: `pkg/orchestrator/retry.go` (receive spawn epoch; staleness check before side effects)
- Test: `pkg/orchestrator/retry_epoch_test.go`

**Interfaces:**
- Consumes: `epoch uint64` from the run callback (Task 1).
- Produces: a helper `(o *Orchestrator) superseded(stageID string, epoch uint64) bool`
  (`o.loadPauseGen(stageID) != epoch`), checked before every outcome-publishing side
  effect (completion, verify-fail, fail, retry-reschedule, interrupt respawn).

- [ ] **Step 1: Write the failing test** — a runner whose epoch is stale publishes no outcome.

```go
func TestRunWithRetry_SupersededPublishesNothing(t *testing.T) {
    o := newTestOrchestrator(t /* s1 */)
    // simulate: runner started at epoch 0, a pause bumped to 1 mid-run
    epoch := uint64(0)
    o.bumpPauseGen("s1") // now current=1, runner holds epoch 0
    published := o.captureTriggers(t) // records EvComplete/EvFail/EvScheduleRetry
    o.runWithRetryForTest("s1", epoch, fakeRunnerThatCompletes)
    if published.any() { t.Fatalf("superseded runner must publish nothing, got %v", published) }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run SupersededPublishesNothing -v`
Expected: FAIL (outcome published).

- [ ] **Step 3: Implement the guard at each side-effect site**

Thread `epoch` into `runWithRetry`. Before each of: the completion path
(`retry.go:281` region), the `EvFail`/`commitVerifyFailure` branch, the retry-reschedule
branch, and the `onUserInterrupted` respawn — add:
```go
if o.superseded(s.ID, epoch) { return } // a newer generation owns this stage
```
Keep the existing `status==paused` early-return; the epoch guard covers the
transitioned-but-superseded window.

- [ ] **Step 4: Run to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run SupersededPublishesNothing -v` → PASS.

- [ ] **Step 5: Regression**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/...` → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/retry.go pkg/orchestrator/retry_epoch_test.go
git commit -m "orchestrator: epoch-aware side effects в runWithRetry"
```

---

## Task 6: Feedback strategy + `Revise`-from-paused

**Files:**
- Modify: `pkg/orchestrator/resume.go` (add `feedbackStrategy`)
- Modify: `pkg/orchestrator/control_api.go` (`Revise` paused branch)
- Modify: `pkg/state/state.go` (durable-atomic feedback append helper if none exists)
- Test: `pkg/orchestrator/resume_feedback_test.go`

**Interfaces:**
- Produces: `feedbackStrategy{o, feedback}` — `prepare` persists via a durable-atomic
  append (reuse `SaveFeedbackOnce`'s fsync/atomic path or add
  `state.AppendFeedbackDurable`); `event` EvRevise; `guard` `{}`; `spawn` resolves
  `resumeKind` (detectInterruptedPhase) → `withFeedbackRunner`; `acceptsPausedFrom`
  false for `""`/`pending`. `Revise` paused branch → `resumePaused(reqCtx, id, feedbackStrategy{...})`.

- [ ] **Step 1: Write the failing tests** — (a) overlap yields exactly one feedback runner that receives the note; (b) pending-paused → no-op; (c) prepare failure leaves paused.

```go
func TestRevisePaused_OverlapSingleFeedbackRunner(t *testing.T) {
    o := newTestOrchestrator(t /* s1 running */)
    hold := o.holdRunnerAfterInterrupt("s1") // fake runner blocks after receiving interrupt
    _, _ = o.Pause(context.Background(), "s1")
    var fbRunners atomic.Int64; var gotFeedback atomic.Value
    o.testFeedbackHook = func(fb string) { fbRunners.Add(1); gotFeedback.Store(fb) }
    applied, _, _ := o.Revise(context.Background(), "s1", "add retries")
    hold.release()
    if !applied || fbRunners.Load() != 1 { t.Fatalf("applied=%v runners=%d", applied, fbRunners.Load()) }
    if gotFeedback.Load() != "add retries" { t.Fatal("feedback not delivered") }
}
func TestRevisePaused_PendingIsNoop(t *testing.T) { /* pause a pending (auto_run:false) stage → Revise returns applied=false, no spawn */ }
func TestRevisePaused_PrepareFailureStaysPaused(t *testing.T) { /* inject SaveFeedback err → status stays paused, no revising */ }
```

- [ ] **Step 2: Run to verify they fail**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run RevisePaused -v`
Expected: FAIL (Revise still rejects paused).

- [ ] **Step 3: Implement durable append + feedbackStrategy + Revise branch**

Add `state.AppendFeedbackDurable(stageDir, feedback)` (temp+rename+fsync, or reuse
`SaveFeedbackOnce`'s mechanism without the op sentinel). Implement `feedbackStrategy`.
In `Revise`, before the existing running/awaiting_approval logic:
```go
if o.currentStatus(stageID) == state.StatusPaused {
    return o.resumePaused(reqCtx, stageID, feedbackStrategy{o: o, feedback: feedback})
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run RevisePaused -v` → PASS.

- [ ] **Step 5: Concurrent Continue-vs-Revise + regression**

Add `TestResumePaused_ContinueVsReviseSingleSpawn` (fire both concurrently on a paused
stage → exactly one runner, status not paused, no strand). Run:
`~/homebrew/bin/go test ./pkg/orchestrator/... -race` → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/resume.go pkg/orchestrator/control_api.go pkg/state/state.go pkg/orchestrator/resume_feedback_test.go
git commit -m "orchestrator: send-from-paused через feedbackStrategy + durable feedback persist"
```

---

## Task 7: Consolidate `resumeOwner` onto the shared core

**Files:**
- Modify: `pkg/orchestrator/reviewpause.go` (`resumeOwner` tail → shared core; review strategy)
- Modify: `pkg/orchestrator/reviewpause.go` (`PauseFlow` ownership uses eligible FSM status)
- Test: `pkg/orchestrator/reviewpause_test.go` (existing) + `reviewpause_resume_test.go`

**Interfaces:**
- Produces: `reviewOwnerStrategy{o, ow, isTarget}` — `prepare` = `SaveFeedbackOnce`
  (target) / no new write (non-target FromRevising) / none; `armResumeContext` for
  non-target; `event` EvRevise (useFeedback) | EvContinue; `spawn` by `ow.ResumeKind`;
  durable-replay authorization for already-transitioned statuses stays in this
  strategy. `runResumeTransaction` remains the outer transaction; its per-owner tail
  calls `resumePaused(ctx, ow.ID, reviewOwnerStrategy{...})`.

- [ ] **Step 1: Write the failing test** — review-pause begins mid-resume: a send-from-paused aborts (no resume inside the review transaction).

```go
func TestResumePaused_AbortsWhenReviewPauseWins(t *testing.T) {
    o := newTestOrchestrator(t /* s1 paused-from-running */)
    o.beginReviewPauseDuringDrain("s1") // hook: establishes review transaction while WaitDrained loops
    applied, _, err := o.Revise(context.Background(), "s1", "note")
    if applied || err == nil { t.Fatalf("resume must be rejected during review pause: applied=%v err=%v", applied, err) }
    if o.currentStatus("s1") != state.StatusPaused { t.Fatal("stage must remain paused") }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run AbortsWhenReviewPauseWins -v`
Expected: FAIL (no admission ordering yet catches mid-drain review pause).

- [ ] **Step 3: Implement review strategy + PauseFlow ordering**

Refactor `resumeOwner` (reviewpause.go:376) so its post-drain tail delegates to
`resumePaused` with `reviewOwnerStrategy`, deleting the duplicated
transition/dispatch. Make `PauseFlow` take `resumeClaim(id)` before pausing each stage
and base ownership on eligible FSM status (not only `IsActive`) so a
transition-committed-but-queued resume isn't skipped. Ensure `resumePaused`'s
`flowPauseMu`+`rejectIfReviewPaused` admission (Task 4) rejects a resume once a review
transaction is established.

- [ ] **Step 4: Run to verify it passes**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/ -run 'AbortsWhenReviewPauseWins|ReviewPause' -v` → PASS.

- [ ] **Step 5: Full review-pause regression (race)**

Run: `~/homebrew/bin/go test ./pkg/orchestrator/... -race` → PASS (all existing
review-pause tests green; no duplicated resume logic remains — grep `resume.go` is the
only transition/drain/spawn site).

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/reviewpause.go pkg/orchestrator/reviewpause_resume_test.go
git commit -m "orchestrator: resumeOwner на общий resumePaused core (без копипасты)"
```

---

## Task 8: Server — accept paused in `handleRevise`; pause returns `{applied}`

**Files:**
- Modify: `pkg/server/handlers.go` (`handleRevise` gate; pause handler response)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Consumes: `StageActions.Pause(ctx, id) (bool, error)` (Task 3).
- Produces: `POST /api/stages/{id}/pause` → `200 {"applied":bool}`; `handleRevise`
  accepts status `paused`; revise CAS-miss (`applied=false`) → `409` (unchanged;
  preserves draft).

- [ ] **Step 1: Write the failing tests**

```go
func TestHandleRevise_AcceptsPaused(t *testing.T) { /* stage paused → POST revise → 200; orchestrator.Revise called */ }
func TestHandlePause_ReturnsApplied(t *testing.T) { /* running stage → POST pause → 200 {"applied":true}; second → {"applied":false} */ }
```

- [ ] **Step 2: Run to verify they fail**

Run: `~/homebrew/bin/go test ./pkg/server/ -run 'AcceptsPaused|ReturnsApplied' -v`
Expected: FAIL (handler rejects paused; pause returns no body).

- [ ] **Step 3: Implement**

In `handleRevise`, add `state.StatusPaused` to the accepted pre-filter set. In the pause
handler, capture `applied` from `Pause` and `json.NewEncoder(w).Encode(struct{Applied bool `json:"applied"`}{applied})`.

- [ ] **Step 4: Run to verify they pass**

Run: `~/homebrew/bin/go test ./pkg/server/ -run 'AcceptsPaused|ReturnsApplied' -v` → PASS.

- [ ] **Step 5: Full server + lint**

Run: `~/homebrew/bin/go test ./pkg/server/... && make lint-ci` → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "server: handleRevise принимает paused; pause отдаёт {applied}"
```

---

## Task 9: Client — `pauseStage` returns `{applied}`

**Files:**
- Modify: `pkg/web/dashboard/src/api/run-client.ts`
- Test: `pkg/web/dashboard/src/api/run-client.test.ts` (create if absent, else extend)

**Interfaces:**
- Produces: `pauseStage(stageId): Promise<{ applied: boolean }>` (was `Promise<void>`).
  `continueStage`/`reviseStage` unchanged.

- [ ] **Step 1: Write the failing test**

```ts
test('pauseStage returns applied from the response body', async () => {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ applied: true }), { status: 200 }))
  await expect(pauseStage('s1')).resolves.toEqual({ applied: true })
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/api/run-client.test.ts`
Expected: FAIL (returns void).

- [ ] **Step 3: Implement** — change `pauseStage` to `jsonRequest<{applied:boolean}>('/api/stages/'+id+'/pause','POST',null)`.

- [ ] **Step 4: Run to verify it passes** → PASS.

- [ ] **Step 5: typecheck** — `npm run typecheck` (App still ignores the return until Task 11).

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/api/run-client.ts pkg/web/dashboard/src/api/run-client.test.ts
git commit -m "dashboard(api): pauseStage возвращает {applied}"
```

---

## Task 10: `PasteableTextarea` — `onFocus`/`onBlur`

**Files:**
- Modify: `pkg/web/dashboard/src/components/pasteable-textarea/PasteableTextarea.tsx`
- Test: `.../PasteableTextarea.test.tsx`

**Interfaces:**
- Produces: optional `onFocus?: () => void`, `onBlur?: () => void` props forwarded to the
  inner `<textarea>`. Existing callers unaffected.

- [ ] **Step 1: Write the failing test** — focusing/blurring the textarea calls the props.

```tsx
test('forwards onFocus/onBlur to the textarea', () => {
  const onFocus = vi.fn(), onBlur = vi.fn()
  render(<PasteableTextarea stageId="s1" value="" onChange={()=>{}} onFocus={onFocus} onBlur={onBlur} />)
  const ta = document.querySelector('textarea')!
  fireEvent.focus(ta); fireEvent.blur(ta)
  expect(onFocus).toHaveBeenCalledOnce(); expect(onBlur).toHaveBeenCalledOnce()
})
```

- [ ] **Step 2: Run to verify it fails** → FAIL.

- [ ] **Step 3: Implement** — add the two optional props to the type and spread onto the `<textarea>`.

- [ ] **Step 4: Run to verify it passes** → PASS.

- [ ] **Step 5: typecheck** → clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/pasteable-textarea/
git commit -m "dashboard(composer): PasteableTextarea пробрасывает onFocus/onBlur"
```

---

## Task 11: `usePauseOnFocus` hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-pause-on-focus/{use-pause-on-focus.ts,index.ts,use-pause-on-focus.test.ts}`

**Interfaces:**
- Produces:
  ```ts
  type PauseOnFocusApi = {
    enabled: boolean; setEnabled: (v: boolean) => void   // persisted afm.pauseOnFocus
    isOwnedPause: (stageId: string) => boolean            // banner + deferred-attention key
    onFocus: (stageId: string, status: StageStatus) => void
    onBlur: (stageId: string, hasDraft: boolean) => void
    beforeSend: (stageId: string) => Promise<void>        // awaits in-flight pause; caller then revises
    afterSend: (stageId: string) => void                  // clears ownership
    onResumeNow: (stageId: string) => void
    reconcile: (stages: Stage[]) => void                  // clear ownership if status left paused
  }
  function usePauseOnFocus(deps: { pauseStage; continueStage }): PauseOnFocusApi
  ```
  Op state machine per stageId: `idle→pausing→owned→(sending|resuming)→idle`; ownership
  only when `pauseStage` resolves `{applied:true}`; all mutating ops await any in-flight
  pause; stage change Continues a still-owned prior stage. Stage-ID-driven (no dependence
  on `workspaceStage`).

- [ ] **Step 1: Write the failing tests** (the core suite)

```ts
// use-pause-on-focus.test.ts — key cases:
test('toggle persists to localStorage')                                    // afm.pauseOnFocus
test('focus with enabled+running pauses and claims ownership only on applied')
test('focus with disabled does not pause')
test('beforeSend awaits an in-flight pause (no request reordering)')
test('afterSend clears ownership')
test('blur empty while owned continues; blur with draft stays paused')
test('onResumeNow continues and clears ownership; dedups with blur')
test('does NOT own a pause that returned applied:false (manual/cross-tab) → blur no continue')
test('stage change continues the previously owned stage before switching')
test('reconcile clears ownership when live status leaves paused')
```
Use `vi.fn()` resolved promises for `pauseStage`/`continueStage`; assert call order with
a shared log array.

- [ ] **Step 2: Run to verify they fail** → FAIL (hook absent).

- [ ] **Step 3: Implement the hook** — refs for the per-stage op state + owned set; a
  `pendingPause` promise map so `beforeSend`/`onBlur`/`onResumeNow` await it; localStorage
  read on mount / write on `setEnabled`.

- [ ] **Step 4: Run to verify they pass** → PASS.

- [ ] **Step 5: typecheck** → clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-pause-on-focus/
git commit -m "dashboard: usePauseOnFocus (ownership + promise-sequenced op state machine)"
```

---

## Task 12: `useWorkspaceView` deferred-attention + App wiring + `noteTarget`

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-workspace-view/workspace-view.ts` (+ hook)
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Test: `.../use-workspace-view/*.test.ts`, `.../app/App.test.tsx`

**Interfaces:**
- Consumes: `usePauseOnFocus` (Task 11).
- Produces: `useWorkspaceView` accepts a `deferAttentionFor?: (stageId, kind) => boolean`
  so an owned paused stage's attention is deferred (NOT permanently marked handled) and
  re-opens once ownership releases. `App` instantiates `usePauseOnFocus` BEFORE
  `useWorkspaceView`, keyed by `stages`. `noteTarget = !isScript &&
  flowPauseState==='none' && (status==='running' || (status==='paused' && ['running',
  'planning','revising','retrying'].includes(pausedFrom)))`.

- [ ] **Step 1: Write the failing tests**

```ts
// workspace-view test: an owned paused arrival is deferred, not consumed; on release it auto-opens.
test('deferred attention re-opens after ownership releases')
// App test: composer hidden during review-pause and for pending-paused/script; shown for paused-from-running.
test('noteTarget: shown for paused-from-running, hidden during flow review-pause')
test('noteTarget: hidden for paused-from-pending and script stages')
```

- [ ] **Step 2: Run to verify they fail** → FAIL.

- [ ] **Step 3: Implement** — thread the defer predicate into the reducer (skip marking
  handled while deferred; re-queue when the predicate flips false); move the
  `usePauseOnFocus` call above `useWorkspaceView` in App; update `noteTarget`; pass hook
  handlers + `isOwnedPause` down to `FeedWorkspace`/`FeedComposer`; wire
  `onSend` to `beforeSend`→`reviseStage`→`afterSend`.

- [ ] **Step 4: Run to verify they pass** → PASS.

- [ ] **Step 5: Full dashboard test + typecheck**

Run: `cd pkg/web/dashboard && npm run test && npm run typecheck` → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-workspace-view/ pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/src/app/App.test.tsx
git commit -m "dashboard: deferred-attention для owned-pause; App wiring + noteTarget gate"
```

---

## Task 13: `FeedComposer` toggle + banner + CSS

**Files:**
- Modify: `pkg/web/dashboard/src/components/feed-workspace/FeedComposer.tsx` (+ FeedWorkspace pass-through)
- Modify: `pkg/web/dashboard/skins/base/feed-workspace.css`
- Test: `.../FeedComposer.test.tsx`

**Interfaces:**
- Consumes: hook props (`enabled`,`setEnabled`,`isOwnedPause`,`onFocus`,`onBlur`,`onResumeNow`) + `status`.
- Produces: right-aligned toggle row, paused banner (`.pause-banner` with `Resume now`),
  paused send tint + amber focus ring — matching the approved mockup.

- [ ] **Step 1: Write the failing tests**

```tsx
test('renders the Pause-on-focus toggle (right-aligned opts row)')
test('shows the paused banner when status==="paused" and pause is owned')
test('Resume now calls onResumeNow')
test('focusing the textarea calls onFocus with the stage id + status')
```

- [ ] **Step 2: Run to verify they fail** → FAIL.

- [ ] **Step 3: Implement** the presentational composer (toggle + banner + wiring to
  `onFocus`/`onBlur` of `PasteableTextarea`) and the CSS (`.composer-opts` right-aligned,
  `.switch`/`.track`, `.pause-banner`, paused send tint, `.composer-row.focused` amber
  ring when paused) — port from the approved mockup.

- [ ] **Step 4: Run to verify they pass** → PASS.

- [ ] **Step 5: Full dashboard test + typecheck** → PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/feed-workspace/ pkg/web/dashboard/skins/base/feed-workspace.css
git commit -m "dashboard(composer): тумблер Pause on focus + баннер паузы + стили"
```

---

## Task 14: CHANGELOG, bundle rebuild, final verification

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `pkg/web/dashboard/index.html`, `pkg/web/dashboard/assets/*`, `public/skins/*` (generated)

- [ ] **Step 1: CHANGELOG English Improvement entry** under `## 2026-09-29`:

```markdown
### Improvement: pause a stage while you write a note (Pause on focus)

The feed composer gains a right-aligned **Pause on focus** toggle. When on, clicking
into the note field pauses the running stage so the agent can't finish before you're
done; sending the note resumes the stage with your note applied. Resume-now or leaving
the field empty un-pauses. Internally, resume-from-paused, Continue, and review-pause
resume now share one race-free primitive (per-stage claim, generation guard, reliable
drain), which also hardens the existing resume paths.
```

- [ ] **Step 2: Rebuild the embedded bundle**

Run: `cd pkg/web/dashboard && npm run build`
Verify: `grep -l "pause-banner\|pauseOnFocus" assets/*.js` is non-empty.

- [ ] **Step 3: Full gates**

Run:
```
cd pkg/web/dashboard && npm run test && npm run typecheck
cd - && ~/homebrew/bin/go build ./... && ~/homebrew/bin/go vet ./... && ~/homebrew/bin/go test ./... -race
make lint-ci
```
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md pkg/web/dashboard/index.html pkg/web/dashboard/assets pkg/web/dashboard/public
git commit -m "Pause on focus: CHANGELOG + пересборка бандла"
```

- [ ] **Step 5: Final codex review** (architectural + anti-copypaste lens per user
  preference) of the whole branch diff; fix findings; re-run gates.

- [ ] **Step 6: COMPLEX live run** (docker off, isolated dir; real `claude` agent).
  Multi-stage flow where a stage runs long enough to pause. Verify, with the dashboard
  driven via chrome-devtools + status polling:
  1. toggle on → focus note field → stage shows Paused (rail + banner), agent frozen;
  2. type a multi-line note → Send → stage resumes and the agent's next turn reflects
     the note (check the feed / feedback.md); status returns to running;
  3. Resume now (without sending) un-pauses; blur-empty un-pauses;
  4. manual kebab pause + send also resumes with the note;
  5. **overlap stress:** pause, then rapidly Send while the old agent is still draining
     → exactly one agent continues (check no duplicate agent_action bursts / no strand),
     and repeat with a second stage running in parallel;
  6. review-pause interaction: start a flow review-pause → composer is hidden/disabled;
  7. toggle off → send behaves as today (interrupt, no pause);
  8. spot-check graphite + goga themes.
  Capture screenshots of the paused banner and the resumed feed.
