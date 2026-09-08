# File-browser review notes with flow-wide pause — design

Date: 2026-09-08
Status: approved (brainstorming), pending spec review → implementation plan
Branch: `file-notes`

## Problem

The Docker file browser lets a user browse the project and view/diff files
mid-run, but it is read-only for feedback: there is no way to click a line and
attach a note the way `PlanPanel`/`DialogChannel` already allow for plan lines
and dialog questions. A reviewer who spots issues across several files while the
flow is running has no structured way to collect those observations and steer a
stage with them.

Two hard constraints make this non-trivial:

1. **Files must not change while notes are being written.** If stages keep
   mutating files, line numbers and content drift and the notes become
   meaningless. So writing notes must pause the flow first.
2. **Pause today is strictly per-stage — there is no flow-wide pause.** We must
   introduce a global "flow paused" concept, reusing the existing per-stage
   pause for the actively-running stages.

## Goal

In the file browser, click a line → write a note. The first note prompts a
yes/no gate: to write notes the flow must be paused. On "yes", every non-script
agent stage is gracefully paused and a full-width "flow paused" banner appears.
The user accumulates notes across any number of files (each note = path + line +
text), stored durably in one file. When done, the user picks one stage to
receive all the notes as feedback (reusing the existing feedback mechanism); the
chosen stage restarts with the notes, every other frozen stage resumes plainly
via `--resume`, the pause lifts, and the flow continues.

## Decisions (locked during brainstorming)

- **Injection semantics:** resume-with-feedback, like `Revise`. The chosen stage
  restarts immediately reading the notes as `feedback.md` (the `*WithFeedback`
  machinery). Works for a stage that was active (paused-from-running/planning/
  revising/retrying).
- **Other parallel stages resume WITHOUT notes, via `--resume`.** If several
  stages were frozen, only the chosen one restarts with notes; the rest continue
  their existing agent session where they left off (session `--resume`, NOT a
  fresh restart — they must not redo completed work).
- **Durable:** pause state and accumulated notes are written to disk (marker file
  + notes file under `.afm/runs/<run_id>/`), matching the `prenote.md` /
  `autonomous.flag` idiom. A page reload or afm restart keeps the flow paused and
  the notes intact.
- **Global pause = Approach A (marker file + reuse).** A durable marker plus one
  global check in `concurrency.shouldRun`; active non-script stages paused with
  the existing durable `EvPause`; injection through the existing `Revise` /
  `*WithFeedback` chain. Smallest new surface; no flow-level FSM event.

## Non-goals (YAGNI)

- No multi-stage injection — a single chosen target.
- No new flow-scoped FSM event class; the FSM stays per-stage.
- No pausing/interrupting running **script** stages (architecturally
  unsupported — `RunScript` takes no interrupt channel). They finish; their
  successors are blocked by the global gate.
- No editing of a note's anchor (path/line) after creation — only text
  edit/delete. Re-anchoring means delete + re-add.

## Architecture

### Storage (durable, `.afm/runs/<run_id>/`)

Both files live in the `state` package as direct file IO (temp+rename atomic
writes), like `SavePreNote`/`LoadPreNote`. The orchestrator only *reads* notes,
at injection time.

