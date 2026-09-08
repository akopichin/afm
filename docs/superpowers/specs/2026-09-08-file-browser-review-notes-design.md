# File-browser review notes with flow-wide pause — design (v3)

Date: 2026-09-08
Status: revised after two design-review rounds
(`2026-09-08-file-browser-review-notes-design-review.md`), pending final user
review → implementation plan.
Branch: `file-notes`

> v3 supersedes v2. The second review found deeper concurrency/recovery holes:
> the activation gate had a check-to-start race, the pause path never woke the
> event loop on agent drain, quiescence could deadlock on passive waiters,
> boolean progress flags weren't crash-idempotent across files, and the
> continuation descriptor was too coarse. Every claim below was verified against
> the actual code (file:line noted where it drives a decision). The core promise
> is now stated precisely: **while a review pause is quiescent, no AFM-controlled
> project writer is active.**

## Problem

The Docker file browser can browse/view/diff files mid-run but has no way to
click a line and attach a note (as `PlanPanel`/`DialogChannel` do for plan lines
and dialog questions). A reviewer who spots issues across several files has no
structured way to collect them and steer a stage.

Two hard constraints:

1. **Files must be stable while notes are written.** Line-anchored notes are
   meaningless if files change under them. Note-writing must pause the flow, and
   must not begin until every AFM-controlled **project writer** has actually
   stopped (**quiescence**) — not merely when statuses read `paused` (statuses go
   to `paused` synchronously, before the subprocess dies in the ≤15 s grace
   window).
2. **Pause is strictly per-stage today; there is no flow-wide pause.** We add a
   flow-wide concept as a small durable transaction that reuses per-stage pause.

## Goal

In the file browser, click a line → write a note. The first note prompts a
yes/no gate: to write notes the flow must be paused. On "yes" the flow enters a
durable review pause: all active non-script agent stages are gracefully paused,
in-flight scripts/hooks/agents are allowed to drain, and note editing unlocks
**only once the flow is quiescent**. A full-width banner shows the state
throughout. The user accumulates notes across any number of files
(path + line + text), stored durably. When done, the user picks one stage to
receive all notes as feedback; the chosen stage restarts with the notes, every
other frozen stage resumes, the pause lifts, and the flow continues.

## Locked decisions

- **Approach A — durable marker transaction + reuse.** No flow-level FSM event
  class; per-stage pause, `Revise`/`*WithFeedback`, marker+direct-IO reused.
- **Injection = resume-with-feedback (Revise-style)** into the one chosen owned
  active stage.
- **Non-target frozen stages resume via the normal `Continue` path.** Interactive
  → real `--resume`. **Non-interactive → the review-pause resume injects the
  `buildRetryContext` "previously completed actions" block at attempt 0** (decision
  A′), so they don't redo work. (Verified: `buildRetryContext` normally runs only
  at `attempt > 0` — `retry.go:87-97` — so a plain Continue would otherwise be a
  context-free fresh run.)
- **Strong file-stability contract.** Scripts/hooks/agents are never forcibly
  interrupted beyond the existing SIGINT+grace; note editing waits for full
  quiescence. The banner shows what pausing is blocked on.
- **Durable + fail-closed.** The transaction and notes survive reload/restart;
  recovery *finishes* an interrupted pause/resume. A corrupt/unreadable marker
  **fails closed** (admission stays closed, scheduling halts pending action) — it
  is never silently treated as absent.

## Non-goals (YAGNI)

- Multi-stage injection (single target).
- No new flow-scoped FSM event.
- No forcible interruption of scripts/hooks.
- No session-resume for non-interactive stages (we inject replay context instead).
- No re-anchoring a note's path/line after creation (text edit / delete only).

---

## Architecture

### Two synchronization primitives (kept distinct)

1. **`o.flowPauseMu sync.Mutex`** — serializes the whole review-pause *lifecycle*:
   `PauseFlow`, note CRUD, `InjectNotes`, `CancelNotes`, recovery. One owner at a
   time.
