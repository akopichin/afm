# File-browser review notes with best-effort pause — design (v4, hybrid)

Date: 2026-09-08
Status: revised after three design-review rounds; **architecture pivoted to the
hybrid model** (user decision). Pending final user review → implementation plan.
Branch: `file-notes`

> **v4 supersedes v1–v3.** The first three drafts pursued a *strong* contract —
> note correctness depended on perfectly freezing every AFM writer (quiescence).
> Three review rounds showed that guaranteeing this correctly requires a refactor
> of the orchestrator's core launch/activity accounting (launch tokens,
> reference-counted writer handles, executing-hook-subprocess counting, admission
> ABA elimination). Per the "prefer simplicity" bar, we pivot: **note correctness
> rests on a content anchor (digest + original line text captured at write time),
> and the flow pause becomes a best-effort UX affordance, not a correctness
> boundary.** This removes the need for quiescence detection, launch tokens,
> writer counting, and the perfect admission barrier — and with them the bulk of
> the v3 review's blocking findings, which existed *only* because quiescence was
> load-bearing. The earlier "strong contract / wait-for-scripts" decision is
> superseded by this hybrid choice.

## Problem

The Docker file browser can browse/view/diff files mid-run but has no way to
click a line and attach a note (as `PlanPanel`/`DialogChannel` do for plan lines
and dialog questions). A reviewer who spots issues across several files has no
structured way to collect them and steer a stage.

We *want* files to be stable while notes are written, but we do not need to make
that a hard concurrency guarantee. In practice the flow pause stops the agent
stages (the common writers); for the rare case a file still changes (an
uninterruptible script, a race), the note carries exactly what the user saw, and
any drift is detected and surfaced at injection.

## Goal

In the file browser, click a line → write a note anchored to the file's current
content. The first note prompts a yes/no gate: to write notes the flow is put on
a best-effort pause (active agent stages stopped, new activations held) and a
full-width "flow paused" banner appears. The user accumulates notes across any
number of files; each note stores its text, line, the **original line text**, and
a **content digest** of the file as seen. When done, the user picks one stage to
receive all notes as feedback (reusing the existing feedback mechanism); the
chosen stage restarts with the notes, every other paused stage resumes, the pause
lifts, and the flow continues.

## Locked decisions

- **Hybrid model.** Note correctness = content anchor; pause = best-effort UX.
- **Approach A — durable marker + reuse.** No flow-level FSM event; per-stage
  pause, `Revise`/`Continue`/`*WithFeedback`, marker+direct-IO reused.
- **Injection = resume-with-feedback (Revise-style)** into the chosen paused stage.
- **Other paused stages resume via the existing `Continue` path.** Interactive →
  real `--resume`; **non-interactive → the resume injects the `buildRetryContext`
  replay block at attempt 0** (decision A′), so they don't redo work. We reuse
  afm's existing resume/recovery dispatch (`resumeStageAtStatus`) unchanged — we
  inherit its shipped semantics rather than inventing a new continuation model.
- **Durable.** Notes + pause marker survive reload/restart. Recovery reuses the
  existing paused-stage recovery; the marker only re-establishes the best-effort
  activation hold.
- **Content digest, not mtime+size.** The stale-detection anchor is a content hash
  (the workspace ETag is `mtime+size` — fine as an HTTP cache validator, not as a
  correctness anchor).

## Non-goals (YAGNI)

- **No quiescence guarantee, no launch tokens, no writer counting.** Correctness
  does not depend on perfectly stopping writers.
- No new flow-scoped FSM event; no new continuation-descriptor/session machinery
  (we reuse existing resume paths).
- No forcible interruption of scripts/hooks (they finish; if they write, drift is
  detected).
- Single injection target; text edit/delete only (no re-anchoring).

---

## Architecture

### Notes storage — content-anchored (durable, `.afm/runs/<run_id>/`)

`review-notes.json`, a single whole-file-rewritten document (tiny, human-typed):