**`review-notes.json`** — a single whole-file-rewritten document (volume is tiny,
human-typed; whole-file rewrite is the simplest thing that works — "prefer
simplicity"):

```json
{
  "version": 1,
  "notes": [
    {
      "id": "n1",
      "root": "project",
      "path": "pkg/foo/bar.go",
      "display_path": "project/pkg/foo/bar.go",
      "abs": "/work/pkg/foo/bar.go",
      "line": 42,
      "text": "...",
      "created_at": "2026-09-08T..."
    }
  ]
}
```

- `id` — server-assigned, stable for edit/delete (`n1`, `n2`, … monotonically
  from the current max, never reused within a run).
- `line: 0` (or omitted) — a file-level note with no specific line.
- `abs` — the container-absolute path, captured at add time from the file
  browser, used to build the `[AFM file: "<abs>"]` marker at injection. Absolute
  (not project-relative) so it resolves regardless of `flow.root_dir`, matching
  the existing marker convention.

State API: `SaveReviewNotes(runDir, doc)`, `LoadReviewNotes(runDir) (doc, error)`
(missing file → empty doc, not an error).

**`notes-pause.flag`** — presence = "flow is paused for notes". Content records
exactly what this pause froze, so resume only touches what it froze (a stage the
user had *manually* paused before must stay paused):

```json
{ "paused_stages": ["stage-a", "stage-b"], "created_at": "2026-09-08T..." }
```

State API: `WriteNotesPauseMarker(runDir, marker)`,
`ReadNotesPauseMarker(runDir) (marker, found, error)`,
`ClearNotesPauseMarker(runDir)`.

### Orchestrator — flow-pause lifecycle

New field `flowPaused atomic.Bool`, seeded from the marker on `orchestrator.New`
/ start (durability for free — a restart while paused re-enters the paused
state).

**Single control point — the concurrency gate.** `concurrency.Manager.shouldRun`
today is `Store.Get(id) != StatusPaused`. It becomes:

```go
func(id string) bool { return !o.flowPaused.Load() && o.opts.Store.Get(id) != state.StatusPaused }
```

While `flowPaused` is set, **no** new agent launch/activation happens for any
stage (planning/implementation/review/autonomous, fresh or resume) — every path
already funnels through `SpawnAgent`. This is the one place that freezes forward
progress; idle stages (`awaiting_approval`, `awaiting_user_input`, `ready`,
`pending`) need no explicit `EvPause` — the gate stops them advancing.

New flow-scoped actions (a `FlowActions` interface on the orchestrator, wired
into the server like `PrimaryActions`):

- **`PauseFlow(ctx) (pausedStageIDs []string, err error)`**
  1. Compute *pausable* stages: status ∈ {running, planning, revising, retrying},
     **not** a script stage, **not** already user-paused.
  2. Write `notes-pause.flag` with the pausable set and set `flowPaused=true`
     — durable, **before** any signaling, so a crash mid-pause recovers as paused.
  3. For each pausable stage, run the existing per-stage pause internals
     (`Trigger(EvPause)` + `bumpPauseGen` + signal `interruptChans`).
  4. Running **script** stages are left running. Their successors stay blocked by
     the gate.
  5. Return the set. The UI polls `/api/status` until every returned id is
     actually `paused` → "fully paused, you may type."

- **Note CRUD** — not in the orchestrator. Pure file IO in the HTTP handlers via
  `state.SaveReviewNotes`, like `prenote`. Write operations are gated on
  `flow_paused` (see API).

- **`InjectNotesAndResume(ctx, targetStageID) error`**
  1. Load notes → render feedback text (§ Injection rendering) →
     `state.SaveFeedback(targetStageDir, rendered)`.
  2. Clear `flowPaused=false` + `ClearNotesPauseMarker` **first**, so the resume
     spawns below are not blocked by the gate.
  3. **Resume the target WITH feedback.** One small FSM extension: add `paused` to
     `EvRevise`'s `From` set. Injection = `Trigger(target, EvRevise)`
     (paused→revising) + `SpawnAgent(run<phase>WithFeedback)`, dispatched by the
     stage's `PausedFrom` exactly like `resumeStageAtStatus`'s existing `revising`
     branch (`detectInterruptedPhase`). The `*WithFeedback` runner reads
     `feedback.md` and splices it + the `[AFM file:...]` markers. No double-spawn.
  4. **Resume every other frozen stage WITHOUT notes**, via the existing
     `Continue` path (`EvContinue` → `PausedFrom` → `resumeStageAtStatus`), which
     continues the stage's existing agent session with `--resume` (must NOT
     restart fresh / redo work). Iterate the recorded `paused_stages` minus the
     target.
  5. Re-drive scheduling (`startReadyStages` / `tryActivatePrePlanned` /
     `startPlanningForUnblocked`) so stages that were blocked purely by the gate
     (never `EvPause`d) now activate.
  6. Delete `review-notes.json`.

- **`CancelNotesAndResume(ctx) error`** — discard `review-notes.json` + marker,
  clear the flag, plain-`Continue` all frozen stages (via `--resume`), re-drive
  scheduling. No stage receives feedback.