2. **`o.admission sync.RWMutex` + `o.admissionClosed bool`** — the **admission
   barrier** that makes "is the flow paused?" atomic with FSM activation. This is
   the fix for the check-to-start race: a plain helper checked before `EvStart*`
   is *not* atomic with the transition.

**Every path that activates new work** (scheduler `startReadyStages` /
`tryActivatePrePlanned` / `startPlanningForUnblocked`, bootstrap
`startPlanningForPending`, and the internal resume respawns) wraps its
gate-check + FSM transition in a short read-side critical section:

```
admission.RLock()
if admissionClosed { admission.RUnlock(); leave stage in pending/ready; continue }
Trigger(EvStartRun | EvStartPlanning)      // commit the activation
admission.RUnlock()
SpawnAgent(...)                            // launches goroutine; may block on semaphore later
```

The RLock spans only check→transition (no agent run), so it's cheap. `PauseFlow`
takes `admission.Lock()` (write side) to flip `admissionClosed=true` and snapshot
under it. Because activation commits happen only under RLock, once `PauseFlow`
holds the write lock and closes admission, **no new `EvStart*` can commit** — so
a status-based snapshot is complete: no stage can slip in after it.

The residual "signal lost between `shouldRun` and interrupt-channel registration"
window (verified real: `shouldRun` at `concurrency.go:141`, `interruptChans.Store`
at `retry.go:78`, with `run()` in between) is closed by an **interrupt-registration
handshake** (below), not by holding RLock across the async goroutine.

### The review-pause transaction (durable, `.afm/runs/<run_id>/`)

One marker file **`notes-pause.json`**, written **once, complete** (v2's
empty-then-rewrite created an unrecoverable crash window):

```json
{
  "version": 1,
  "operation_id": "review-<lastSeq>-<rand4hex>",   // unique even across two no-op pause cycles
  "state": "pausing",                               // pausing | paused | resuming
  "created_at": "...",
  "target_stage": "",                               // set during resuming (inject); "" for cancel
  "feedback_delivered": false,                      // mirror of the feedback-file sentinel
  "stages": [
    {
      "id": "stage-a",
      "owned": true,               // this op paused it (not the user, not a script)
      "paused_from": "running",
      "phase": "implementation",   // continuation descriptor: planning | implementation | autonomous
      "session_id": "",            // interactive only, for --resume
      "pause_applied": true,       // EvPause CAS succeeded
      "resume_applied": false      // the DURABLE resume FSM transition committed (not "spawn happened")
    }
  ]
}
```

`operation_id` includes a random suffix (Go `crypto/rand`) so two empty pause
cycles without an intervening FSM event still differ. `state=none` = marker
absent.

**Notes file `review-notes.json`** — single whole-file-rewritten document:

```json
{
  "version": 1,
  "rev": 7,                 // ETag; bumped per write; multi-tab 409
  "next_id": 8,             // persisted; IDs never reused after delete
  "notes": [
    {
      "id": "n7", "root": "project", "path": "pkg/foo/bar.go",
      "display_path": "project/pkg/foo/bar.go",
      "reference": "[AFM file: \"/work/pkg/foo/bar.go\"]",   // from workspace marker builder, NOT fmt
      "line": 42, "orig_line_text": "\tif err != nil {",
      "content_etag": "\"a1b2...\"", "text": "...", "created_at": "..."
    }
  ]
}
```

Durability: atomic temp+rename with a **unique** temp name, **fsync file + parent
dir**. Corrupt/parse failure → **quarantine** to `<name>.corrupt-<ts>` and refuse
(never overwrite evidence); unknown `version` refused. For the *marker*
specifically, a corrupt read **fails closed** (see Recovery), not "treat as
absent."

