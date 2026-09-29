# Design: "Pause on focus" composer + safe resume-from-paused

Status: draft for review · Date: 2026-09-29

## Intent

When an agent is actively running a stage, the user wants to steer it with a note
from the feed composer without the agent finishing (or moving on) before the note
is delivered. A **Pause on focus** toggle: when on, focusing the note field
freezes the running stage (agent held), a banner shows it's paused, and **Send**
delivers the note *and resumes the stage with it applied*. Toggle off ⇒ today's
behavior (send interrupts the live agent, no pause). The preference persists.

The UX is settled and approved (see the mockup that was signed off). This document
is about doing the **resume-from-paused** safely, because two codex reviews showed
the naïve version has reliability-core concurrency hazards — and that the machinery
needed also **hardens a latent bug in the existing `Continue`/resume paths**.

### Goals
- Focus-pauses a running stage; Send resumes it delivering the note; toggle-off is
  unchanged behavior.
- **Exactly one** agent runner after any pause→resume, in every interleaving
  (active-draining runner, queued-behind-semaphore runner, concurrent Continue,
  concurrent review-pause, startup recovery).
- Consolidate the three near-duplicate resume paths — `Continue`, review-pause's
  `resumeOwner`, and the new send-from-paused — onto **one shared primitive**. No
  third copy of drain/transition/spawn logic.
- No stranded stages (paused forever, or `revising` with no runner).

### Non-goals
- No change to the pause SIGINT+grace mechanism itself.
- No new FSM statuses (revising already exists; EvRevise already permits `paused`).
- No change to the approved visual design.

## Background: current mechanics (as-is)

- **Pause** (`control_api.go`): CAS `EvPause` (running/planning/revising/retrying →
  paused), `bumpPauseGen(id)`, signal `interruptChans`. 15s grace then SIGKILL.
  Returns `error` (no "did it apply" signal).
- **Revise** (`control_api.go`): permitted from `running`/`awaiting_approval` (the
  method gates out `paused`, though the FSM table already allows `EvRevise` from
  `paused`). Running → `EvRevise` (→revising) + `SaveFeedback` + signal interrupt;
  the live runner restarts with feedback (`runWithRetry` → `*WithFeedback`).
- **Continue** (`control_api.go`): under `continueMu`; `EvContinue` (paused →
  `PausedFrom`) then `resumeStageAtStatus`. Uses `continuedThisProcess` so startup
  recovery doesn't double-spawn. **Does not drain** the old runner before spawning.
- **resumeOwner** (`reviewpause.go`): the ONLY path that currently drains — waits
  `concurrency.IsActive(id)` (so the interrupted runner returns still seeing
  `paused` and no-ops per `retry.go`), then dispatches on current status and spawns
  one runner via `spawnAgentLeased`.
- **SpawnAgentLease** (`concurrency/concurrency.go`): `AcquireLease` (blocks on the
  command semaphore) → `markActive` → `shouldRun(id)` re-check (currently
  `status != paused`) → `run`. `markActive` happens **only after** lease
  acquisition, so `IsActive` cannot see a runner still queued on the semaphore.
- **pauseGen** (`orchestrator.go`): a per-stage monotonic `atomic.Uint64`, bumped by
  every Pause (`bumpPauseGen`), read by `loadPauseGen`. Today only the before-hook
  path consults it (`hooks.go`) to abdicate a superseded relaunch. **The spawn path
  does not consult it.**

## The core problem

Resuming a paused stage (Continue OR send-from-paused) must guarantee a single
runner. Two independent hazards:

1. **Active old runner** still draining when we transition+spawn → it returns,
   sees a non-paused status, and spawns its own feedback restart → two runners.
   `resumeOwner`'s drain solves this for the active case only.
2. **Queued old runner** (spawned, blocked on the semaphore, `markActive` not yet
   called): `IsActive==false`, so drain sees nothing; after we resume to
   running/revising, the queued runner acquires the lease and `shouldRun`
   (status-only) lets it run → two runners. **No existing mechanism catches this**
   — and the existing `Continue` is exposed to it too.

Plus serialization gaps: Continue vs send-from-paused racing (both read paused,
both transition+spawn), review-pause beginning mid-resume, and feedback-write
failing *after* the FSM transition (stranding `revising`).

## Design (Approach A: generation-first, shared primitive)

### 1. Epoch (pauseGen) guard in the spawn path — the queued-runner fix

`SpawnAgentLease` captures the stage's `pauseGen` **epoch synchronously, before
launching the goroutine** (capturing inside the goroutine before `AcquireLease`
would itself race a pause), threads it into the goroutine, and re-checks it **after
lease acquisition, before the callback**:

```
epoch := epochOf(s.ID)             // captured synchronously in SpawnAgentLease, before `go`
go func() {
  lease := AcquireLease(ctx, cmd)  // may block arbitrarily
  markActive(s.ID)                 // ref-counted/token — see §3
  if epochOf(s.ID) != epoch || !shouldRun(s.ID) { return }  // abdicate if superseded
  run(ctx, s, lease, epoch)        // epoch flows to the runner (retry.go)
}()
```

- Manager takes **one** generic seam `epochOf func(stageID) uint64` (reads
  `pauseGen`) and does the capture+compare itself — not two callbacks
  (`genOf`/`superseded` was needless duplication). Matches how `shouldRun` is
  injected; Manager stays orchestrator-agnostic.
- Any Pause (which bumps `pauseGen`) invalidates a queued pre-pause runner
  regardless of `IsActive`. Protects **every** resume path, including today's
  `Continue`.
- **The runner receives the epoch captured for THAT spawn** (not a re-read of
  `pauseGen` at `runWithRetry` entry, which is weaker). `retry.go` must check
  staleness against that captured epoch **before every outcome-publishing side
  effect** — not only the `ErrUserInterrupted` branch: normal completion
  (`retry.go:281`), non-retryable failure, retry-reschedule, and the interrupt
  respawn (`retry.go:419`) can all publish or spawn, and a superseded generation
  must publish/spawn none of them.

The epoch guard stops queued/stale callbacks from *starting* or *publishing*. It
does **not** stop an already-running subprocess from mutating files — that is why
drain (§3) is a correctness requirement, not an optimization.

### 2. One shared resume primitive — strategy-based (consolidation / DRY)

A single **shared core** owns the mechanism; callers supply a semantic **strategy**
for preparation and dispatch. A coarse `withFeedback bool` is NOT enough — the three
callers differ in real ways (plain Continue must keep `resumeStageAtStatus`'s
artifact-recovery + completion fast-paths; a non-target review owner paused from
`revising` needs a feedback runner *without writing new feedback*; a review target
uses idempotent `SaveFeedbackOnce`).

Shared core `resumePaused(ctx, stageID, strategy)` owns, under the **per-stage
resume claim**:
1. **Review admission** (see §4): strict `flowPauseMu → stageClaim` ordering.
2. Re-read status; the paused case drives the FSM out of paused. An
   already-transitioned active status is treated as "spawn only" **only with
   explicit durable authorization** (review transaction proof) — a normal second
   Continue/send must NEVER read an active status as permission to spawn again.
   Guard `PausedFrom ∉ {"", pending}` for feedback strategies.
3. **Reliable drain** (§3): wait for all admitted pre-pause callbacks to unwind;
   on timeout/cancel → abort, leave `paused`.
4. Strategy **prepare** (durable, before the transition): e.g. feedback strategy
   persists the note via a durable atomic append (see "Feedback persistence"); a
   write failure aborts with the stage still `paused` (retryable).
5. Startup-recovery marker with **unique-token compare-and-set** ownership (stored
   before the transition; rollback via `CompareAndDelete` with the caller's token;
   recovery checks it under the same claim / after status read) — a losing op never
   deletes the winner's marker, and the token lifecycle permits a later
   second pause/resume in the same process.
6. FSM CAS: strategy chooses `EvRevise` or `EvContinue`; lost CAS → release,
   `applied=false`.
7. Exactly-one dispatch: `spawnAgentLeased(runContext, stage, strategy.runner)`.

Strategies:
- **plain Continue** → delegates to existing `resumeStageAtStatus` (keeps its
  recovery/completion fast-paths); no feedback write; `EvContinue`.
- **new feedback (send-from-paused)** → durable feedback prepare + saved-kind
  `withFeedbackRunner`; `EvRevise`.
- **review owner** → existing `SaveFeedbackOnce`/`armResumeContext` prepare +
  saved-kind dispatch, with explicit durable replay authorization.

`Continue` and the new send-from-paused become thin callers. `runResumeTransaction`
stays the review-specific OUTER transaction, but its per-owner tail calls the shared
core — no parallel drain/transition/spawn implementation remains.

### 3. Reliable drain — a correctness requirement

The epoch guard (§1) stops queued/stale callbacks; it cannot stop an
already-running **subprocess** from mutating files alongside the newly resumed
runner. So the shared core MUST drain all *admitted* (past-lease) pre-pause
callbacks before transitioning.

Today's active tracking is inadequate for overlap: `markActive`/`markDone` is a
boolean `Store`/`Delete` (`concurrency.go:157`), and `retry.go:169` explicitly lets
replacement B start before generation A unwinds — so A's `markDone` can delete B's
active marker. Fix: make active tracking **ref-counted or per-spawn-token** so drain
observes every admitted callback. The shared core:
- waits until all admitted pre-pause callbacks have drained (bounded,
  ctx-escapable);