```json
{
  "version": 1,
  "rev": 7,                 // ETag; bumped per write; multi-tab 409
  "next_id": 8,             // persisted; IDs never reused after delete
  "notes": [
    {
      "id": "n7", "root": "project", "path": "pkg/foo/bar.go",
      "display_path": "project/pkg/foo/bar.go",
      "reference": "[AFM file: \"/work/pkg/foo/bar.go\"]",   // from the workspace marker builder, NOT fmt
      "line": 42,                       // >=1 = line note; null = file-level note
      "orig_line_text": "\tif err != nil {",   // the exact line as seen (null for file-level)
      "content_sha": "sha256:...",      // digest of the whole file as seen — the correctness anchor
      "text": "...", "created_at": "..."
    }
  ]
}
```

- `content_sha` is computed server-side from the file the user actually annotated
  (the same bytes the browser served). It is the drift anchor: at injection we
  recompute and compare.
- `line: null` ⇒ file-level note (`orig_line_text` null). This is an explicit,
  validated wire shape (the v3 render had a `File:` branch with no schema for it).
- Durability: atomic temp+rename (unique temp name), fsync file + parent dir.
  Corrupt/unknown-version → quarantine to `<name>.corrupt-<ts>` and start empty
  (notes are advisory; losing a corrupt notes file is not a safety violation).
- Storage API (direct IO, like `SavePreNote`): `SaveReviewNotes`/`LoadReviewNotes`/
  `DeleteReviewNotes` (delete idempotent); `SaveFeedbackOnce(stageDir, opID, text)`.

### Best-effort pause — a simple flag + marker (NOT a correctness boundary)

`o.flowPaused atomic.Bool`, mirrored by a durable marker `notes-pause.json`:

```json
{
  "version": 1,
  "operation_id": "<128-bit hex>",   // crypto/rand; part of the feedback-once protocol
  "paused_stages": ["stage-a"],      // stages THIS pause put into `paused` (for resume ownership)
  "created_at": "..."
}
```

- **Activation hold.** While `flowPaused` is set, activation sites
  (`startReadyStages`, `tryActivatePrePlanned`, `startPlanningForUnblocked`,
  bootstrap `startPlanningForPending`) skip the `EvStart*` transition and leave the
  stage in `pending`/`ready`. `concurrency.shouldRun` is left untouched (so no
  stranding, no `script_after` leak — the v1 mistakes). This is a plain flag check,
  not an atomic barrier: a stage already mid-activation when the pause lands may
  still run — acceptable, because a resulting file change is caught by the note's
  `content_sha` at injection. Best-effort by design.
- **Pausing active stages.** `PauseFlow` snapshots stages with status ∈ {running,
  planning, revising, retrying}, non-script, not already `paused`, records them as
  `paused_stages`, sets `flowPaused`, writes the marker, then applies the existing
  per-stage `Pause` internals (`EvPause` + `bumpPauseGen` + signal
  `interruptChans`) to each. Running **script** stages are left to finish (their
  successors are held by the flag). We do **not** wait for drain before enabling
  note editing — notes are content-anchored, so a still-draining agent that writes
  is caught by drift detection.
- **Banner honesty.** The banner shows "⏸ Flow paused". Optionally it lists stages
  still transitioning to `paused` as informational text, but note editing is not
  gated on it.

### Injection & cancel — reuse Revise/Continue

`InjectNotesAndResume(targetStageID)` (server-enforced: `flowPaused` set,
`len(notes) > 0` else `no_notes` 400, target ∈ `paused_stages` else 400):

1. Render notes → feedback text (with drift annotations, below). Deliver **once**:
   `SaveFeedbackOnce(targetDir, operation_id, rendered)` embeds
   `<!-- afm-review-op: <id> -->` and no-ops if present (crash-safe against
   double-append). `operation_id` is 128-bit, so it can't collide.
