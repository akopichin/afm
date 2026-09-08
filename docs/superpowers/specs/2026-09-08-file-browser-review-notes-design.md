# File-browser review notes with best-effort pause — design (v6, hybrid)

Date: 2026-09-08
Status: revised after five review rounds. Hybrid model confirmed sound; v6 closes
the remaining bounded per-stage correctness issues. Ready for the implementation
plan once accepted.
Branch: `file-notes`

> **v6 supersedes v1–v5.** The hybrid model (note correctness via a content
> anchor; pause is best-effort UX) is settled and endorsed by review. v6 fixes the
> four bounded issues the v5 review found — all per-stage, none reviving global
> quiescence: no resume/old-callback overlap, correct `resume_kind` derivation via
> a small runner-kind registry, a clean split between the activation hold and the
> review-transaction guard, and an atomic exactly-once feedback write. Plus a
> per-status recovery matrix and fail-closed handling of a corrupt `resuming`
> marker.

## Problem

The Docker file browser can browse/view/diff files mid-run but has no way to click
a line and attach a note (as `PlanPanel`/`DialogChannel` do for plan lines and
dialog questions). A reviewer who spots issues across several files has no
structured way to collect them and steer a stage.

We *want* files stable while notes are written, but do not make it a hard
concurrency guarantee. The pause stops the agent stages (the common writers); for
the rare case a file still changes (an uninterruptible script, a race), each note
carries the exact original line text it was written against, so the target agent
always reconciles against what the reviewer actually saw.

## Goal

Click a line → write a note anchored to the file's current content. The first note
prompts a yes/no gate: the flow enters best-effort **review mode** (active agent
stages stopped, new activations held) and a full-width banner appears. The user
accumulates notes across files; each note stores its text, line, the **original
line text**, and a **content digest** of the file as seen. When done, the user
picks one stage to receive all notes as feedback; that stage restarts with the
notes, every other paused stage resumes, review mode lifts, and the flow continues.

## Locked decisions

- **Hybrid model.** Note correctness = content anchor (always carried); pause =
  best-effort UX.
- **Approach A — durable marker + reuse.** No flow-level FSM event; per-stage
  pause, `Revise`/`Continue`/`*WithFeedback` reused, marker+direct-IO.
- **Injection = resume-with-feedback (Revise-style)** into the chosen paused stage,
  dispatched by a recorded `resume_kind`.
- **Other paused stages resume via the existing runners**, chosen by `resume_kind`;
  interactive `--resume`, non-interactive attempt-0 `buildRetryContext` replay (A′).
- **Own only stages that are `IsActive` at pause**; resume each only after its old
  callback has drained. Bounded per-stage — no launch tokens, no global quiescence.
- **Durable resume transaction.** The marker (`paused`/`resuming`) is the authority
  for control guards, banner, `shouldExit`, and finalization, until it is deleted.
- **Content digest, not mtime+size**, and it is always carried into feedback.

## Non-goals (YAGNI)

- No quiescence guarantee, launch tokens, writer counting, or continuous
  continuation/session descriptor.
- No new flow-scoped FSM event.
- No forcible interruption of scripts/hooks.
- Single injection target; text edit/delete only.

---

## Architecture

### Notes storage — content-anchored (durable, `.afm/runs/<run_id>/`)

`review-notes.json`:

```json
{ "version": 1, "rev": 7, "next_id": 8,
  "notes": [ {
    "id": "n7", "root": "project", "path": "pkg/foo/bar.go",
    "display_path": "project/pkg/foo/bar.go",
    "reference": "[AFM file: \"/work/pkg/foo/bar.go\"]",   // workspace marker builder, NOT fmt
    "line": 42,                        // >=1 line note; null file-level
    "orig_line_text": "\tif err != nil {",   // exact line as seen (null for file-level) — ALWAYS rendered
    "content_sha": "sha256:...",       // digest of the whole file as seen
    "text": "...", "created_at": "..." } ] }
```

`line:null` = file-level. **Replacement** on an existing `(root,path,line)` keeps
`id`/`created_at`, refreshes `text`/`content_sha`/`orig_line_text`. Durability:
atomic temp+rename (unique temp), fsync file + parent dir; corrupt/unknown-version
→ quarantine + start empty (advisory). API: `SaveReviewNotes`/`LoadReviewNotes`/
`DeleteReviewNotes` (idempotent), content-hash helper.