State API (direct IO): `SaveReviewNotes`/`LoadReviewNotes`/`DeleteReviewNotes`
(delete is idempotent); `SaveFeedbackOnce(stageDir, opID, text)`;
`WriteNotesPauseMarker`/`ReadNotesPauseMarker`/`ClearNotesPauseMarker`.

### Quiescence — scoped to project writers (not passive goroutines)

Verified: `IsActive(stageID)` means a `SpawnAgent` goroutine exists, not that a
subprocess is writing. `awaiting_user_input` is **not** in `EvPause`'s From-set
(`fsm.go:144`) and its agent may stay alive polling; `hook_failed` waits idle for
Retry/Skip. Counting those as "active" while the control policy blocks the actions
that would release them = a permanent `pausing` deadlock. So quiescence is scoped
to actual writers:

```
quiescent  ⇔  no OWNED agent stage is IsActive          // interrupted agents finished draining (subprocess dead)
          AND no running SCRIPT stage is IsActive        // uninterruptible; waited for (strong contract)
          AND o.pendingAfterHooks.Load()  == 0           // script_after mutates the project
          AND o.pendingReflections.Load() == 0           // reflection writes memory (may be in-project)
```

Explicitly **excluded** (not project writers): `awaiting_user_input` /
`hook_failed` passive waiters, and detached dialog-fix agents (`SpawnDetached`,
which only rewrite a `question.json` in the stage dir, tracked in `agentWG` not
`IsActive`). For an owned stage, `IsActive=false` ⇔ its `run()` goroutine returned
⇔ the executor's subprocess ended — so this genuinely waits for the ≤15 s grace
drain, which is the whole point.

Edge: an owned stage that raced `running → awaiting_user_input` (poller) before
our `EvPause` → the CAS fails → `pause_applied=false, owned=false`; it becomes an
excluded passive waiter (not resumed, its question answerable after the pause
resolves).

### Quiescence must be re-evaluated on drain (event-loop wake)

Verified: `SpawnAgent`'s deferred cleanup calls `markDone` + `sem.release()` only
— **no `WakeEventLoop`** (`concurrency.go:133-136`); and the paused-interrupt
return (`retry.go:163-165`) publishes nothing. So without a change, the last owned
agent could drain and `pausing` would never advance absent an unrelated event.

**Change:** `SpawnAgent`'s deferred cleanup calls `m.WakeEventLoop()` after
`markDone`. The event loop, on the drain wake (existing `eventAgentDrained`),
calls `maybeAdvanceFlowPause()`: if `state==pausing` and quiescent → commit
`state=paused` (durable) and unlock note editing. (`concurrency.go` joins the
touched-files list.)

### `PauseFlow` — atomic snapshot, single complete marker

Under `flowPauseMu`:

1. `admission.Lock()` → `admissionClosed=true`. No new `EvStart*` can commit.
2. **Snapshot by FSM status** (stable, admission closed): stages with status ∈
   {running, planning, revising, retrying}, **not** a script, **not** already
   `paused`. For each, read its **continuation descriptor** (see below) →
   `{owned:true, paused_from, phase, session_id}`.
3. `admission.Unlock()` (keep `admissionClosed=true`; the bool, not the lock, is
   the barrier).
4. **Persist one complete `pausing` marker** (all owned entries, `pause_applied`
   still false). This is the atomic accept point: a crash *before* it = "pause not
   durably accepted" (no marker; ordinary recovery resumes stages normally); a
   crash *after* it = fully reconstructible.
