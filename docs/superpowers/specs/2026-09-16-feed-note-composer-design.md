# Feed note composer — messenger-style agent notes (branch `dialogs`)

## Problem

Today the only way to send a mid-run note to a live agent is the kebab menu's
"Add note for agent" → `AgentNoteModal` (revise variant). The dashboard feed is
presented as a messenger, yet there is no messenger-style input pinned to the
bottom to write to the agent. We want a persistent composer at the bottom of the
feed for the **currently running** stage, styled like a chat input, with an
Attach button (image upload / paste, and the optional project-file picker), a
growing textarea, and delivery through the existing graceful-pause + feedback
path. The note must appear on the right in the feed with its own text (like a
dialog answer) but **not** be clickable. Because a running stage can now be
addressed from the composer, the kebab's "Add note for agent" item is removed;
the pre-note item (for not-yet-started stages) stays.

## Non-goals (YAGNI)

- No new HTTP endpoint — reuse `POST /api/stages/{id}/revise`.
- No new FSM states.
- No markdown rendering inside the composer.
- No persisted unsent-draft across reloads.
- No composer in any workspace view other than Feed.
- Do **not** build a `revised`/"revisions: N" feed line — see the correction
  under Decisions #2 (it does not exist today and we are not adding it).

## Decisions (user-confirmed, with one correction)

1. **Visibility:** the composer renders only when the selected workspace stage
   is `running` **and not a script stage** (agent is live and revise-able). See
   the script-guard finding under Correctness below. `awaiting_approval` revises
   its plan through `PlanPanel`, not here.
2. **`revisions: N` line — correction.** The user chose "keep both" believing a
   system `revisions: N` line already renders per revise. It does **not**:
   `feed-view-model.ts`'s `case 'revised'` is dead — no backend producer emits a
   live-or-replayed `revised` feed event (`transitionToFeedEvents`,
   `events_handler.go:84-103`, maps a `revised` transition to `stage_status_changed`
   only; no live publisher exists). So there is nothing to "keep both" of: the
   **note bubble is the sole feed record** of a note. We do not add a `revised`
   producer (out of scope, YAGNI). A revise still emits the usual
   `stage_status_changed` line, unchanged.
3. **Kebab:** "Add note for agent" is removed for both `running` and
   `awaiting_approval`. Pre-note (pending) stays. Custom `stage.buttons` stay.

## Architecture

### Delivery (reuse Revise) — with an applied/seq return (P2.2, P2.1)

The composer's submit calls `reviseStage(id, text)` →
`POST /api/stages/{id}/revise` `{feedback}` → `handleRevise`.

`Orchestrator.Revise` (`control_api.go`) currently returns `nil` on **three
no-op paths** (status not `awaiting_approval`/`running`; `Trigger` CAS lost on
either branch — `control_api.go:186-194,208-209`), so a caller cannot tell a
delivered note from a dropped one. We change its contract:

- `Orchestrator.Revise(ctx, stageID, feedback) (applied bool, seq uint64, err error)`.
  On the running branch, capture the transition seq via `triggerWithSeq`
  (instead of `Trigger`, which discards it); return `applied=true, seq=<that seq>`
  only when the transition was actually applied AND `SaveFeedback` succeeded.
  Every no-op path returns `applied=false, seq=0, err=nil`.
- The `StageActions` interface (`pkg/server/actions.go:17`) is updated to the
  same signature.
- `Orchestrator.Button` (`control_api.go:242`) adapts:
  `_, _, err := o.Revise(...); return err` — keeps its own `error` return.
  Button clicks go through `handleStageButton`, **not** `handleRevise`, so they
  produce no note bubble (unchanged behavior).
- Test mocks/fakes implementing `StageActions.Revise` are updated to the new
  signature.

`handleRevise` (`pkg/server/handlers.go`):
- Add the **script guard** it currently lacks (mirror `handleStageButton`,
  `handlers.go:285-288`): if `s.stageIsScript[stageID]` → `400`. A script has no
  live agent; `Revise` would move it to `revising`, after which script
  completion is dropped (`completeStage` rejects `revising`) — the stage would
  hang. This is defense-in-depth behind the client `!isScript` gate.
- Call `applied, seq, err := s.actions.Revise(...)`.
  - `err != nil` → `500`.
  - `!applied` → `409` (stage no longer accepts the note; a real
    snapshot-vs-action race). The composer surfaces this as a rejection and
    **keeps** the text (see P1.2).
  - `applied` → emit the `agent_note` notice (below), return `200`.