### The runner-kind registry (enables correct dispatch)

`o.runnerKind sync.Map` (stageID → `planning|implementation|review|autonomous`) is
set **at the spawn site** of every main runner (`runPlanningAgent`,
`runImplementationAgent`, `runReviewAgent`, `runAutonomousAgent`, and their
`*WithFeedback` variants — and at the queued spawn site, so a callback still behind
the semaphore already has its kind), and cleared on terminal/`awaiting`. This is a
small per-stage label, **not** the rejected v3 continuous-continuation model: no
sessions, no phase reconstruction. It is the authoritative source of `resume_kind`,
so dispatch never relies on `detectInterruptedPhase` (which only sees interactive
session files and would misroute non-interactive implementation/auto/retrying
targets to planning).

### The pause marker — the review-transaction authority

`notes-pause.json`:

```json
{ "version": 1, "operation_id": "<128-bit hex>",
  "state": "paused",            // paused | resuming
  "mode": "",                   // inject | cancel  (while resuming)
  "target_stage": "",           // inject target (while resuming)
  "created_at": "...",
  "owned": [ { "id": "stage-a", "resume_kind": "implementation", "from_revising": false } ] }
```

`owned` lists only stages this pause actually moved to `paused` (CAS winners that
were `IsActive`). `resume_kind` comes from the registry. `from_revising` marks an
owner paused mid-`revising` so its resume uses the `*WithFeedback` runner (to keep
its pre-existing `feedback.md`), even when it is a non-target plain resume.

