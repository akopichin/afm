# File-browser review notes with best-effort pause — design (v5, hybrid)

Date: 2026-09-08
Status: revised after four review rounds; hybrid model confirmed sound, v5 closes
the remaining bounded correctness gaps. Pending final user review → plan.
Branch: `file-notes`

> **v5 supersedes v1–v4.** v4 pivoted to the hybrid model (note correctness via a
> content anchor; pause is best-effort UX) — the review confirmed that pivot is
> sound and removed the v3 quiescence/launch-token/writer-counting machinery for
> good. v5 fixes the four bounded issues the v4 review found — all *within* the
> hybrid model, none reintroducing quiescence: truthful pause ownership, a small
> durable resume transaction, deterministic feedback-runner dispatch, and a
> timing-independent line anchor.

## Problem

The Docker file browser can browse/view/diff files mid-run but has no way to click
a line and attach a note (as `PlanPanel`/`DialogChannel` do for plan lines and
dialog questions). A reviewer who spots issues across several files has no
structured way to collect them and steer a stage.

We *want* files stable while notes are written, but do not make it a hard
concurrency guarantee. The flow pause stops the agent stages (the common writers);
for the rare case a file still changes (an uninterruptible script, a race), each
note carries the exact original line text it was written against, so the target
agent always reconciles against what the reviewer actually saw.

## Goal

In the file browser, click a line → write a note anchored to the file's current
content. The first note prompts a yes/no gate: the flow goes on a best-effort
pause (active agent stages stopped, new activations held) and a full-width "flow
paused" banner appears. The user accumulates notes across files; each note stores
its text, line, the **original line text**, and a **content digest** of the file
as seen. When done, the user picks one stage to receive all notes as feedback; the
chosen stage restarts with the notes, every other paused stage resumes, the pause
lifts, and the flow continues.

## Locked decisions

- **Hybrid model.** Note correctness = content anchor (always carried); pause =
  best-effort UX.
- **Approach A — durable marker + reuse.** No flow-level FSM event; per-stage
  pause, `Revise`/`Continue`/`*WithFeedback`, marker+direct-IO reused.
- **Injection = resume-with-feedback (Revise-style)** into the chosen paused stage,
  dispatched by a recorded `resume_kind` (not phase-guessing).
- **Other paused stages resume via the existing `Continue` path.** Interactive →
  `--resume`; **non-interactive → attempt-0 `buildRetryContext` replay** (A′).
- **Durable resume transaction.** The marker is a small crash-recoverable record
  (`paused`/`resuming`), kept until every resume transition commits.
- **Content digest, not mtime+size** for the anchor.

## Non-goals (YAGNI)

- No quiescence guarantee, launch tokens, writer counting, or continuous
  continuation descriptor.
- No new flow-scoped FSM event.
- No forcible interruption of scripts/hooks.
- Single injection target; text edit/delete only (no re-anchoring).

---

## Architecture

### Notes storage — content-anchored (durable, `.afm/runs/<run_id>/`)

`review-notes.json`, a single whole-file-rewritten document:

```json
{
  "version": 1, "rev": 7, "next_id": 8,
  "notes": [
    {
      "id": "n7", "root": "project", "path": "pkg/foo/bar.go",
      "display_path": "project/pkg/foo/bar.go",
      "reference": "[AFM file: \"/work/pkg/foo/bar.go\"]",   // workspace marker builder, NOT fmt
      "line": 42,                       // >=1 = line note; null = file-level note
      "orig_line_text": "\tif err != nil {",   // exact line as seen (null for file-level) — ALWAYS rendered
      "content_sha": "sha256:...",      // digest of the whole file as seen
      "text": "...", "created_at": "..."
    }
  ]
}
```

- `content_sha` is computed server-side from the same bytes the browser served.
- `line: null` ⇒ file-level note (`orig_line_text` null), an explicit validated
  wire shape.
- **Replacement** (a second note on an existing `(root,path,line)`) keeps the
  existing `id`/`created_at` and refreshes `text`/`content_sha`/`orig_line_text`
  from the newly viewed content (stable id, refreshed anchor — least surprising).