5. Apply per-stage `EvPause` (+ `bumpPauseGen` + signal `interruptChans`) to each
   owned stage; set `pause_applied=true` as each CAS succeeds. A stage that
   completed between snapshot and here loses the CAS → `owned=false,
   pause_applied=false` (it's `done`, not ours). Persist.
6. Leave `state=pausing`; the drain-driven `maybeAdvanceFlowPause` commits
   `paused` at quiescence.

Running **script** stages are never `EvPause`d; quiescence waits for them; their
successors stay blocked by `admissionClosed`.

### Interrupt-registration handshake (closes the lost-signal window)

At the `runWithRetry` attempt-loop head, **after** `interruptChans.Store`
(`retry.go:78`) and before the first `agentFn`, add:

```
if o.currentStatus(s.ID) == state.StatusPaused { return }   // pause landed in the shouldRun→register gap; self-abort
```

So even if `PauseFlow`'s signal was dropped (channel not yet registered), the
freshly-started attempt observes `paused` and aborts before running the
subprocess — no project write slips through. This also fixes a latent
manual-Pause bug and is a global improvement.

### Continuation descriptor (deterministic resume)

Verified: nothing persists the executing phase; `running` covers implementation,
inline review, and autonomous; `detectInterruptedPhase` uses session mtimes
(interactive only); normal review runs **inline inside `runImplementationAgent`**
under phase `implementation` (`agents.go:284-301`) — there is no distinct review
FSM status on the normal path.

**Descriptor** `o.continuation sync.Map` (stageID → `{phase, session_id}`), where
`phase ∈ {planning, implementation, autonomous}` — **inline review folds into
implementation** (resuming such a stage with `runImplementationWithFeedback`
correctly redoes impl+review with the feedback, so no separate review phase is
needed). Set the descriptor at the **spawn boundary** (right where the activation
path commits `EvStart*` and calls `SpawnAgent`), so a stage queued behind the
semaphore already has it. Keep it across retry backoff; clear on
terminal/awaiting. `PauseFlow` copies it into each owned marker entry; resume
reads the marker `phase` — no mtime guessing. Before-hook re-run on resume is left
exactly as today's Continue/recovery behavior (not a regression; no `before_hook_done`
tracking added — YAGNI).

### Central flow-pause policy (two windows)

- **Admission** (auto-scheduler + bootstrap + internal respawns) is gated by
  `admissionClosed`, which is `true` for `state ∈ {pausing, paused}` and set
  **false at the start of `resuming`** so the resume cascade can spawn.
- **Control policy** (user/HTTP actions) blocks the whole
  `state ∈ {pausing, paused, resuming}` window with a machine-readable 409
  (`{"error":"flow_paused"}`): `Continue`, `Approve` (+ headless auto-approve),
  `Retry`, `Revise`, `Button`, dialog `NotifyAnswer`, non-interactive
  **auto-answer** (poller suppressed while paused), manual `Pause`. The lifecycle
  resumes via **private** helpers (`SpawnAgent(run<phase>WithFeedback)` /
  internal `resumeStageAtStatus`) that bypass these public guards, so the guards
  block *all* external callers unconditionally (a second tab or poller can't
  race).

### `InjectNotesAndResume(targetStageID)` — idempotent, recovery-safe

Precondition (server-enforced, machine-readable errors): `state==paused` (else
`not_paused` 409), `len(notes) > 0` (else `no_notes` 400), `targetStageID` is an
owned `pause_applied` entry (else 400). Under `flowPauseMu`:

1. `state=resuming`, `target_stage=targetStageID`; set `admissionClosed=false`;
   persist. (Cascade may now spawn.)
2. **Feedback once:** `SaveFeedbackOnce(targetDir, operation_id, rendered)` embeds
   `<!-- afm-review-op: <op_id> -->` and no-ops if that sentinel is already
   present — so a crash between append and flag can't double-append. Persist
   `feedback_delivered=true` (a mirror; the file sentinel is authoritative).
3. **Resume the target with feedback.** FSM: add `paused` to `EvRevise`'s From-set.
   `EvRevise` (paused→revising), mark `resume_applied=true` (**durable transition
   committed**, persisted), then `SpawnAgent(run<phase>WithFeedback)` dispatched by
   the marker `phase` — for non-interactive, the runner injects the attempt-0
   replay context (decision A′). If `phase==planning`, `runPlanningWithFeedback`
   (tolerates missing plan — `LatestPlanVersion` returns `0,"",nil`).