**The marker's existence + `state` is `reviewTxnActive`** — the single authority
consulted by the public control policy, the banner/status, `shouldExit`, and the
finalization guard. It stays authoritative through `resuming` until the marker is
durably deleted. A separate **private** `o.activationHeld atomic.Bool` gates only
the scheduler's activation sites and is cleared earlier (when internal resume is
allowed to spawn). The two are never conflated (v5's overloaded `flowPaused` bug).

### `PauseFlow` — own only live callbacks; truthful marker

Under `flowPauseMu`, atomically exclusive with finalization (below):

1. **Set `activationHeld=true`.** Activation sites (`startReadyStages`,
   `tryActivatePrePlanned`, `startPlanningForUnblocked`, bootstrap
   `startPlanningForPending`, **and its active-status recovery respawns**) skip
   work while held. `concurrency.shouldRun` untouched (no stranding / `script_after`
   leak). Best-effort: a stage mid-activation may still run — the note anchor covers
   it.
2. **Snapshot candidates:** status ∈ {running, planning, revising, retrying},
   **and `IsActive`**, non-script, not already `paused`. A stage that is
   status-active but only **queued** behind the semaphore (not yet `IsActive`) is
   **not** owned — it continues best-effort (and its own callback, if we had
   paused it, would anyway be dropped by `shouldRun` on dequeue). Owning only live
   callbacks is what makes the drain-before-resume rule below well-defined.
3. **Apply `EvPause`** to each candidate (existing `Pause` internals: `EvPause` +
   `bumpPauseGen` + signal `interruptChans`). A candidate that left the From-set
   first loses the CAS and is dropped from `owned`.
4. For each winner, record `resume_kind` (from the registry) and
   `from_revising := (PausedFrom==revising)`.
5. **Persist the marker** (`state=paused`, `owned=winners`).

Running **script** stages finish (successors held); we don't wait for drain to
enable editing — notes are content-anchored.

**Weak pre-marker crash semantics (explicit):** a crash after some `EvPause` but
before the marker write leaves those stages durably `paused` with no marker — "not
durably accepted"; the user manually Continues them. **Initial-marker-write failure**
rolls back `activationHeld=false` and returns an error **that surfaces the IDs of
the stages already paused** (they stay `paused`; the user Continues them) — not a
silent "rolled back."

### Injection & cancel — async resume under the run context, drained per owner

`InjectNotesAndResume(targetStageID)` (server-enforced: `reviewTxnActive`;
`len(notes)>0` else `no_notes`; target ∈ `owned` **and currently `StatusPaused`**
else 400). Under `flowPauseMu`:

1. **Persist `state=resuming, mode=inject, target_stage`** (marker stays — this is
   the durable resume intent, and keeps `reviewTxnActive` true).
2. Render feedback (§ rendering). **`SaveFeedbackOnce(targetDir, operation_id,
   rendered)`** — read the existing `feedback.md`, no-op if the
   `<!-- afm-review-op: <id> -->` sentinel is present, else construct the complete
   new content (old + sentinel + block) and persist atomically (unique-temp + file
   fsync + rename + dir fsync). Serialized by `flowPauseMu`; external `Revise` is
   blocked by the control policy, so no concurrent feedback writer. Returns success
   only when sentinel+block are durable **together** — never a sentinel-only or
   partial block.
3. **Clear `activationHeld=false`** (internal resume may now spawn; scheduling can
   re-drive). The marker remains → `reviewTxnActive` stays true.
4. Return to the caller; the **resume runs asynchronously under the run context**
   (like the existing HTTP approve/revise spawns), so the HTTP handler doesn't
   block on per-owner drain. It is durable-recoverable via `state=resuming`.

The async resume worker, for each `owned` entry, in order:
- **Wait until the owner's old callback has drained (`IsActive==false`).** The
  interrupted process returns `ErrUserInterrupted` while status is still `paused`,
  so `runWithRetry` treats it as Pause and returns cleanly — no double-launch, no
  Revise mis-read. (Owners were IsActive at pause, so this wait is well-defined and
  bounded by the ≤15 s grace.)
- **Transition + spawn via a private resume dispatcher keyed on `resume_kind`:**
  - **target:** `EvRevise` (paused→revising) + `run<resume_kind>WithFeedback`.
  - **non-target:** if `from_revising` → `EvRevise` + `run<resume_kind>WithFeedback`
    (preserves its prior `feedback.md`); else `EvContinue` (paused→`PausedFrom`) +
    the plain `run<resume_kind>Agent`, with attempt-0 replay for non-interactive
    (A′). This dispatcher is new/private — we do **not** claim the unchanged
    `Continue→resumeStageAtStatus` consumes a kind.
  - **`resume_kind==review`:** a standalone review runner publishes completion with
    `phaseReview`; its successful completion is routed through `completeStage` (so
    the stage reaches `done` and runs the normal cascade), unlike inline review
    whose outer implementation runner publishes the final completion.
- Re-drive scheduling so activation-held pending stages start.

When every owner has transitioned+spawned: **`DeleteReviewNotes`, then delete the
marker — last.** Only now is `reviewTxnActive` false.

`CancelNotesAndResume()`: `state=resuming, mode=cancel`; per owner drain then plain
resume (or `*WithFeedback` if `from_revising`); delete notes + marker last. No
feedback.

**Control policy.** While `reviewTxnActive` (marker present, `paused` **or**
`resuming`), external forward-driving actions
(`Continue`/`Approve`/`Retry`/`Revise`/`Button`/dialog `NotifyAnswer`/manual
`Pause`) return `409 {"error":"flow_paused"}`. The lifecycle's own resume uses the
private dispatcher, which bypasses the guard.

### Recovery — per-status matrix, distinguishes restart from in-process retry

On start, `ReadNotesPauseMarker`. **Recovery runs before ordinary bootstrap can
resume any stage** (`activationHeld` is set from the marker first).

- **absent** → normal.
- **`state=paused`** → `activationHeld=true`; owners are already durably `paused`
  (existing recovery keeps them so); suppress every bootstrap respawn for owners;
  banner returns; nothing auto-resumes.
- **`state=resuming`** → finish, driving each owner by its **current event-log
  status** (a restart has no live process even when the log says `running`):
  - `paused` → run the intended target/plain resume transition + spawn (by
    `resume_kind`).
  - active status *after* a committed transition (`revising`/`running`/`planning`)
    → respawn the missing process with the recorded kind.
  - `awaiting_user_input` / `hook_failed` → restore the existing passive wait; do
    not spawn.
  - `done` / `failed` → resolved; do not restart.
  - invalid/unexpected → retain the marker and surface an actionable error.
  `SaveFeedbackOnce` is idempotent (sentinel); then `DeleteReviewNotes` + delete
  marker. This matrix is for **process-restart** recovery; an **in-process** retry
  after a marker-clear failure (a live callback may already exist) does not respawn
  — it only retries the delete.
- **corrupt/unreadable:**
  - a corrupt **`paused`** marker → fail-open (log + quarantine; owners stay
    `paused` for manual Continue) — the documented advisory tradeoff.
  - a corrupt **`resuming`** marker → **fail closed:** it may be the only record of
    target/mode/owners after feedback was delivered and partial resume; keep
    `activationHeld=true`, do not start normal scheduling, surface the quarantined
    path + a concrete manual-recovery action. Owners' log statuses may be mixed —
    do **not** assume all remain paused.

### Finalization guard (atomic with pause acceptance)

A shared lock/CAS (`o.finalizeMu` guarding a `finalizing` flag) is held by **both**
the event-loop transition into `runEndOfRunMemory`/`Run` return **and** `PauseFlow`
from its finalization check through setting `activationHeld`+persisting the marker.
So the two are mutually exclusive. `PauseFlow` during finalization →
`409 run_finalizing`; a pause after `Run` returns is a no-op. `shouldExit` consults
**`reviewTxnActive` (the marker)**, so it stays false through the entire
`paused`+`resuming` lifecycle — including a fast target completing while other
owners are still resuming — until the marker is deleted.

### Direct-IO failure semantics

- **Initial marker write fails** → roll back `activationHeld=false`, return an error
  listing the already-paused winner IDs (they remain `paused`).
- **Feedback write fails** → leave the `resuming` marker + notes for retry; error.
- **A resume FSM transition's log write fails** → existing storage-fatal path (it's
  an event-log write); the `resuming` marker lets recovery re-drive.
