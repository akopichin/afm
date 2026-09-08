# File-browser review notes with flow-wide pause — design (v2)

Date: 2026-09-08
Status: revised after design review (`2026-09-08-file-browser-review-notes-design-review.md`),
pending final user review → implementation plan.
Branch: `file-notes`

> v2 supersedes the first draft. The review found that a boolean-on-`SpawnAgent`
> is not enough to guarantee the core invariant (no AFM process mutates project
> files while the user writes line-anchored notes). v2 reframes the marker as a
> **small durable transaction** with an explicit lifecycle, coordinated with
> scheduling and process drain. All six blocking findings and the additional gaps
> are addressed below, verified against the actual code.

## Problem

The Docker file browser can browse/view/diff files mid-run but has no way to
click a line and attach a note (as `PlanPanel`/`DialogChannel` do for plan lines
and dialog questions). A reviewer who spots issues across several files has no
structured way to collect them and steer a stage.

Two hard constraints:

1. **Files must be stable while notes are written.** Line-anchored notes are
   meaningless if files keep changing under them. So note-writing must pause the
   flow first, and — critically — must not begin until every AFM-controlled
   writer has actually stopped (**quiescence**), not merely when statuses read
   `paused`.
2. **Pause is strictly per-stage today; there is no flow-wide pause.** We add a
   flow-wide concept as a durable transaction that reuses per-stage pause for the
   active agent stages.

## Goal

In the file browser, click a line → write a note. The first note prompts a
yes/no gate: to write notes the flow must be paused. On "yes" the flow enters a
durable review-pause: all active non-script agent stages are gracefully paused,
in-flight scripts/hooks are allowed to finish, and note editing unlocks **only
once the flow is fully quiescent**. A full-width "flow paused" banner is shown
throughout. The user accumulates notes across any number of files (path + line +
text), stored durably. When done, the user picks one stage to receive all notes
as feedback (reusing the feedback mechanism); the chosen stage restarts with the
notes, every other frozen stage resumes via the normal Continue path, the pause
lifts, and the flow continues.

## Locked decisions

- **Approach A — durable marker transaction + reuse.** No flow-level FSM event
  class; per-stage pause, `Revise`/`*WithFeedback`, and marker+direct-IO idioms
  are reused.
- **Injection = resume-with-feedback (Revise-style).** The chosen (owned, active)
  stage restarts reading the notes as `feedback.md`.
- **Other frozen stages resume via the normal `Continue` path (decision A).**
  Interactive stages get a real `--resume`; **non-interactive stages re-spawn
  with prior tool-actions replayed as prompt context** (`buildRetryContext`) —
  this is exactly what `Continue`/`resumeStageAtStatus` already do. We do **not**
  add session support to non-interactive runners. Work is not lost (on-disk
  artifacts + replayed context), though a non-target non-interactive agent may
  redo some steps. This is stated honestly in the UI.
- **Strong file-stability contract (decision Strong).** Scripts/hooks are never
  interrupted (architecturally impossible), but note editing stays disabled until
  **all** AFM processes (agents, script stages, `script_before`/`script_after`
  hooks, reflection agents) have stopped. The banner shows what pausing is
  waiting on.
- **Durable.** The transaction and notes survive reload and afm restart; recovery
  *finishes* an interrupted pause/resume rather than re-entering edit mode.

## Non-goals (YAGNI)

- Multi-stage injection (single chosen target).
- No new flow-scoped FSM event; the FSM stays per-stage.
- No pausing/interrupting scripts or hooks.
- No session-resume support for non-interactive stages.
- No re-anchoring a note's path/line after creation (text edit / delete only).

---

## Architecture

### The review-pause transaction (durable, `.afm/runs/<run_id>/`)

One marker file **`notes-pause.json`** is the transaction record. It is NOT a
final snapshot — recovery must be able to answer, without guessing: was pause
requested but not fully applied? which pauses are *ours* vs the user's? has every
writer stopped? which phase/session does each stage resume? was feedback already
delivered? was resume partially applied?

```json
{
  "version": 1,
  "operation_id": "review-<lastSeqAtPause>",
  "state": "pausing",                 // pausing | paused | resuming
  "created_at": "2026-09-08T...",
  "target_stage": "",                 // set only during resuming (inject); "" for cancel
  "feedback_delivered": false,        // idempotency for inject
  "notes_deleted": false,             // idempotency for the terminal cleanup
  "stages": [
    {
      "id": "stage-a",
      "owned": true,                  // this operation paused it (not the user)
      "paused_from": "running",       // FSM status it left
      "phase": "implementation",      // exact executing phase (see Phase persistence)
      "pause_applied": true,          // EvPause CAS succeeded
      "resume_applied": false         // resume transition + spawn done
    }
  ]
}
```