4. **Resume every other owned `pause_applied` stage** via the internal Continue
   path (`EvContinue` → `paused_from` → `resumeStageAtStatus`), dispatched by the
   marker `phase`; mark each `resume_applied=true` on the durable transition; then
   respawn (interactive `--resume`; non-interactive with attempt-0 replay). A
   `planning` non-target resumes via the fresh `runPlanningAgent` (as today) —
   this differs from the target's `runPlanningWithFeedback` on purpose (with vs
   without feedback), not a contradiction.
5. Re-drive scheduling so admission-blocked pending stages activate.
6. `DeleteReviewNotes` (idempotent), then `ClearNotesPauseMarker` **last**.

`CancelNotesAndResume()` = same without a target and without feedback.

### Recovery matrix (finish, don't restart edit mode) — runs before bootstrap

**Init ordering:** the marker is read during orchestrator construction; the HTTP
server (started before `Run`) sees the marker state immediately, and control
actions are rejected until flow-pause recovery has run. Flow-pause recovery runs
**before** `startPlanningForPending` so bootstrap can't resume a frozen stage
first (bootstrap's resume paths also honor `admissionClosed`).

- **absent** → normal.
- **corrupt/unreadable** → **fail closed:** quarantine a copy, keep
  `admissionClosed=true`, do **not** start normal scheduling; surface a fatal
  error / attention state requiring explicit action. (Silently continuing would
  resume the frozen stages the pause was protecting.)
- **`pausing`** → `admissionClosed=true`; reconcile each owned entry against the
  log (still-active & not-paused → re-apply `EvPause`; already `done` → drop
  ownership); processes died with the crash, so re-evaluate quiescence (typically
  immediate) → `paused`. Editing locked until quiescent.
- **`paused`** → banner + notes; editing after quiescence re-confirmed.
- **`resuming`** → **finish resuming:** `SaveFeedbackOnce` is a no-op if the
  sentinel exists; for each owned stage, reconcile from the event log — respawn if
  its active status implies a missing process — regardless of the ephemeral
  `resume_applied` flag (which only asserts the durable transition, not a live
  process); `DeleteReviewNotes`; clear marker.

`shouldExit` returns **false while any marker exists** (`state != none`), so the
server stays up for the reviewer even if the last script finished and all stages
are terminal.

### IsActive single-callback invariant

Quiescence relies on the per-stage `IsActive` boolean. Per-stage there is at most
one active agent callback at a time — every launch is FSM-CAS-guarded (only one
`EvStart*`/`EvRevise` wins), so a callback's exit can't clear the flag out from
under a second concurrent callback for the same stage. The design depends on and
documents this invariant (no ref-counting needed).

---

## HTTP API (`/api/flow/*`) — machine-readable error codes

- `POST /api/flow/pause` → `{ paused_stages:[], blockers:[] }`; existing marker →
  returns current transaction (no second pause).
- `GET  /api/flow/notes` → `{ rev, notes:[] }` (lock-free read).
- `POST /api/flow/notes` `{root, path, line, text, content_etag, expected_rev}` →
  created note. `state==paused` (else `flow_not_paused` 409); server resolves
  `abs`/`display_path`/`reference` from `{root,path}` via `workspace` (client
  absolute path never trusted); re-reads under quiescence and validates regular
  readable file, `line` within content (`stale_line` 409 on mismatch),
  `content_etag` (`stale_etag` 409), `expected_rev` (`rev_conflict` 409), non-blank
  `text` ≤ `maxNoteTextLen`, note count ≤ `maxNotes`. One note per `(root,path,line)`
  (replace). Snapshots `orig_line_text`.
- `PUT /api/flow/notes/{id}` `{text, expected_rev}` / `DELETE .../{id}?expected_rev=`.
- `POST /api/flow/notes/inject` `{stage_id}` → `no_notes` 400 if empty, target
  validated. `POST /api/flow/notes/cancel`.
- `/api/status` gains `flow_pause_state` (`none|pausing|paused|resuming`),
  `flow_quiescent` bool, `flow_paused_stages` []string, `flow_pause_blockers`
  []string (owned active stage ids + `"after-hooks"`/`"reflections"`/`"script:<id>"`).

## Frontend (`pkg/web/dashboard/src`)

- FileViewer line-comment UX (reuse `PlanPanel`'s `.plan-line`/`line-comment-*` +
  `PasteableTextarea`, `●` on annotated lines).
- Pause gate: first note while `flow_pause_state==none` → confirm *"…поставить на
  паузу?"*; **Yes** → `POST /api/flow/pause`, editor disabled showing *"pausing —
  waiting: <blockers>"* until `paused && flow_quiescent`, then **reload the
  selected file** (defend the click-before-quiescence window), capture its ETag,
  enable editor; **No** → close, flow untouched.
- Full-width banner (App.tsx, after `<FlowHeader/>`), by state; `paused` adds
  **Send notes** + **Cancel**.
- Notes modal (from **Send**): grouped by file, edit/delete, stage **picker**
  (`flow_paused_stages`); empty picker → disclosed upfront *"no active stage to
  receive notes"*, **Send** disabled. 409 codes distinguish flow-pause vs
  stale-ETag/rev conflicts → targeted "reload" messaging.
- `use-status.ts` maps new fields; `run-client.ts` gains the seven calls.

## Injection rendering (`feedback.md`)

Deterministic order (files by `display_path`, notes by `line`); file titled by the
stored safe `reference`; `display_path` **JSON-quoted** (filenames may contain
`)`/newlines):

```
<!-- afm-review-op: review-1731-9f2a -->
## Review notes
### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"
- Line 42: <text>
- File: <text>
```

Total rendered size capped (`maxRenderedFeedback`); overflow reported before
inject, not silently truncated.

## Edge cases

- **No pausable agent stage** — pause still engages; `flow_paused_stages` empty →
  Send disabled, disclosed upfront; `shouldExit` held by the marker.
- **Script/hook running** — waited for (Strong); listed as a blocker.
- **User-manually-paused stage** — not owned; never resumed by inject/cancel.
- **Forward-driving control during pause** — 409 `flow_paused` from any tab/poller.
- **Crash at any durable step** — recovery finishes idempotently; feedback never
  double-appended (file sentinel); notes deletion idempotent; resume respawns from
  the log.
- **Pause in the shouldRun→register gap** — handshake self-aborts the attempt.
- **Pause vs awaiting_user_input / hook_failed** — excluded from quiescence; reach
  a well-defined `paused`, never a stuck `pausing`.

---

## Testing

**`pkg/state`** — notes roundtrip (`next_id` monotonic across delete, `rev` bump,
missing→empty); corrupt/unknown-version quarantine without overwrite; marker
write/read/clear; `SaveFeedbackOnce` no-ops on repeated op-id; fsync path.

**`pkg/orchestrator`**
- **Admission race:** deterministically stop a scheduler between gate-check and
  `EvStart*`, run `PauseFlow`, release — the stage is either owned by the snapshot
  or remains unstarted; repeated for the retry/revise/continue spawn sites; a
  running script completing during pause unblocks a dependent that does **not**
  strand.
- **Wake-on-drain:** interrupt the last owned agent, let it exit without a
  completion event → `pausing` advances to `paused` with no extra external event.
- **Quiescence deadlock:** pause while a stage is `awaiting_user_input`; while a
  before-hook is `hook_failed`; while an after-hook waits Retry/Skip — each reaches
  a defined `paused` (or documented error), never a stuck `pausing`.
- **Crash matrix:** crash before marker persist (pause not accepted); after
  complete persist; while an owned stage still shows an active log status (ordinary
  recovery cannot spawn it before flow-pause recovery); crash after each durable
  write/side-effect in pause/inject/delete/resume → repeated recovery converges to
  exactly one feedback block, eventual note deletion, one live continuation per
  owned stage.
- **Double-launch:** inject/cancel immediately after PauseFlow; fast plain Continue
  of a non-target — none double-spawns.
- **Handshake:** pause landing in the shouldRun→register gap self-aborts attempt 0.
- **Resume context (decision A′):** the first resumed **non-interactive** attempt's
  actual prompt/stdin contains the replay block; interactive resumes its session;
  covers default, preplanned/autonomous, and custom runners.
- **Phase dispatch:** inject into planning (no plan yet), implementation, inline
  review, autonomous, planning-retry, implementation-retry — phase-correct;
  non-interactive (no session files) too.
- **Lifecycle guards:** every forward control → 409 `flow_paused`; auto-answer
  suppressed; `shouldExit` false while a marker exists (incl. all-terminal /
  last-script-done).
- FSM: `EvRevise` From includes `paused`.

**`pkg/server`** — CRUD gating + `stale_etag`/`rev_conflict`/`stale_line`/`no_notes`
codes; inject target validation; server-side `{root,path}` resolution (absolute
path never trusted); `/api/status` fields; paths with quotes/backslashes/newlines/
non-ASCII round-trip via stored `reference`; rendered `display_path` JSON-quoted.

**Frontend** — line comment + `●`; pause confirm; editor disabled until
`paused && quiescent`, file reloaded on unlock; banner per state with blockers;
empty-target disclosure; notes edit/delete; picker inject; 409 code handling;
`use-status` mapping.

**Live verification** (`verify` skill, Docker + real browser): click a line → pause
gate → watch `pausing` wait on a real draining agent (and a running script if
present) → write notes across two files → inject into one stage, see its
`feedback.md` receive the notes once + others resume → banner clears → flow
continues; reload mid-`paused` and confirm pause + notes survive.

## Files touched (anticipated)

- `pkg/state/state.go` — notes + marker IO (durable, quarantine, `SaveFeedbackOnce`).
- `pkg/orchestrator/` — `flowPauseMu`, `admission`/`admissionClosed`,
  `continuation` map, marker transaction, `PauseFlow`/`Inject`/`Cancel`/note CRUD,
  admission-wrapped activation (`scheduling.go`/`recovery.go`), `maybeAdvanceFlowPause`
  on the drain wake, control policy, `shouldExit` guard, init-ordering recovery.
- `pkg/orchestrator/concurrency/concurrency.go` — `WakeEventLoop` on drain.
- `pkg/orchestrator/retry.go` — interrupt-registration handshake; attempt-0 replay
  context for resumed runs.
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` From += `paused`.
- `pkg/executor` / runner_factory — thread the attempt-0 resume context.
- `pkg/server/` — `/api/flow/*`, `statusResponse` fields, `FlowActions` wiring,
  note resolution/validation, machine-readable error codes.
- `pkg/web/dashboard/src/` — FileViewer line comments, pause confirm, banner, notes
  modal + picker, `run-client`, `use-status`, CSS.
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.

## Minimum acceptance criteria (must be mechanically testable in the plan)

1. No activation crosses the pause snapshot without being owned or staying blocked.
2. Draining the last owned worker re-evaluates the coordinator with no extra event.
3. Questions / failed-hook waiters cannot deadlock `pausing`.
4. Every crash point in pause/inject/delete/resume converges under repeated recovery.
5. Feedback delivered at most once per operation; note deletion eventually
   completes; active FSM states respawned when necessary.
6. The first resumed non-interactive attempt receives the promised replay context.
7. Every queued/hooked/retrying/implementing/reviewing/planning stage has an
   unambiguous continuation descriptor (`phase`, session).
8. Corrupt/unreadable pause state fails closed before normal scheduling starts.