- **Final marker delete fails after a successful resume** → recoverable: recovery
  re-enters `resuming`; `SaveFeedbackOnce`/transitions are idempotent; no second
  live spawn (in-process retry only re-deletes; restart recovery reconciles from the
  log); retry the delete.

### The content anchor is always carried

Every line note **always** renders its stored `orig_line_text` (JSON-quoted). The
injection-time hash comparison is a diagnostic only (`changed-at-injection`), never
proof the file is still unchanged when the agent runs:

```
<!-- afm-review-op: <id> -->
## Review notes
### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"
- Line 42; original: "\tif err != nil {": <text>
- Line 88 (⚠ content differed at injection); original: "\tfoo()": <text>
- File: <text>
### [AFM file: "/work/gone.go"]  "project/gone.go"  (⚠ file unavailable at injection)
- Line 5; original: "package gone": <text>
```

Missing/unreadable at injection (deleted/renamed/symlink/binary/over-limit) →
`file unavailable at injection` marker + stored `reference` + original text; the
batch is never aborted. `display_path` JSON-quoted; deterministic order; size capped
(`maxRenderedFeedback`, overflow reported pre-inject).

---

## HTTP API (`/api/flow/*`)

- `POST /api/flow/pause` → `{ paused_stages:[] }` (owners); existing marker → returns
  it; during finalization → `409 run_finalizing`; initial-marker failure → error
  with the paused winner IDs.