**Recovery.** On start, `ReadNotesPauseMarker` returns found → `flowPaused=true`
(gate active). The frozen stages are already durably `paused` in the event log;
`review-notes.json` persists. Nothing auto-resumes — the dashboard shows the
banner and the notes; the user finishes and injects/cancels as before.

### `--resume` requirement (explicit)

The non-target frozen stages MUST resume their existing session, not restart
from scratch. `resumeStageAtStatus` already dispatches paused-from-running to a
resume that continues the session — the spec pins this as a hard requirement with
a dedicated regression test (a paused-then-resumed non-target stage keeps its
session id / does not re-execute completed steps). If any resume path currently
restarts fresh for a paused stage, that path is corrected to `--resume`.

### HTTP API (flow-scoped, new `/api/flow/*`)

- `POST /api/flow/pause` → `{ "paused_stages": ["..."] }`. Idempotent-ish: if
  already `flow_paused`, returns the current set.
- `GET  /api/flow/notes` → `{ "notes": [...] }`.
- `POST /api/flow/notes` (`{root, path, line, text}`) → the created note with its
  `id`. **Gated on `flow_paused`** (400 otherwise) — notes require a paused flow.
- `PUT  /api/flow/notes/{id}` (`{text}`) → edit text. Gated on `flow_paused`.
- `DELETE /api/flow/notes/{id}` → delete. Gated on `flow_paused`.
- `POST /api/flow/notes/inject` (`{stage_id}`) → `InjectNotesAndResume`. Validates
  `stage_id` ∈ the current frozen set (else 400).
- `POST /api/flow/notes/cancel` → `CancelNotesAndResume`.
- `/api/status` gains `flow_paused bool` (feeds the banner) and
  `flow_paused_stages []string` (feeds the stage picker). Both derived from the
  marker / `flowPaused` + the frozen set.

`abs`/`display_path` for a POSTed note are resolved server-side from
`{root, path}` through the same `workspace` resolution the file browser uses —
the client sends only `root` + relative `path` + `line` + `text`; the server
never trusts a client-supplied absolute path (consistent with the file browser's
security boundary).

### Frontend (`pkg/web/dashboard/src`)

- **FileViewer line-comment UX.** Reuse the `PlanPanel` pattern: `.plan-line` +
  `line-comment-*` classes, a `comments: Record<number,string>`-style local
  draft, a `PasteableTextarea` editor, and a `●` marker on lines that already
  have a saved note. Clicking a line opens the editor for that line.
- **Pause gate.** The first note attempt while `!flow_paused` opens a confirm
  dialog: *"Чтобы писать заметки, флоу нужно поставить на паузу. Поставить на
  паузу?"*
  - **Yes** → `POST /api/flow/pause`, show a "pausing…" state until `flow_paused`
    && every `flow_paused_stages` id reports `paused`, then enable the editor →
    `POST /api/flow/notes`.
  - **No** → close the editor; the flow is untouched.
- **Full-width banner.** Rendered in `App.tsx` immediately after `<FlowHeader/>`,
  gated by `flow_paused`: *"⏸ Flow paused — review notes (N)"* plus **Send
  notes** and **Cancel** actions. If any script stage is still running, the banner
  appends *"(N script stage(s) still running)"*.
- **Notes review modal** (opened from the banner **Send notes**): all notes
  grouped by file (with `display_path`), per-note edit/delete, and a **stage
  picker** — a dropdown of `flow_paused_stages`. **Inject** → `POST
  /api/flow/notes/inject` → resume. **Cancel** in the banner → `POST
  /api/flow/notes/cancel`.
- `use-status.ts` maps `flow_paused` → `flowPaused` and `flow_paused_stages` →
  `flowPausedStages`. Client (`run-client.ts`): `pauseFlow`, `listNotes`,
  `addNote`, `updateNote`, `deleteNote`, `injectNotes`, `cancelNotes`.

### Injection rendering (into `feedback.md`)

Notes grouped by file, each file titled with the marker the agent resolves with
its own read tool; file-level notes (`line: 0`) rendered as `File:`:

```
## Review notes

### [AFM file: "/work/pkg/foo/bar.go"] (project/pkg/foo/bar.go)
- Line 42: <text>
- Line 88: <text>

### [AFM file: "/work/other.go"] (project/other.go)
- File: <text>
```