### Note text in the feed — new `agent_note` notice with a unique id

The feed is event-driven from the backend; there is no event carrying the note
text. Mirror exactly how `dialog_answer` surfaces text, plus a unique id so
live/replay reconcile correctly (P2.1):

- New bus event constant `bus.EventAgentNote` (`"agent_note"`) in
  `pkg/orchestrator/bus/bus.go`.
- In `handleRevise`, only when `applied`, build a **single payload**
  `payload := map[string]any{"id": seq, "text": mcp.DialogSnippet(feedback)}`
  (reuse the dialog snippet helper for consistent truncation) and:
  - `s.uiBus.Publish(bus.Event{Type: bus.EventAgentNote, StageID: stageID, Data: payload})`
    (live), and
  - `stagefiles.AppendNotice(s.runDir, stageID, string(bus.EventAgentNote), payload)`
    (persisted, replayed by `/api/events`).
  The **same** payload object goes to both, so the live event and the replayed
  notice are byte-identical.
- `id = seq` (the EvRevise transition seq) is unique and monotonic across
  restarts (seqs are persisted in `state.json`'s `last_seq`). It is the
  reconciliation key, and it distinguishes two notes with identical text.

### Why the unique id is required (P2.1)

Both the live `uiBus` event and the reconstructed notice are **seq-less at the
top level** (`reconstructNotices` does not set `feedEvent.Seq`; `uiBus.Publish`
of a notice carries no seq). So the client's `dedupeKey`
(`use-event-feed.ts:169-172`) falls to the content key
`type|stageId|JSON(payload)`. Two consequences, both handled by putting a unique
`id` inside the payload:

- **Same note, live+replay:** identical payload → identical content key →
  deduped (as for `dialog_answer`). We add `agent_note` to
  `CONTENT_DEDUPE_ON_INGEST` (`use-event-feed.ts:20`) so the history-before-live
  ordering is also caught on ingest; `mergeHistory` already covers
  live-before-history via the same key.
- **Two distinct notes, identical text:** different `id` → different content key
  → they do **not** collapse (the bug a content-only key would cause).

No backend `reconstructNotices` dedup entry is needed for `agent_note` — the
notice is written once per applied revise, so replay alone never doubles it;
the only doubling risk is the client live+history merge, solved above. (Adding
it to `dialogDedupTypes` would be harmless but unnecessary; we leave it out to
avoid implying content-collapse of distinct notes.)

### Frontend rendering

- `feed-view-model.ts`: add `case 'agent_note'` →
  `{ actor:'user', tone:'neutral', kind:'message', text: str(obj.text), mono:false }`
  with `navigable` **omitted** → `FeedGroupView` renders a plain, non-clickable
  `<div>`.
- `types/afm-event.ts`: add `agent_note` to the known event-type lists so the
  live stream and reconstructed notices are accepted (not filtered) by
  `use-event-feed`/`toEvent`.

### Composer component

- New `components/feed-workspace/FeedComposer.tsx`:
  - Props: `stageId: string`, `onSend: (text: string) => Promise<void>`.
  - Local `value` state. Renders `PasteableTextarea` (growing textarea via
    `useAutoGrowTextarea`, clipboard image paste via `useImagePaste`, Attach menu
    via `allowFileReferences`) + a round send button (▷).
  - Submit (send button or `PasteableTextarea.onSubmit` = Cmd/Ctrl+Enter):
    whitespace-only → no-op; otherwise `await onSend(trimmed)`; **clear only on
    resolve, keep the text on reject** (P1.2). `onSend` must therefore reject on
    failure — see App wiring.
  - `allowFileReferences` set (same as `AgentNoteModal`): image upload always
    available; project-file picker item only when the Docker file-browser
    capability is on. Mounted inside the App-level `FileBrowserProvider`, so
    `useFileBrowserOptional()` is safe.

### `FeedWorkspace` layout + wiring

- New props: `noteTarget: string | null` (stage id to write to, or null) and
  `onSendNote: (stageId: string, text: string) => Promise<void>`.
- Render the composer only when `mode === 'feed' && noteTarget !== null`, as a
  **keyed** element: `<FeedComposer key={noteTarget} stageId={noteTarget} … />`.
  Keying by stage id remounts a fresh, empty draft when the user switches
  directly between two running stages, so a draft never sends to the wrong stage
  (P1.3) and any stale file-picker callback is invalidated.
- Layout: `.feed-workspace` is a flex column; `.feed-scroll` gets
  `flex:1; min-height:0`; `.feed-composer` is `flex-shrink:0`. The floating
  JumpToLatest button stays inside `.feed-scroll`.
- CSS in `skins/base/feed-workspace.css`, tokenized (semantic tokens only), so
  it adapts across skins × light/dark. Rebuild the bundle after.

### App wiring

- `noteTarget = (workspaceStage?.status === 'running' && !workspaceStage.isScript)
  ? workspaceStage.id : null`.
- `onSendNote(stageId, text)` = `reviseStage(stageId, text)` **without swallowing
  the rejection** — it must reject so the composer keeps the text on failure
  (P1.2). (This differs from the current `handleSubmitNote`, which logs and
  resolves; that pattern suited a modal, not the composer.) A top-level
  `.catch` for logging may wrap it, but it must re-throw / return a rejected
  promise to the composer.
- Pass `noteTarget`/`onSendNote` into both `FeedWorkspace` render sites (the feed
  view and the `workspaceStage === null` fallback; in the fallback `noteTarget`
  is `null`).
- Remove the revise-variant `AgentNoteModal` render, `noteModalStageId` state,
  and `onAddNote` wiring. `AgentNoteModal` stays (still used for pre-note).

### Kebab (`StagesList`)

- Delete the "Add note for agent" `<li>` and the `onAddNote` prop.
- Keep pre-note (`canPreNote`, pending) and the custom `stage.buttons` block
  (still gated by `canAddNote`, the live-agent condition).
- `hasKebab(stage)` becomes
  `(canAddNote(stage) && stage.buttons.length > 0) || canPreNote(stage) || canPause(stage)`
  so a running stage with no buttons still shows the kebab via Pause, while an
  `awaiting_approval` stage with no buttons shows no empty kebab.
- Drop the divider that separated the removed note item from the buttons block
  (buttons become the top block).

### Attention / focus interaction

The composer textarea is an editable element, so `use-workspace-view`'s
`suppressed` signal (true while an editable element is focused) already keeps a
new arrival from stealing focus mid-typing — the same guarantee the line-comment
and answer boxes rely on. Verify during implementation.

## Testing

- `feed-view-model.test.ts`: `agent_note` → right, user, `message`, text from
  payload, **non-navigable** (no `navigable` flag).
- `use-event-feed.test.ts`: an `agent_note` delivered both live and via history
  replay appears once; two `agent_note`s with identical text but different `id`
  appear twice.
- `FeedWorkspace.test.tsx`: composer shown only when `noteTarget` non-null and
  `mode==='feed'`; submit calls `onSendNote`; clears on resolve; **keeps text on
  reject**; remounts (empty) when `noteTarget` changes.
- `App.test.tsx`: `noteTarget` excludes script stages and non-running stages;
  `onSendNote` rejection propagates to the composer (text retained).
- `StagesList.test.tsx`: no "Add note for agent" for `running`/
  `awaiting_approval`; pre-note present for pending; custom buttons still render;
  no empty kebab for an `awaiting_approval` stage with no buttons.
- Backend (`pkg/server`): `handleRevise` on a script stage → `400`; a no-op
  Revise (`applied=false`) → `409` and **no** `agent_note` notice; an applied
  Revise appends one `agent_note` notice `{id,text}` live + into `notices.jsonl`
  and `/api/events` replays it (mirror `dialog_answer_feed_test.go`).
- `Revise` unit/contract tests updated for `(applied, seq, err)`; the no-op
  paths return `applied=false`.

## Files touched

- New: `pkg/web/dashboard/src/components/feed-workspace/FeedComposer.tsx`.
- Edit (frontend): `FeedWorkspace.tsx`, `feed-view-model.ts`, `types/afm-event.ts`,
  `hooks/use-event-feed/use-event-feed.ts`, `app/App.tsx`,
  `components/stages-list/StagesList.tsx`, `skins/base/feed-workspace.css`
  (+ bundle rebuild), plus their tests.
- Edit (backend): `pkg/orchestrator/bus/bus.go` (`EventAgentNote`),
  `pkg/orchestrator/control_api.go` (`Revise` returns `(applied, seq, err)`;
  `Button` adapts), `pkg/server/actions.go` (interface), `pkg/server/handlers.go`
  (`handleRevise` script guard + applied/409 + notice emit), plus test mocks.