`operation_id = "review-" + strconv.Itoa(store.LastSeq())` at pause time — unique
per run (only one review-pause at a time). Written in Go, so `time.Now` is
available for `created_at`.

`state = none` is represented by the marker's **absence**.

**Notes file `review-notes.json`** — a single whole-file-rewritten document
(volume is tiny, human-typed):

```json
{
  "version": 1,
  "rev": 7,                    // bumped on every write; ETag for multi-tab 409s
  "next_id": 8,                // persisted; IDs never reused even after delete
  "notes": [
    {
      "id": "n7",
      "root": "project",
      "path": "pkg/foo/bar.go",
      "display_path": "project/pkg/foo/bar.go",
      "reference": "[AFM file: \"/work/pkg/foo/bar.go\"]",  // from the workspace marker builder, NOT fmt
      "line": 42,                 // 0 = file-level note
      "orig_line_text": "\tif err != nil {",               // snapshot at add time (review UX + resume context)
      "content_etag": "\"a1b2...\"",                        // file ETag the user saw
      "text": "...",
      "created_at": "2026-09-08T..."
    }
  ]
}
```

Both files: atomic temp+rename with a **unique** temp name, **fsync file + parent
dir** (the feature promises durability on par with the event log — atomic rename
alone is torn-read safety, not crash durability). On a corrupt/parse failure the
loader **quarantines** to `<name>.corrupt-<ts>` and surfaces an error; it never
overwrites evidence. An unknown `version` is refused, not silently dropped.

State-package API (direct IO, like `SavePreNote`): `SaveReviewNotes` /
`LoadReviewNotes` / `DeleteReviewNotes`; `WriteNotesPauseMarker` /
`ReadNotesPauseMarker` (returns marker, found, error) / `ClearNotesPauseMarker`.

### The coordinator (one serialized owner)

A single mutex `o.flowPauseMu` on the orchestrator serializes **all** review-pause
lifecycle operations: `PauseFlow`, note CRUD (add/edit/delete), `InjectNotes`,
`CancelNotes`, and recovery. This is the single point the review requires; no
lifecycle step races another. Read-only note listing (`GET`) reads the file
lock-free.

Because CRUD must be serialized with the lifecycle **and** enforced against the
transaction state server-side, note writes go **through the orchestrator**
(`FlowActions.AddNote/UpdateNote/DeleteNote`), not direct-to-file from the
handler. Each takes `flowPauseMu`, asserts `state == paused` (quiescent), does the
IO, bumps `rev`.

### Flow gate — checked before FSM activation, not at `SpawnAgent`

**Verified:** every activation path commits the FSM transition (`EvStartRun`,
`EvStartPlanning`) *before* `SpawnAgent` (`scheduling.go:136,157`,
`recovery.go:159`). Today no stage strands because the *only* cause of a false
`shouldRun` is a real `EvPause` (so the stage is `paused`). Putting `flowPaused`
into `shouldRun` (the v1 mistake) would have introduced the orphan the review
describes: flipped to `running`, then dropped, stuck with no process and no
`EvPause`.

**Fix:** the flow gate lives at the **activation sites**, before the transition.
A helper `o.flowActivationBlocked(stageID) bool` returns true while the marker
`state ∈ {pausing, paused}`. It is consulted at the top of `startReadyStages`
(before `EvStartRun`), `tryActivatePrePlanned`, and
`startPlanningForUnblocked`/`startPlanningForPending` (before `EvStartPlanning`).
When blocked, the stage is simply left in `pending`/`ready` — a clean, recoverable
state re-driven when the gate opens. `concurrency.shouldRun` is **left exactly as
is** (paused-check only), so it keeps serving as the last-line queued-behind-
semaphore defence **and** the `script_after`/reflection paths (which `SpawnAgent`
directly, post-completion, never through activation) are untouched — closing the
`pendingAfterHooks` leak concern by construction.

### Quiescence — the real "you may type" boundary