2. Clear `flowPaused` and delete the marker **after** feedback is delivered but
   **before** resuming (so the resume spawns aren't held by the flag).
3. Resume the target with feedback: FSM adds `paused` to `EvRevise`'s From-set;
   `EvRevise` (paused→revising) + `SpawnAgent(run<phase>WithFeedback)` via the
   **existing** `resumeStageAtStatus` revising dispatch (we inherit its phase
   detection — the same one afm-restart recovery already relies on).
4. Resume every other `paused_stages` entry via the existing `Continue` path
   (`EvContinue` → `resumeStageAtStatus`); interactive `--resume`, non-interactive
   attempt-0 replay (decision A′).
5. Re-drive scheduling so flag-held pending stages activate.
6. `DeleteReviewNotes`.

`CancelNotesAndResume()` = clear flag + marker, resume all `paused_stages` plainly,
delete notes. No target, no feedback.

**Control policy.** While `flowPaused` is set, external forward-driving actions
(`Continue`, `Approve`, `Retry`, `Revise`, `Button`, dialog `NotifyAnswer`, manual
`Pause`) return a machine-readable 409 `{"error":"flow_paused"}`; the lifecycle's
own resume uses private helpers that bypass the guard. This keeps the UX coherent
(no conflicting user actions mid-review); it is not a correctness barrier for note
anchoring.

### Drift detection at injection (the payoff of content-anchoring)

For each note, recompute the target file's `content_sha`. If it differs from the
stored one, the rendered feedback annotates the note as possibly stale and
includes the `orig_line_text` verbatim, so the agent reconciles against what the
reviewer actually saw:

```
### [AFM file: "/work/pkg/foo/bar.go"]  "project/pkg/foo/bar.go"
- Line 42 (⚠ file changed since note; original line was: `if err != nil {`): <text>
- Line 88: <text>
- File: <text>            // line-level == null
```

`display_path` is JSON-quoted in the header (filenames may contain `)`/newlines).
Deterministic order (files by `display_path`, notes by line, file-level last).
Total rendered size capped (`maxRenderedFeedback`); overflow reported before
inject.

### Recovery — reuse existing, fail-open on the advisory marker

On start, `ReadNotesPauseMarker`:
- **absent** → normal.
- **present & valid** → set `flowPaused=true` (re-establish the activation hold);
  the `paused_stages` are already durably `paused` in the event log (existing
  recovery keeps them paused); notes file persists; the banner returns. The user
  finishes and injects/cancels. Nothing auto-resumes.
- **corrupt/unreadable** → log + quarantine + **treat as no active pause**
  (fail-open is safe here: the only consequence is the activation hold isn't
  re-established; any durably-`paused` stages simply stay paused for the user to
  Continue manually). Correctness doesn't depend on the marker.

Feedback delivery idempotency across a crash is handled by the `SaveFeedbackOnce`
sentinel, not by the marker. `DeleteReviewNotes` is idempotent. There is no
multi-phase transaction to recover — the pivot removed it.

### What we deliberately do NOT build (and why it's safe)

- **No quiescence detection / writer counting / launch tokens / admission ABA
  elimination** — correctness comes from `content_sha`, so a stale launch or a
  late-draining agent that writes a file is *detected*, not *prevented*. (Resolves
  v3-review findings 1–6 by removing their premise.)
- **No new continuation descriptor / session-timing work** — we reuse the shipped
  `resumeStageAtStatus` dispatch; whatever it does for crash recovery today is what
  Continue does here. (Resolves v3-review finding 6.)
- **No hook-subprocess accounting** — hooks aren't waited on. (Resolves finding 3.)
- **End-of-run memory writers** (`runEndOfRunMemory`) — `PauseFlow` is rejected once
  run finalization has begun, and a pause requested after `Run` returns is a no-op
  (documented), so there's nothing to track. (Resolves the additional gap.)

---

## HTTP API (`/api/flow/*`)

- `POST /api/flow/pause` → `{ paused_stages:[] }`; existing marker → returns it.
  Rejected (`run_finalizing` 409) once run finalization has begun.
- `GET  /api/flow/notes` → `{ rev, notes:[] }` (lock-free read).
- `POST /api/flow/notes` `{ root, path, line|null, text, content_sha, expected_rev }`
  → created note. `flowPaused` required (else `flow_not_paused` 409). Server resolves
  `abs`/`display_path`/`reference` from `{root,path}` via `workspace` (client
  absolute path never trusted); recomputes `content_sha` from the current file and
  requires it to match the client's (`stale_content` 409 — the reviewer viewed an
  old version); for a line note validates `line` within content and snapshots
  `orig_line_text`; `line:null` = file-level; `expected_rev` (`rev_conflict` 409),
  non-blank `text` ≤ `maxNoteTextLen`, count ≤ `maxNotes`. One note per
  `(root,path,line)` (replace).
- `PUT /api/flow/notes/{id}` `{ text, expected_rev }` / `DELETE .../{id}?expected_rev=`.
- `POST /api/flow/notes/inject` `{ stage_id }` (`no_notes` 400 if empty) /
  `POST /api/flow/notes/cancel`.
- `/api/status` gains `flow_paused` bool and `flow_paused_stages` []string (the
  picker). (No `pausing`/`quiescent`/`blockers` — there's no quiescence phase.)

A single `o.flowPauseMu` serializes `PauseFlow`/CRUD/`Inject`/`Cancel` so
whole-file note writes and the lifecycle don't interleave; `rev`/`expected_rev`
guards multi-tab conflicts.

## Frontend (`pkg/web/dashboard/src`)

- FileViewer line-comment UX (reuse `PlanPanel`'s `.plan-line`/`line-comment-*` +
  `PasteableTextarea`, `●` on annotated lines). The client sends the `content_sha`
  of the file it rendered (computed from the fetched content) with each add.
- Pause gate: first note while `!flow_paused` → confirm *"…поставить на паузу?"*;
  **Yes** → `POST /api/flow/pause`, then enable the editor; **No** → close, flow
  untouched.
- Full-width banner (App.tsx, after `<FlowHeader/>`), gated by `flow_paused`:
  *"⏸ Flow paused — review notes (N)"* + **Send notes** + **Cancel**.
- Notes modal (from **Send**): grouped by file, edit/delete, stage **picker**
  (`flow_paused_stages`); empty picker → disclosed *"no active stage to receive
  notes"*, **Send** disabled. A `stale_content` 409 on add tells the user the file
  changed — reload and re-anchor.
- `use-status.ts` maps `flow_paused`/`flow_paused_stages`; `run-client.ts` gains the
  seven calls.

## Edge cases

- **No pausable agent stage** — pause still engages (flag + banner);
  `flow_paused_stages` empty → Send disabled, disclosed upfront. `shouldExit` is
  held open while the marker exists so the server stays up for the reviewer.
- **A script (or a racing agent) changes a file during review** — allowed; the
  note's `content_sha` mismatch surfaces the drift at injection with the original
  line text.
- **User-manually-paused stage before review pause** — not in `paused_stages`;
  never resumed by inject/cancel.
- **Crash mid-inject** — `SaveFeedbackOnce` sentinel prevents double feedback;
  `DeleteReviewNotes` idempotent; no transaction to reconcile.
- **Forward control during pause** — 409 `flow_paused`.

---

## Testing

**`pkg/state`** — notes roundtrip (`next_id` monotonic across delete, `rev` bump,
`line:null` file-level, missing→empty); corrupt/unknown-version quarantine;
`SaveFeedbackOnce` no-ops on repeated op-id; marker write/read/clear; fsync path.

**`pkg/orchestrator`**
- `PauseFlow` sets the flag + marker, snapshots the right stages, applies `EvPause`;
  the activation hold keeps a newly-unblocked dependent in `pending` (no stranding,
  `shouldRun` untouched); a running script finishing during pause does not start its
  successor.
- Inject writes rendered feedback **once** (repeat inject / crash-replay doesn't
  double-append), `EvRevise`-from-`paused` resumes the target via the existing
  dispatch, others resume via Continue, flag+marker+notes cleared, held stages
  activate. Cancel resumes all plainly, no feedback.
- **Drift:** a note whose file changed between add and inject renders the
  stale-warning + `orig_line_text`; an unchanged file renders clean.
- Resume context (A′): first resumed non-interactive attempt's prompt contains the
  replay block; interactive resumes its session.
- Control policy: forward actions → 409 `flow_paused`; `shouldExit` held by marker;
  `PauseFlow` rejected during finalization; pause after `Run` returns is a no-op.
- FSM: `EvRevise` From includes `paused`.
- Recovery: valid marker re-establishes the hold; corrupt marker fails open (logged,
  quarantined, no stranded transaction).

**`pkg/server`** — CRUD gating + `stale_content`/`rev_conflict`/`no_notes`/
`flow_not_paused` codes; server-side `{root,path}` resolution + `content_sha`
recompute (absolute path never trusted); `line:null` file-level validation;
`/api/status` fields; paths with quotes/backslashes/newlines/non-ASCII round-trip
via stored `reference`; `display_path` JSON-quoted in the render.

**Frontend** — line comment + `●`; client `content_sha` computation; pause confirm;
banner; notes edit/delete; picker inject; `stale_content`/`rev_conflict` handling;
`use-status` mapping.

**Live verification** (`verify` skill, Docker + real browser): click a line → pause
gate → banner + real agent stage stops → write notes across two files → inject into
one stage, see its `feedback.md` receive the notes once + others resume → banner
clears → flow continues; reload mid-pause and confirm the pause + notes survive;
force a file change during pause and see the drift warning in the injected feedback.

## Files touched (anticipated)

- `pkg/state/state.go` — content-anchored notes IO, marker IO, `SaveFeedbackOnce`,
  content-hash helper.
- `pkg/orchestrator/` — `flowPaused` flag, `flowPauseMu`, marker, `PauseFlow`/
  `Inject`/`Cancel`/note CRUD, the activation-site flag check, control policy,
  `shouldExit` guard, finalization guard, drift-annotated rendering, recovery
  (fail-open marker).
- `pkg/orchestrator/retry.go` — attempt-0 replay context for resumed runs (A′).
- `pkg/orchestrator/bus/fsm.go` — `EvRevise` From += `paused`.
- `pkg/executor`/runner_factory — thread the attempt-0 resume context.
- `pkg/server/` — `/api/flow/*`, `statusResponse` fields, `FlowActions` wiring, note
  resolution/validation + `content_sha` recompute, machine-readable error codes.
- `pkg/web/dashboard/src/` — FileViewer line comments + client content hashing, pause
  confirm, banner, notes modal + picker, `run-client`, `use-status`, CSS.
- `docs/superpowers/plans/2026-09-08-file-browser-review-notes.md` — the plan.

## Acceptance criteria (mechanically testable in the plan)

1. Note correctness is independent of pause timing: a file changed between add and
   inject is detected (`content_sha`) and surfaced, never silently mis-anchored.
2. The activation hold never strands a stage (`shouldRun` untouched; held stages
   stay `pending`/`ready` and activate on resume).
3. Feedback is delivered at most once per operation across crash/replay.
4. Inject resumes the target with feedback and every other paused stage plainly,
   via the existing resume dispatch; the first non-interactive resumed attempt
   carries the replay context.
5. A corrupt marker fails open (no stranded transaction, evidence quarantined); a
   valid marker survives restart and re-establishes the hold + banner.
6. Pause is unavailable during/after run finalization; `shouldExit` stays open
   while a marker exists.