- Durability: atomic temp+rename (unique temp), fsync file + parent dir.
  Corrupt/unknown-version → quarantine to `<name>.corrupt-<ts>` and start empty
  (notes are advisory).
- Storage API: `SaveReviewNotes`/`LoadReviewNotes`/`DeleteReviewNotes` (idempotent);
  `SaveFeedbackOnce(stageDir, opID, text)`; a content-hash helper.

### The pause marker — a small durable resume transaction

`notes-pause.json`, written after `EvPause` has actually been applied:

```json
{
  "version": 1,
  "operation_id": "<128-bit hex>",         // crypto/rand; part of the feedback-once protocol
  "state": "paused",                        // paused | resuming
  "mode": "",                               // "" while paused; inject | cancel while resuming
  "target_stage": "",                       // the inject target while resuming
  "created_at": "...",
  "owned": [
    { "id": "stage-a", "resume_kind": "implementation" }   // ONLY stages whose EvPause CAS won
  ]
}
```

`owned` lists only stages this pause **actually** moved to `paused` (truthful
ownership). `resume_kind ∈ {planning, implementation, review, autonomous}` is the
deterministic feedback-runner selector, computed once at pause time (below).

### Best-effort pause — flag + truthful marker

`o.flowPaused atomic.Bool` mirrors the marker's existence and drives the
activation hold and the banner. `PauseFlow` (under `o.flowPauseMu`, and atomically
exclusive with finalization — see below):

1. **Set `flowPaused=true`** (activation hold starts immediately; the in-memory
   flag, not the marker, is the authority). Activation sites (`startReadyStages`,
   `tryActivatePrePlanned`, `startPlanningForUnblocked`, bootstrap
   `startPlanningForPending`, **and the active-status recovery respawns** in
   `startPlanningForPending`) skip work while it is set, leaving stages in place.
   `concurrency.shouldRun` is untouched (no stranding, no `script_after` leak).
   Best-effort: a stage already mid-activation may still run — acceptable, the note
   anchor covers any resulting change.
2. **Snapshot candidates:** status ∈ {running, planning, revising, retrying},
   non-script, not already `paused`.
3. **Apply `EvPause` to each candidate** (existing per-stage `Pause` internals:
   `EvPause` + `bumpPauseGen` + signal `interruptChans`). A candidate that left the
   From-set first (completed, `awaiting_user_input`, failed) **loses the CAS and is
   dropped** — it never enters `owned`.
4. **For each successful owner, compute `resume_kind`:** autonomous
   (`IsAuto()`/`autonomous.flag`) → `autonomous`; else `PausedFrom==planning` →
   `planning`; else (`running`/`retrying`/`revising`) → `implementation` for
   non-interactive, or for interactive use `detectInterruptedPhase` to pick
   `review` vs `implementation` (session files exist for interactive). Recorded so
   dispatch never re-guesses.
5. **Persist the marker** (`state=paused`, `owned=winners`).

Running **script** stages are left to finish (successors held by the flag); we do
not wait for drain — notes are content-anchored.

**Weak crash semantics on pause creation (explicit):** a crash after some `EvPause`
but before the marker write leaves those stages durably `paused` with **no marker**
→ "the pause request was never durably accepted." Recovery finds no marker and the
user manually Continues those stages. This is the accepted best-effort trade
(stated per the review); it needs no intent/reconciliation phase because the pause
itself carries no payload yet.

### Injection & cancel — a recoverable resume transaction

`InjectNotesAndResume(targetStageID)` (server-enforced: `flowPaused` set;
`len(notes)>0` else `no_notes` 400; target ∈ `owned` **and currently
`StatusPaused`** else 400 — a target that silently left `paused` must not receive
feedback). Under `flowPauseMu`:

1. **Persist `state=resuming, mode=inject, target_stage`** (marker still present —
   this is the durable resume intent).
2. Render notes → feedback (§ rendering). `SaveFeedbackOnce(targetDir,
   operation_id, rendered)` — the `<!-- afm-review-op: <id> -->` sentinel makes a
   crash-replayed append a no-op.