Statuses go to `paused` synchronously, before the subprocess actually dies (the
15 s graceful-interrupt window; a late `ErrUserInterrupted`). So "all statuses
`paused`" is **not** proof of quiescence and is **not** a correctness boundary.

`state: pausing → paused` is committed (durably) only when the flow is
**quiescent**, evaluated on the orchestrator event loop whenever an agent/hook
completes (they already `WakeEventLoop`):

```
quiescent  ⇔  no stage is IsActive
          AND o.pendingAfterHooks.Load() == 0
          AND o.pendingReflections.Load() == 0
```

During `pausing` nothing new starts (activation gated), so the only active things
are drainers: the interrupted agents finishing their grace window, any running
script stage finishing, any in-flight `script_before`/`script_after`. Each
completing stage runs its single `script_after` (bounded) and then its successors
are gated — so writers strictly decrease to zero. When the event loop sees
quiescence it flips the marker to `paused` and note editing unlocks. `IsActive`
covers script stages and hooks (all go through `SpawnAgent`/`markActive`).

**This also closes the inject double-launch race.** Inject is only accepted in
`state == paused` (quiescent) — by then every interrupted process has already
returned `ErrUserInterrupted` and been correctly handled as a Pause
(`runWithRetry` saw `currentStatus == paused`). There is no in-flight process
left to misread a later `revising` flip. No interrupt-token plumbing through the
executor is required.

### Central flow-pause policy for existing controls

Two distinct windows must not be conflated:

- **Activation gate** (the auto-scheduler starting stages) is closed for
  `state ∈ {pausing, paused}` and **opens at `resuming`** — the resume step needs
  the cascade to spawn gate-blocked pending stages.
- **Control policy** (user/HTTP-initiated actions) blocks the whole
  `state ∈ {pausing, paused, resuming}` window, so a user action can't race the
  in-progress resume.

While the control window is open, every forward-driving control action is
rejected server-side with `ErrFlowPaused` (409): `Continue`, `Approve`
(+ headless auto-approve), `Retry`, `Revise`, `Button`, dialog `NotifyAnswer`,
non-interactive **auto-answer** (the poller suppresses auto-answering while
paused), and manual `Pause`. The review-pause lifecycle does its own resuming via
**private** helpers (`SpawnAgent(run<phase>WithFeedback)` and the internal
`resumeStageAtStatus` path) that bypass these public guards — so the guards can
block *all* external callers unconditionally. Hiding the buttons in one browser
tab is not sufficient — a second tab or an internal poller can still race, so the
gate is in the orchestrator, not the UI.

### `PauseFlow` — crash/race-consistent ordering

Under `flowPauseMu`:

1. **Write the marker `state=pausing` FIRST** (empty `stages`), then persist.
   This closes the activation gate *before* any snapshot, so no new agent can
   start into the frozen set (closes "new agent between snapshot and flag").
2. **Snapshot pausable stages** now (gate already closed → the set is stable):
   status ∈ {running, planning, revising, retrying}, **not** a script stage,
   **not** already `paused` (a user-paused stage is not ours). Record each as an
   `owned` marker entry with `paused_from` and `phase` (from the phase map);
   persist.
3. **Apply `EvPause`** to each owned stage (reusing per-stage `Pause` internals:
   `EvPause` + `bumpPauseGen` + signal `interruptChans`). If a stage completed
   between snapshot and here, its CAS loses → mark that entry
   `pause_applied=false, owned=false` (it is `done`, not ours) so resume never
   touches it. A `StorageError` mid-loop terminates the run as usual; the marker's
   per-stage `pause_applied` lets recovery reconcile.
4. Leave `state=pausing`. The event loop transitions to `paused` on quiescence.
5. Return the owned set + current blockers to the UI (which polls `/api/status`).

Running **script** stages are never `EvPause`d; they run to completion and are
waited for by quiescence; their successors are gated.

### `InjectNotesAndResume(targetStageID)` — idempotent, recovery-safe

Precondition (server-enforced): `state == paused` (quiescent) and `targetStageID`
is an owned, `pause_applied` entry (else 400). Under `flowPauseMu`:

1. Set `state=resuming`, `target_stage=targetStageID`; persist. (The gate now
   **opens** so the resume + cascade can spawn.)
2. If `feedback_delivered == false`: render notes → `feedback.md` via
   `state.SaveFeedback` (append-only), then set `feedback_delivered=true` and
   persist **before** spawning. On retry/recovery this guarantees the notes are
   never appended twice.