- on drain timeout/cancel → **abort the resume, leave the stage `paused`** (a
  retry/reload re-drives it) — never transition with a subprocess still live.

The claim is **per-stage**, so a drain on stage X never blocks resume of stage Y
(fixes the "global `continueMu` held during drain" hazard).

### 4. Atomic pause + serialize with review-pause

- **`EvPause` + `bumpPauseGen` must be atomic w.r.t. resume.** Today they are
  separate (`control_api.go:273`); a Continue interleaving between them enqueues a
  replacement at the pre-bump epoch, then the bump rejects BOTH old and replacement
  as stale → FSM says running/revising with no runner (stranded). Fix: `Pause`,
  `PauseFlow`, and every resume path take the **same per-stage claim**; `EvPause +
  bumpPauseGen` complete before the claim is released.
- **Review-pause ordering (corrected):** strict lock order **`flowPauseMu →
  stageClaim`**. A normal resume acquires both, checks review state, then MAY
  release `flowPauseMu` while retaining the stage claim through drain → transition →
  enqueue. `PauseFlow` takes each stage's claim before inspecting/pausing it, and
  its ownership decision uses **eligible FSM status (not only `IsActive`)** — or the
  Manager exposes synchronously-registered pending+active ownership so a
  transition-committed-but-semaphore-queued resume isn't skipped. (The earlier
  "no-`flowPauseMu`" idea is rejected — it left a window where a resume commits
  after `rejectIfReviewPaused` saw nil but before `PauseFlow` wrote its marker.)

### 5. `Pause` returns `applied`

`Pause` → `(applied bool, err error)`; `applied` true only when its `EvPause` CAS
won. HTTP pause handler returns `{applied}` (like revise); the client type + the
frontend use it so the composer claims ownership only over a pause it actually won.

### 6. HTTP + orchestrator gates

- `handleRevise` (`pkg/server/handlers.go`): accept `paused` in the pre-filter
  (must not be stricter than the orchestrator, which stays authoritative).
- `Orchestrator.Revise`: `paused` branch → `resumePaused(..., feedback strategy)`.
- **Conflict contracts (pick one per endpoint, stated to remove ambiguity):**
  send/revise CAS-miss (`applied=false`) → **409** (existing behavior; the composer
  keeps the draft on 409). `pause` → **200 `{applied}`** (the composer needs the
  boolean to decide ownership; a 409 would surface as a thrown error). Different
  endpoints, deliberately different contracts.

### Feedback persistence — durable atomic, not bare append

`state.SaveFeedback` appends directly without fsync and can leave partial data on a
crash (`state.go:565`), so "write failed ⇒ stage safely paused & retryable" isn't
guaranteed. The feedback strategy uses a **durable atomic** persist — reuse the
fsync/atomic path behind `SaveFeedbackOnce`, or add a durable atomic append helper —
before the FSM transition.

### 7. Lifecycle

No new mapping. Sequence: focus → `stage_paused` (EvPause); send → `EvRevise`
emits `stage_revision_started` (empty phase, since `phaseForStatus(paused)`→"").
No `stage_resumed` is emitted for send-from-paused (that event maps from
`EvContinue`, which the plain resume path still emits). Documented + asserted.

## Frontend

### `usePauseOnFocus` hook (owned by App, above the composer)

State lives above `FeedComposer` because `paused` is an attention status and the
composer can remount (attention auto-open, stage reselection).

- **Toggle pref:** `localStorage` `afm.pauseOnFocus`.
- **Op state machine** (refs, promise-sequenced), keyed by stageId:
  `idle → pausing → owned → (sending | resuming) → idle`.
- **Ownership** established only when `pauseStage` resolves `applied===true`; never
  claims a manual/cross-tab pause. Released only after the resuming/sending call
  succeeds.
- All of send/blur/toggle-off/resume-now **await** any in-flight pause (no request
  reordering). Duplicate Continue (blur + resume-now) deduped by the `resuming`
  state.
- **Stage change / navigation:** if we still own a pause on the previous stage, we
  **Continue it before discarding ownership** (never strand). Ownership is keyed to
  the original stage and survives composer remount.
- **Deferred attention (not general suppression):** today `useWorkspaceView`
  permanently marks even *suppressed* arrivals as handled (`workspace-view.ts:164`),
  so reusing the general suppression signal would drop the attention permanently if
  we later release ownership while the stage is still paused. Instead add a
  **targeted deferred-attention** path: while a composer-owned pause is held for a
  stage, that stage's paused-attention is deferred (not consumed); on release it can
  still auto-open. Scope the deferral to the owned window
  (`pausing|owned|sending|resuming`) for that stage only — existing attention UX for
  other stages/kinds is untouched.