3. **Clear the in-memory `flowPaused` flag** (so resume spawns aren't held) — but
   keep the marker.
4. **Resume the target with feedback**, dispatched by its `resume_kind`:
   `EvRevise` (paused→revising) + `SpawnAgent(run<resume_kind>WithFeedback)`. This
   bypasses `detectInterruptedPhase`, so a non-interactive implementation/auto
   target is not mis-dispatched as planning.
5. **Resume every other owner** via the existing `Continue` path by `resume_kind`;
   interactive `--resume`, non-interactive attempt-0 replay (A′).
6. Re-drive scheduling so flag-held pending stages activate.
7. **`DeleteReviewNotes`, then delete the marker — last.**

`CancelNotesAndResume()`: `state=resuming, mode=cancel`; resume all owners plainly;
delete notes + marker last. No feedback.

**Recovery finishes the transaction idempotently.** On start, `ReadNotesPauseMarker`:
- **absent** → normal.
- **`state=paused`** → `flowPaused=true` (re-establish the hold); owners are already
  durably `paused` (existing recovery keeps them paused); **suppress every
  bootstrap respawn for owned stages** (the flag gates the active-status recovery
  paths too, per PauseFlow step 1); banner returns; nothing auto-resumes.
- **`state=resuming`** → finish: `SaveFeedbackOnce` (no-op if the sentinel exists);
  for each owner, reconcile from the event log and re-drive its resume by
  `resume_kind` if not already running (idempotent); the inject target uses the
  `*WithFeedback` runner, others plain; then `DeleteReviewNotes` + delete marker.
  So a crash after feedback-append / after some Continues / after note-delete all
  converge to exactly one feedback block and no stage left paused by the operation.
- **corrupt/unreadable** → log + quarantine + treat as no active pause (fail-open;
  owners stay `paused` for manual Continue; correctness unaffected).

**Control policy.** While `flowPaused` is set, external forward-driving actions
(`Continue`/`Approve`/`Retry`/`Revise`/`Button`/dialog `NotifyAnswer`/manual
`Pause`) return `409 {"error":"flow_paused"}`; the lifecycle resumes via private
helpers that bypass the guard.

### The content anchor is always carried (timing-independent)

Best-effort pause permits a late writer to change a file *after* injection
recomputes the hash but before the target reads feedback. So the hash comparison is
**diagnostic, not proof**: every line note **always** renders its stored
`orig_line_text` (JSON-quoted — source lines can contain backticks), and the
comparison only adds a `changed-at-injection` flag. The agent can always reconcile
the line number against the original text regardless of timing:

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

**Missing/unreadable at injection** (deleted, renamed, symlink, binary, over the
read limit): the note is rendered with a `file unavailable at injection` marker,
its stored safe `reference`, and its original text — the rest of the injection is
never aborted. Tests distinguish `changed` from `unavailable`. `display_path` is
JSON-quoted; deterministic order (files by `display_path`, notes by line, file-level
last); total size capped (`maxRenderedFeedback`, overflow reported pre-inject).

### Finalization guard (atomic with pause acceptance)

`PauseFlow` must be rejected once run finalization has begun, and the two must be
one atomic decision. A small mutex/CAS (`o.finalizeMu` or a `finalizing atomic.Bool`
flipped under a shared lock) is held by **both** the event-loop transition into
`runEndOfRunMemory`/`Run` return **and** `PauseFlow` from its finalization check
through setting `flowPaused`. So finalization can't start while a pause is being
accepted, and vice versa. `PauseFlow` during finalization → `409 run_finalizing`; a
pause requested after `Run` returns is a no-op. `shouldExit` consults the in-memory
`flowPaused` flag (not just marker visibility) and stays false while it is set.

### Direct-IO failure semantics (deterministic, not storage-fatal)

- **Initial marker write fails** (in `PauseFlow`) → roll back `flowPaused=false`
  and return an error; never leave the flag set with no marker.
- **Feedback write fails** (`SaveFeedbackOnce`) → leave the marker (`state=paused`
  or the already-persisted `resuming`) and notes in place for retry; return an
  error.
- **A resume FSM transition's log write fails** → the existing storage-fatal path
  applies (that's an event-log write); the `resuming` marker lets recovery re-drive.
- **Marker-clear (final delete) fails after a successful resume** → recoverable:
  recovery re-enters `state=resuming`, `SaveFeedbackOnce`/Continue are idempotent,
  no second live spawn (reconciled from the log), then retries the delete.

---

## HTTP API (`/api/flow/*`)

- `POST /api/flow/pause` → `{ paused_stages:[] }` (the `owned` ids); existing marker
  → returns it; during finalization → `409 run_finalizing`.
- `GET  /api/flow/notes` → `{ rev, notes:[] }`.
- `POST /api/flow/notes` `{ root, path, line|null, text, content_sha, expected_rev }`
  → created/replaced note. `flowPaused` required (`flow_not_paused` 409). Server
  resolves `abs`/`display_path`/`reference` from `{root,path}` via `workspace`
  (absolute path never trusted); recomputes `content_sha` and requires the client's
  to match (`stale_content` 409); line note → validate `line` in range + snapshot
  `orig_line_text`; `line:null` = file-level; `expected_rev` (`rev_conflict` 409);
  non-blank `text` ≤ `maxNoteTextLen`; count ≤ `maxNotes`.
- `PUT /api/flow/notes/{id}` `{ text, expected_rev }` / `DELETE .../{id}?expected_rev=`.
- `POST /api/flow/notes/inject` `{ stage_id }` (`no_notes` 400 if empty; target must
  be `owned` and `StatusPaused`) / `POST /api/flow/notes/cancel`.
- `/api/status` gains `flow_paused` bool and `flow_paused_stages` []string (the
  currently-`paused` owners, for the picker).

`o.flowPauseMu` serializes `PauseFlow`/CRUD/`Inject`/`Cancel`; `rev`/`expected_rev`
guard multi-tab conflicts.

## Frontend (`pkg/web/dashboard/src`)

- FileViewer line-comment UX (reuse `PlanPanel`'s `.plan-line`/`line-comment-*` +
  `PasteableTextarea`, `●` on annotated lines). The client computes and sends the
  `content_sha` of the fetched file with each add.
- Pause gate: first note while `!flow_paused` → confirm *"…поставить на паузу?"*;
  **Yes** → `POST /api/flow/pause`, enable editor; **No** → close.
- Full-width banner (App.tsx, after `<FlowHeader/>`), gated by `flow_paused`:
  *"⏸ Flow paused — review notes (N)"* + **Send notes** + **Cancel**.
- Notes modal (from **Send**): grouped by file, edit/delete, stage **picker**
  (`flow_paused_stages`); empty → disclosed, **Send** disabled. `stale_content` 409
  → "file changed, reload & re-anchor"; `rev_conflict` → "notes changed elsewhere".
- `use-status.ts` maps `flow_paused`/`flow_paused_stages`; `run-client.ts` gains the
  seven calls.

## Edge cases

- **No pausable stage** — pause engages (flag + banner); empty picker → Send
  disabled; `shouldExit` held by the flag.
- **Script / racing agent writes during review** — allowed; the always-present
  `orig_line_text` (plus the injection-time `changed`/`unavailable` diagnostic) lets
  the agent reconcile.
- **Target left `paused` before inject** — rejected (validate `StatusPaused`), no
  feedback written.
- **Crash mid-lifecycle** — resume transaction + `SaveFeedbackOnce` + idempotent
  deletes converge on recovery.
- **User-manually-paused stage** — not in `owned`; untouched by inject/cancel.

---

## Testing

**`pkg/state`** — notes roundtrip (`next_id` monotonic across delete, `rev` bump,
`line:null`, replacement keeps id/created_at & refreshes anchor, missing→empty);
corrupt/unknown-version quarantine; `SaveFeedbackOnce` no-op on repeat op-id; marker
write/read/clear; fsync path; content-hash helper.

**`pkg/orchestrator`**
- **Truthful ownership:** candidate completing / entering `awaiting_user_input`
  before its `EvPause` is dropped from `owned`; the marker lists only CAS winners.
- **Activation hold:** a newly-unblocked dependent stays `pending`; bootstrap
  active-status respawns are suppressed for owned stages on valid-marker recovery; a
  script finishing during pause doesn't start its successor.
- **Dispatch (finding 3):** inject into non-interactive planning, non-interactive
  implementation, `agents:[auto]`, planning-retry, implementation-retry, and
  interactive inline review each select the correct `run*WithFeedback` runner —
  assert the actual runner/prompt, not just FSM status.
- **Anchor (finding 4):** digest matches yet the file mutates before the target
  reads feedback → the feedback still carries `orig_line_text`; JSON-quoted lines
  with backticks survive.
- **Resume transaction (finding 2):** crash after feedback append / after
  `state=resuming` before target transition / after target transition before spawn /
  after some non-target Continues / after note delete before marker delete — repeated
  recovery → one feedback block, no owner left paused by the operation.
- **Pause creation (finding 1):** crash after some `EvPause` before marker write →
  no marker, stages manually continuable (documented weak semantics).
- **Finalization:** `PauseFlow` and finalize are atomically exclusive; pause after
  `Run` returns is a no-op; `shouldExit` held by the flag.
- **Direct-IO failures:** initial-marker failure rolls back the flag;
  feedback-failure retains marker+notes; marker-clear-failure recovers without a
  second spawn.
- Control policy → 409 `flow_paused`. FSM: `EvRevise` From includes `paused`.

**`pkg/server`** — CRUD gating + `stale_content`/`rev_conflict`/`no_notes`/
`flow_not_paused`/`run_finalizing` codes; server-side `{root,path}` resolution +
`content_sha` recompute; `line:null` validation; unavailable-file rendering path;
`/api/status` fields; reference round-trips odd filenames.

**Frontend** — line comment + `●`; client `content_sha`; pause confirm; banner;
notes edit/delete; picker inject; `stale_content`/`rev_conflict` handling;
`use-status`.

**Live verification** (`verify` skill, Docker + real browser): click a line → pause
gate → banner + a real **non-interactive** agent stage stops → notes across two
files → inject into that stage and confirm it restarts as **implementation** (not
planning) reading the notes once → others resume → banner clears; reload mid-pause
(survives); mutate a file during pause and see the `orig_line_text` + `changed`
diagnostic in the injected feedback; delete a reviewed file and see the
`unavailable` render without aborting other notes.

## Files touched (anticipated)

- `pkg/state/state.go` — content-anchored notes IO, resume-transaction marker IO,
  `SaveFeedbackOnce`, content-hash helper.
- `pkg/orchestrator/` — `flowPaused`, `flowPauseMu`, `finalizing` guard, marker
  transaction, `PauseFlow` (EvPause-first + `resume_kind`), `Inject`/`Cancel`/CRUD,
  activation-hold at all activation + active-status-recovery sites, control policy,
  `shouldExit` guard, drift/unavailable rendering, recovery, direct-IO error rules.
- `pkg/orchestrator/retry.go` — attempt-0 replay context (A′).
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` From += `paused`.
- `pkg/executor`/runner_factory — thread the attempt-0 resume context.
- `pkg/server/` — `/api/flow/*`, `statusResponse` fields, `FlowActions` wiring, note
  resolution/validation + `content_sha`, error codes.
- `pkg/web/dashboard/src/` — FileViewer line comments + client hashing, pause confirm,
  banner, notes modal + picker, `run-client`, `use-status`, CSS.
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.

## Acceptance criteria (mechanically testable in the plan)

1. `owned` contains only stages whose `EvPause` CAS won; inject also re-validates
   `StatusPaused` before writing feedback.
2. A crash anywhere in inject/cancel cannot lose resume ownership or orphan
   delivered feedback (durable `resuming` transaction + idempotent recovery).
3. Every target type selects the correct `run*WithFeedback` runner via `resume_kind`
   (non-interactive implementation/auto and retrying included).
4. Every line note always carries `orig_line_text` into feedback; a post-comparison
   write cannot destroy the anchor.
5. Deleted/unreadable reviewed files yield actionable feedback, not an aborted
   injection.
6. Pause acceptance and run finalization are atomically exclusive; `shouldExit` is
   held by the in-memory flag.
7. Every direct-IO failure leaves a documented retryable/manually-recoverable state.