3. **Resume the target with feedback.** FSM: add `paused` to `EvRevise`'s `From`
   set. Then `EvRevise` (paused→revising) + `SpawnAgent(run<phase>WithFeedback)`,
   dispatched by the marker's persisted `phase` (NOT `detectInterruptedPhase`).
   Mark the target `resume_applied=true`; persist.
4. **Resume every other owned, `pause_applied`, not-yet-`resume_applied` stage**
   via the normal `Continue` path (`EvContinue` → `paused_from` →
   `resumeStageAtStatus`), which uses `--resume` for interactive and
   replay-context re-spawn for non-interactive (decision A). Dispatch uses the
   marker `phase` for determinism. Mark each `resume_applied=true` as it goes.
5. Re-drive scheduling (`startReadyStages`/`tryActivatePrePlanned`/
   `startPlanningForUnblocked`) so gate-blocked pending stages now activate.
6. `DeleteReviewNotes` (set `notes_deleted=true` first for idempotency), then
   `ClearNotesPauseMarker`. Marker cleared **last**, after every transition +
   spawn is reconciled.

`CancelNotesAndResume()` is the same with no target and no feedback: `state=
resuming`, `target_stage=""`, resume all owned stages plainly, delete notes, clear
marker.

### Recovery matrix (finish, don't restart edit mode)

On `orchestrator` start, `ReadNotesPauseMarker`:

- **absent** → normal operation.
- **`pausing`** → re-enter pausing: the gate is closed; owned `pause_applied`
  stages are already durably `paused` in the log; any process that was draining
  died with the process, so the flow is (re-)evaluated for quiescence and typically
  transitions to `paused` immediately. Editing stays locked until quiescent. Any
  owned entry with `pause_applied=false` is reconciled against the log (if it's
  `done`, drop ownership; if still pausable, re-apply `EvPause`).
- **`paused`** → show banner; editing enabled once quiescence re-confirmed.
- **`resuming`** → **finish the resume**, not edit mode: honor `feedback_delivered`
  (skip re-append), resume any owned stage without `resume_applied`, honor
  `notes_deleted`, then clear the marker. Idempotent by construction.

`shouldExit` gains a guard: **false while any review-pause marker exists**
(`state != none`), so the orchestrator/server stays alive for the reviewer even
if the last running script completed during pause and all stages are terminal.

### Phase persistence (deterministic resume dispatch)

**Verified:** nothing persists the executing phase; `running` covers
implementation/review/autonomous, `detectInterruptedPhase` relies on session-file
mtimes (interactive only), and non-interactive falls back to
`runImplementationAgent`. That is not reliable enough to pick the right
`*WithFeedback` runner on inject.

Add `o.runningPhase sync.Map` (stageID → phase). Set it when an agent attempt
starts in `runPlanningAgent`/`runImplementationAgent`/`runReviewAgent`/
`runAutonomousAgent` (and their `*WithFeedback` variants); **keep it across retry
backoff** (so a `retrying` stage still reports its phase); clear it only when the
stage reaches a terminal/awaiting status. `PauseFlow` copies `runningPhase[id]`
into each owned marker entry. Resume dispatch reads the marker `phase` — exact,
no mtime guessing. The good sub-cases the review flagged already hold:
`runPlanningWithFeedback` tolerates a missing plan (`LatestPlanVersion` returns
`0,"",nil`), and a mid-planning pause with no plan resumes via the fresh
`runPlanningAgent`.

---

## HTTP API (`/api/flow/*`)

- `POST /api/flow/pause` → `{ paused_stages:[], blockers:[] }`. If a marker
  already exists, returns the current transaction (idempotent, no second pause).
- `GET  /api/flow/notes` → `{ rev, notes:[...] }` (lock-free read).
- `POST /api/flow/notes` `{ root, path, line, text, content_etag, expected_rev }`
  → created note. Rejected unless `state==paused` (409). Server resolves
  `abs`/`display_path`/`reference` from `{root,path}` via the `workspace` package
  (client never sends an absolute path); re-reads the file under quiescence and
  validates: regular readable file, `line` within current content, `content_etag`
  matches (else 409 stale), `expected_rev` matches `rev` (else 409 conflict),
  `text` non-blank and ≤ `maxNoteTextLen`, note count ≤ `maxNotes`. A note for an
  existing `(root, path, line)` **replaces** it (one note per line; matches the
  frontend `Record<number,string>` model). Snapshots `orig_line_text`.