- `GET  /api/flow/notes` → `{ rev, notes:[] }`.
- `POST /api/flow/notes` `{root, path, line|null, text, content_sha, expected_rev}`
  → created/replaced. `reviewTxnActive` with `state=paused` required
  (`flow_not_paused` 409 otherwise — notes can't be added mid-`resuming`). Server
  resolves `abs`/`display_path`/`reference` from `{root,path}` via `workspace`
  (absolute path never trusted); recomputes `content_sha`, requires match
  (`stale_content` 409); line note → validate range + snapshot `orig_line_text`;
  `line:null` = file-level; `expected_rev` (`rev_conflict` 409); non-blank text ≤
  `maxNoteTextLen`; count ≤ `maxNotes`.
- `PUT /api/flow/notes/{id}` / `DELETE .../{id}?expected_rev=`.
- `POST /api/flow/notes/inject` `{stage_id}` (`no_notes` 400; target `owned` +
  `StatusPaused`) / `POST /api/flow/notes/cancel`.
- `/api/status` gains `flow_pause_state` (`none|paused|resuming`) and
  `flow_paused_stages` []string (currently-`paused` owners, for the picker). Notes
  are editable only in `paused`.

`o.flowPauseMu` serializes `PauseFlow`/CRUD/`Inject`/`Cancel`; `rev`/`expected_rev`
guard multi-tab conflicts.

## Frontend (`pkg/web/dashboard/src`)

- FileViewer line-comment UX (reuse `PlanPanel`'s `.plan-line`/`line-comment-*` +
  `PasteableTextarea`, `●` on annotated lines); client computes + sends the fetched
  file's `content_sha` per add.
- Pause gate: first note while `flow_pause_state==none` → confirm; **Yes** → `POST
  /api/flow/pause`, enable editor; **No** → close.
- Full-width banner (App.tsx, after `<FlowHeader/>`), by `flow_pause_state`. Honest
  wording (the pause is best-effort): *"⏸ Review mode — new stages held; active work
  pausing best-effort (N notes)"* + **Send notes** + **Cancel** (in `paused`);
  *"Resuming…"* (in `resuming`). A nearby tooltip explains content-anchoring.
- Notes modal (from **Send**): grouped by file, edit/delete, stage **picker**
  (`flow_paused_stages`); empty → disclosed, **Send** disabled. `stale_content` 409
  → reload+re-anchor; `rev_conflict` → notes changed elsewhere.
- `use-status.ts` maps `flow_pause_state`/`flow_paused_stages`; `run-client.ts` gains
  the seven calls.

## Edge cases

- **No live pausable stage** — review mode engages (marker + banner);
  `flow_paused_stages` empty → Send disabled; `shouldExit` held by the marker.
- **Script / racing / queued agent writes during review** — allowed; the
  always-present `orig_line_text` + injection diagnostic let the agent reconcile.
- **Target left `paused` before inject** — rejected; no feedback written.
- **Crash mid-lifecycle** — resume transaction + atomic `SaveFeedbackOnce` +
  idempotent deletes + the recovery matrix converge.
- **User-manually-paused stage** — not `IsActive`-owned by review; untouched.

---

## Testing

**`pkg/state`** — notes roundtrip (`next_id` monotonic across delete, `rev` bump,
`line:null`, replacement keeps id/created_at & refreshes anchor); corrupt/version
quarantine; **`SaveFeedbackOnce` atomicity** (short/failed temp write; failure
before rename and after rename/before dir-fsync → recovery yields the old complete
file or old+exactly-one-complete-block, never sentinel-only/partial); marker
write/read/clear; fsync; content-hash helper.

**`pkg/orchestrator`**
- **No overlap (finding 1):** inject/cancel immediately after Pause while the old
  runner still holds SIGINT completion → assert executor-call count and max
  same-stage concurrency == 1; repeat with a pre-pause callback queued behind a full
  semaphore (not owned → no same-stage double).
- **Truthful ownership:** candidate completing / entering `awaiting_user_input`
  before its `EvPause`, or only queued (not `IsActive`), is excluded from `owned`.
- **`resume_kind` (finding 2):** non-interactive planning-retry, planning-revision,
  implementation-retry, active implementation, autonomous-retry; interactive
  planning/review/implementation; non-target paused-from-`revising` keeps its prior
  feedback; a standalone `review` resume reaches `done` and runs the cascade. Assert
  the actual runner/prompt.
- **Txn guard (finding 3):** every external forward control during `resuming` → 409;
  a fast target completing before other owners resume does **not** finalize `Run`;
  final-marker-delete failure exposes a recoverable `resuming` and keeps the loop
  alive.
- **Activation hold:** newly-unblocked dependent stays `pending`; bootstrap
  active-status respawns suppressed for owners on valid-marker recovery; a script
  finishing during review doesn't start its successor.
- **Anchor:** digest matches yet file mutates before the target reads feedback →
  feedback still carries `orig_line_text`; backtick lines survive JSON-quoting;
  deleted file → `unavailable` render, batch not aborted.
- **Recovery matrix:** crash after feedback append / after `state=resuming` before
  target transition / after target transition before spawn / after some non-target
  resumes / after note delete before marker delete → repeated recovery → one
  feedback block, no owner left paused by the op; each owner status (paused/active/
  awaiting/hook_failed/done/failed/invalid) handled per the matrix; corrupt
  `resuming` fails closed.
- **Finalization:** `PauseFlow` and finalize atomically exclusive; pause after `Run`
  returns is a no-op; `shouldExit` held by the marker through `resuming`.
- **Direct-IO:** initial-marker failure rolls back the hold and surfaces paused IDs;
  feedback failure retains marker+notes; marker-clear failure recovers without a
  second spawn.
- Control policy → 409 `flow_paused`. FSM: `EvRevise` From includes `paused`.

**`pkg/server`** — CRUD gating + `stale_content`/`rev_conflict`/`no_notes`/
`flow_not_paused`/`run_finalizing` codes; server-side `{root,path}` resolution +
`content_sha` recompute; `line:null` validation; unavailable-file render path;
`/api/status` fields; reference round-trips odd filenames.

**Frontend** — line comment + `●`; client `content_sha`; pause confirm; banner per
state + honest wording; notes edit/delete; picker inject; `stale_content`/
`rev_conflict` handling; `use-status`.

**Live verification** (`verify` skill, Docker + real browser): click a line → pause
gate → banner + a real **non-interactive** agent stage stops → notes across two
files → inject into that stage and confirm it restarts as **implementation** (not
planning), reading the notes once, with **no** duplicate agent for that stage →
others resume → banner clears; reload mid-review (survives); mutate a file during
review and see `orig_line_text` + the `changed` diagnostic; delete a reviewed file
and see the `unavailable` render without aborting other notes.

## Files touched (anticipated)

- `pkg/state/state.go` — content-anchored notes IO, resume-transaction marker IO,
  **atomic** `SaveFeedbackOnce`, content-hash helper.
- `pkg/orchestrator/` — `activationHeld`, `reviewTxnActive` (marker), `flowPauseMu`,
  `finalizing` guard, `runnerKind` registry (set at all main spawn sites), marker
  transaction, `PauseFlow` (IsActive-own + EvPause-first + `resume_kind`), async
  drain-then-resume dispatcher (target `*WithFeedback` / non-target plain-or-
  `*WithFeedback` by `from_revising`, review→`completeStage`), `Inject`/`Cancel`/
  CRUD, activation hold at all activation + active-status-recovery sites, control
  policy on `reviewTxnActive`, `shouldExit`/finalization on the marker, drift/
  unavailable rendering, per-status recovery + corrupt-`resuming` fail-closed, IO
  error rules.
- `pkg/orchestrator/retry.go` — attempt-0 replay context (A′).
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` From += `paused`.
- `pkg/executor`/runner_factory — thread the attempt-0 resume context.
- `pkg/server/` — `/api/flow/*`, `statusResponse` fields, `FlowActions` wiring, note
  resolution/validation + `content_sha`, error codes.
- `pkg/web/dashboard/src/` — FileViewer line comments + client hashing, pause confirm,
  banner, notes modal + picker, `run-client`, `use-status`, CSS.
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.

## Acceptance criteria (mechanically testable in the plan)

1. Resuming an owner never overlaps its pre-pause active or queued callback
   (own-only-`IsActive` + drain-before-resume); same-stage executor concurrency ≤ 1.
2. `resume_kind` is correct for planning/implementation/review/autonomous incl.
   retrying & revising; both target and non-target dispatch consume it; a standalone
   review resume completes via `completeStage`.
3. Public controls, `shouldExit`, and finalization stay guarded until the `resuming`
   marker is durably deleted (`reviewTxnActive`, not the activation hold).
4. `SaveFeedbackOnce` persists the sentinel and complete block atomically; never
   sentinel-only or partial.
5. Recovery is deterministic for every owner FSM status and never confuses a durable
   status with a live process; corrupt `resuming` fails closed; partial pause
   creation surfaces the paused IDs.
6. Every line note always carries `orig_line_text`; deleted/unreadable files yield
   actionable feedback, not an aborted injection.
7. Pause acceptance and run finalization are atomically exclusive.