- **Ordering (corrected):** `usePauseOnFocus` must be **stage-ID-driven and
  reconciled from `stages`**, instantiated **before** `useWorkspaceView` — because
  `selectedStageId`/`workspaceStage` are derived *after* `useWorkspaceView`
  (`App.tsx:153`), so ownership can't depend on them without a cycle. The hook keys
  ownership by stageId from `stages`.
- **Reconcile:** if live status leaves `paused` while we still "own" it (external
  Continue), clear ownership.

### `PasteableTextarea`

Add optional `onFocus?`/`onBlur?` props forwarded to the inner `<textarea>`
(currently absent); existing callers unaffected.

### Composer visibility (`App.tsx` `noteTarget`)

`!isScript && flowPauseState==='none' && (status==='running' || (status==='paused'
&& pausedFrom ∈ {running,planning,revising,retrying}))`. Explicit allowlist (agent
present to steer); `flowPauseState==='none'` keeps the composer closed during a
review-pause where Revise/Continue reject.

### `FeedComposer` + CSS

Presentational: right-aligned toggle row, paused banner (attention vocabulary,
matches mockup), paused send tint + amber focus ring, driven by hook props. CSS in
`skins/base/feed-workspace.css`; mirror to `public/skins` via build.

## Testing (deterministic; fake-runner is the gate, real agent is smoke)

Backend (`pkg/orchestrator`, fake runner that can be held after interrupt):
- Active-drain overlap → exactly one feedback runner; released old runner no-ops.
- **Queued-behind-semaphore**: enqueue the original generation, Pause, resume,
  release the semaphore → the original callback never runs (generation guard).
- Continue vs send-from-paused concurrent → one resume, no double spawn, no strand.
- **Atomic pause+gen:** a Continue interleaving between `EvPause` and `bumpPauseGen`
  must NOT strand the stage (no running/revising-with-no-runner). Assert the claim
  makes them atomic.
- **Ref-counted drain:** an admitted (past-lease) old subprocess is fully drained
  before transition; A's `markDone` never clears B's marker; drain timeout →
  resume aborts, stage stays paused.
- Startup-recovery marker ownership: losing op never deletes winner's token; token
  lifecycle allows a later second pause/resume in the same process.
- Review-pause begins mid-resume → no resume inside the review transaction.
- Feedback write failure → stage stays `paused` (retryable), no runnerless revising.
- `Pause` returns applied on win / false on CAS-miss.
- Continue hardened: same queued-generation test against Continue.
- Lifecycle sequence assertion.
Backend (`pkg/server`): handleRevise accepts paused; pause handler returns applied;
409 on applied=false preserves the draft.
Frontend: `usePauseOnFocus` — toggle persistence; ownership only on applied;
request ordering (send never precedes pause); blur-empty-owned continues;
blur-with-text stays; toggle-off/resume-now continue; not-owned manual pause not
auto-resumed; stage-change Continues the prior owner; remount keeps ownership.
`App`: noteTarget closed during review-pause / pending-paused / script; open for
paused-from-running.

## Verification gates
`npm run test`+`typecheck`; `go build/vet/test ./pkg/orchestrator/... ./pkg/server/...`;
`make lint-ci`; `npm run build` (rebuild bundle + public/skins); live smoke run
(docker off, real short claude stage) across graphite+goga; CHANGELOG English
Improvement entry.

## Risks / invariants
- **Two guards, both required:** the epoch guard stops queued/stale callbacks from
  starting or publishing; reliable (ref-counted) drain stops an admitted subprocess
  from mutating files under the new runner. Neither alone gives "exactly one runner."
- `EvPause`+`bumpPauseGen` atomic under the per-stage claim; all resume paths share
  that claim; lock order `flowPauseMu → stageClaim`.
- Per-stage claim (never a global lock held across a drain).
- Durable-atomic feedback persist BEFORE the FSM transition; abort-leave-paused on
  any prepare/drain failure.
- Consolidate onto the shared core — no third copy of resume logic; strategies carry
  the per-caller semantics.
- Keep `go.mod` Go version untouched.

## Resolved during review (was "open")
- `resumeOwner` consolidation: keep `runResumeTransaction` as the review-specific
  outer transaction; its per-owner tail calls the shared core (which owns claim,
  admission, drain, marker, CAS, dispatch). Review-specific prep (`SaveFeedbackOnce`,
  `armResumeContext`, durable replay authorization) lives in the review strategy, not
  the core.