- `PUT /api/flow/notes/{id}` `{ text, expected_rev }` / `DELETE
  /api/flow/notes/{id}?expected_rev=` → edit text / delete. `state==paused` +
  `expected_rev` gated.
- `POST /api/flow/notes/inject` `{ stage_id }` → `InjectNotesAndResume`
  (target validated ∈ owned). 409 if not `state==paused`.
- `POST /api/flow/notes/cancel` → `CancelNotesAndResume`. 409 if no marker.
- `/api/status` gains:
  - `flow_pause_state`: `none | pausing | paused | resuming`
  - `flow_quiescent`: bool
  - `flow_paused_stages`: `[]string` (owned; feeds the picker)
  - `flow_pause_blockers`: `[]string` (active stage ids + `"after-hooks"` /
    `"reflections"`, shown while `pausing`)

Marker read/parse errors at startup are surfaced (logged via the existing stderr
idiom) and treated as **fatal-to-the-feature-not-silent**: a corrupt marker is
quarantined and the review-pause is reported as needing attention, never silently
"absent" (which would resume frozen stages the user never released) nor silently
"paused" without an owned set.

## Frontend (`pkg/web/dashboard/src`)

- **FileViewer line-comment UX** — reuse the `PlanPanel` pattern (`.plan-line` +
  `line-comment-*`, a `PasteableTextarea`, a `●` marker on annotated lines).
- **Pause gate** — first note attempt while `flow_pause_state==none` opens a
  confirm: *"Чтобы писать заметки, флоу нужно поставить на паузу. Поставить?"*
  **Yes** → `POST /api/flow/pause`; the editor stays **disabled** and shows
  *"pausing — waiting: <blockers>"* until `flow_pause_state==paused &&
  flow_quiescent`; then, **reload the selected file** (defend the click-before-
  quiescence window), capture its ETag, and enable the editor. **No** → close the
  editor, flow untouched.
- **Full-width banner** (App.tsx, after `<FlowHeader/>`), by `flow_pause_state`:
  `pausing` → *"⏸ Pausing — waiting for N process(es)…"*; `paused` → *"⏸ Flow
  paused — review notes (N)"* + **Send notes** + **Cancel**; `resuming` →
  *"Resuming…"*.
- **Notes review modal** (from **Send notes**) — notes grouped by file
  (`display_path`), per-note edit/delete, a **stage picker** (`flow_paused_stages`).
  If the picker is empty (no owned active stage), it discloses *"no active stage
  to receive notes — you can Cancel"* and **Send** is disabled. **Inject** →
  `POST /api/flow/notes/inject`. **Cancel** (banner) → `.../cancel`.
- **Empty-target disclosure is upfront** — if `flow_paused_stages` is empty when
  the pause settles, the banner already says notes can't be injected, before the
  user writes them.
- **Multi-tab / conflict** — a 409 (`expected_rev`/`content_etag` mismatch)
  surfaces "notes changed elsewhere — reload".
- `use-status.ts` maps the new fields; `run-client.ts` gains `pauseFlow`,
  `listNotes`, `addNote`, `updateNote`, `deleteNote`, `injectNotes`,
  `cancelNotes`.

## Injection rendering (into `feedback.md`)

Deterministic ordering (files sorted by `display_path`, notes by `line`), each
file titled by its stored `reference` marker; file-level notes as `File:`:

```
## Review notes
### [AFM file: "/work/pkg/foo/bar.go"] (project/pkg/foo/bar.go)
- Line 42: <text>
- Line 88: <text>
### [AFM file: "/work/other.go"] (project/other.go)
- File: <text>
```

Total rendered size is capped (`maxRenderedFeedback`); overflow is reported to the
user before inject rather than silently truncated.

## Edge cases (explicit contracts)

- **No pausable agent stage** (all script/idle/terminal). PauseFlow still engages
  the gate + banner. `flow_paused_stages` empty → **Send** disabled, disclosed
  upfront; only **Cancel** resumes. `shouldExit` stays open due to the marker, so
  the server doesn't exit under the reviewer even if the last script finished and
  all stages are terminal.
- **Script/hook still running** — never interrupted; note editing waits for it
  (Strong contract); banner lists it as a blocker.
- **User-manually-paused stage** before the review pause — not owned, never
  resumed by inject/cancel.
- **Any forward-driving control during pause** — 409 `ErrFlowPaused` (central
  policy), from any tab or poller.