## Edge cases

- **No pausable agent stage** (everything is script or idle). Pause still engages
  the global gate and shows the banner, but `flow_paused_stages` is empty →
  **Inject** has nowhere to land (variant-1 injection needs a paused-from-active
  target). Handling: the stage picker is empty and **Send** is disabled; the user
  can still **Cancel** to resume. This is the single known limitation of variant-1
  injection and is called out to the user in the UI ("no active stage to receive
  notes"). *(Open thread: a future enhancement could fall back to a `prenote` on
  a chosen `pending` stage; out of scope here.)*
- **Script stage mutating files during note-writing** — an accepted gap (scripts
  are deliberately excluded from pause). The banner surfaces "N script stage(s)
  still running" so the reviewer knows line numbers may still drift.
- **A stage the user manually paused before entering flow-pause** — not in
  `paused_stages`, so resume/cancel leaves it paused (correct — it was
  intentional).
- **`inject` / `cancel` while not `flow_paused`** → 400 (no-op guard).
- **`flock` / active run** — flow-pause actions go through the orchestrator like
  `Pause`/`Continue`, under the run-scoped context, so they compose with the
  existing `ErrRunLocked` semantics.

## Testing

**Go — `pkg/state`:** `SaveReviewNotes`/`LoadReviewNotes` roundtrip (add/edit/
delete, id monotonicity, missing-file → empty); marker write/read/clear.

**Go — `pkg/orchestrator`:**
- `PauseFlow` freezes exactly the pausable set (running/planning/revising/
  retrying, non-script, not-user-paused), writes the marker, sets the flag; the
  gate then blocks a would-be new launch.
- A running **script** stage is left running and its successor stays blocked.
- Recovery: start with a marker present → `flowPaused=true`, frozen stages stay
  `paused`, notes intact.
- `InjectNotesAndResume`: writes rendered `feedback.md` to the target,
  `EvRevise`-from-`paused` resumes it via the correct `*WithFeedback` runner,
  every other frozen stage resumes plainly, marker + notes file gone, blocked
  pending stages activate.
- **`--resume` regression:** a non-target frozen stage resumes its existing
  session (does not restart fresh / redo completed work).
- `CancelNotesAndResume`: discards notes + marker, resumes all frozen stages, no
  feedback written.
- FSM: `EvRevise` `From` includes `paused`.

**Go — `pkg/server`:** notes CRUD gating (rejected when not `flow_paused`);
`inject` target validation; `/api/status` carries `flow_paused` +
`flow_paused_stages`; server-side `abs`/`display_path` resolution from
`{root,path}`.

**Frontend:** FileViewer line comment add/`●`; pause confirm gate (yes → pause →
enabled; no → closed); banner visibility on `flow_paused` + note count + script
warning; notes modal edit/delete; stage picker inject; `use-status` mapping.

**Live verification** (project norm — `verify` skill, Docker + real browser):
click a file line, hit the pause gate, confirm the banner + graceful pause of a
real agent stage, write notes across two files, inject into one stage and observe
its `feedback.md` receiving the rendered notes + the other stage resuming via
`--resume`, then the banner clearing and the flow continuing.

## Files touched (anticipated)

- `pkg/state/state.go` — notes + marker IO.
- `pkg/orchestrator/` — `flowPaused` field, `PauseFlow`/`InjectNotesAndResume`/
  `CancelNotesAndResume` (a new `flow_notes.go` or in `control_api.go`), the
  `shouldRun` gate extension, recovery seed, scheduling re-drive.
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` `From` += `paused`.
- `pkg/orchestrator/concurrency/concurrency.go` — the closure is already the seam;
  the predicate is passed from `orchestrator.New`.
- `pkg/server/` — `/api/flow/*` routes + handlers, `statusResponse` fields, the
  `FlowActions` interface wiring, server-side note resolution.
- `pkg/web/dashboard/src/` — FileViewer line comments, pause confirm dialog,
  full-width banner in `App.tsx`, notes review modal + stage picker, `run-client`,
  `use-status`, CSS (line-comment reuse + banner, tokenized per skin).
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.