- **Crash at any durable step** — recovery matrix finishes pausing/resuming
  idempotently; feedback never double-appended (`feedback_delivered`); notes never
  resurrected after delete (`notes_deleted`).

---

## Testing

**`pkg/state`** — notes roundtrip (add/edit/delete, `next_id` monotonic across
deletes, `rev` bump, missing→empty); corrupt/unknown-version quarantine without
overwrite; marker write/read/clear; fsync durability path.

**`pkg/orchestrator`**
- **Activation gate:** pause racing `pending→planning`; pause racing
  `ready→running`; a running **script** completes during pause and unblocks a
  dependent — the dependent does NOT strand (stays `pending`, activates on
  resume); a launch already queued behind a full semaphore when pause begins.
- **Quiescence:** agent delays exit until the 15 s force-kill boundary → editing
  stays disabled until it drains; pause during `script_before` waits for the hook;
  a stage that writes its completion artifact just before stopping is handled.
- **Double-launch:** inject immediately after PauseFlow cannot double-spawn;
  cancel immediately after PauseFlow cannot double-spawn; a fast plain Continue of
  a non-target stage cannot double-spawn.
- **Ordering/consistency:** PauseFlow concurrent with PauseFlow / Inject / Cancel
  / manual Pause / Continue; a stage completing between snapshot and `EvPause`
  loses ownership; partial-pause storage failure reconciles on recovery.
- **Resume/idempotency:** inject writes rendered `feedback.md` once, `EvRevise`-
  from-`paused` resumes the target via the phase-correct `*WithFeedback` runner,
  others resume plainly, marker+notes gone, gate-blocked pending stages activate;
  a retried inject does not duplicate feedback; recovery from every boundary of
  `pausing` and `resuming`.
- **Phase:** inject into planning (no plan yet), implementation, review,
  autonomous, planning-retry and implementation-retry backoff — dispatch is
  phase-correct; the same for non-interactive stages (no session files).
- **`--resume` semantics (decision A):** interactive non-target resumes its
  session; non-interactive non-target re-spawns with replay context (not blank).
- **Lifecycle guards:** every forward-driving control returns `ErrFlowPaused`
  while paused; auto-answer suppressed while paused; `shouldExit` stays false
  while a marker exists (incl. last-script-completed / all-terminal).
- **Hook accounting:** `script_after` triggered by a stage completing during pause
  runs to completion and quiescence waits for it; `pendingAfterHooks` returns to
  zero (no leak).
- FSM: `EvRevise` `From` includes `paused`.

**`pkg/server`** — CRUD gating (rejected unless `state==paused`); `content_etag`
+ `expected_rev` 409s; inject target validation; server-side resolution of
`abs`/`display_path`/`reference` from `{root,path}` (client absolute path never
trusted); `/api/status` carries the four new fields; paths with quotes /
backslashes / newlines / non-ASCII round-trip through the stored `reference`
(marker builder, not `fmt`).

**Frontend** — line comment + `●`; pause confirm; editor disabled until
`paused && quiescent`, file reloaded on unlock; banner per state with blockers;
empty-target disclosure; notes modal edit/delete; picker inject; 409/reload path;
`use-status` mapping.

**Live verification** (`verify` skill, Docker + real browser): click a line, hit
the pause gate, watch `pausing` wait on a real draining agent (and a running
script if present), write notes across two files, inject into one stage and see
its `feedback.md` receive the rendered notes + the others resume, banner clears,
flow continues; reload mid-`paused` and confirm the pause + notes survive.

## Files touched (anticipated)

- `pkg/state/state.go` — notes + marker IO (durable, quarantine).
- `pkg/orchestrator/` — `flowPauseMu`, `runningPhase`, the marker transaction,
  `PauseFlow`/`InjectNotesAndResume`/`CancelNotesAndResume`/note CRUD, the
  activation gate (`scheduling.go`/`recovery.go`), quiescence evaluation on the
  event loop, the central control-action policy, `shouldExit` guard, recovery.
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` `From` += `paused`.
- `pkg/server/` — `/api/flow/*` routes + handlers, `statusResponse` fields, the
  `FlowActions` interface wiring, server-side note resolution + validation.
- `pkg/web/dashboard/src/` — FileViewer line comments, pause confirm, full-width
  banner, notes modal + picker, `run-client`, `use-status`, CSS (tokenized).
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.
