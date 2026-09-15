# Feed dialog text, click-to-navigate, and block markdown — design

## Problem

Three dashboard display defects, all in the presentation layer (orchestrator,
FSM, flow format, and HTTP/WS APIs are otherwise unchanged):

1. **Feed shows empty user answers.** In the Feed tab, a `user_answered` event
   renders the hardcoded label `"reply to user"` — the actual answer text is
   never shown. (`feed-view-model.ts`.)
2. **Feed shows empty agent questions, not clickable.** An `ask_user` event
   renders the hardcoded `"question to agent"` — no title, and no way to jump to
   the question.
3. **Markdown breaks in the plan/question view.** The plan-approval and dialog
   question panels render markdown **line by line** (`formatLine`) so per-line
   comments work; only fenced code and tables are collapsed into blocks. Ordered
   lists, bulleted/nested lists, blockquotes, and multi-line paragraphs render
   wrong (bare `<li>` with no `<ul>`, `1.` items as plain `<p>`, `>` as literal
   text, each wrapped line as its own `<p>`).

## Root cause

The Feed is reconstructed from `events.jsonl` (server `events_handler.go` →
client `useEventFeed` → `feed-view-model.ts`). The `ask_user`/`user_answered`
FSM transitions carry **no** Q&A text; the text lives only in the per-stage
`<phase>.dialog.jsonl` files (served via `/api/stages/<id>/dialog`). So the feed
has no text to show and hardcodes a placeholder.

`plan-panel/markdown.ts`'s `parseLineBlocks`/`formatLine` deliberately renders
one source line at a time so a review comment can anchor to a line number. Only
`nextLineBlock`'s fenced-code and table branches collapse multiple lines into a
correctly-rendered block; every other block construct falls through to
`formatLine`, which cannot render block markdown.

## Decisions (approved)

- **Feed density:** title/snippet, one line. Question → its first line as title;
  answer → a short one-line snippet (server-truncated). Full text stays on the
  dialog tab.
- **Clickable:** both question and answer feed items are clickable → open that
  stage's dialog view (the live question if the stage is still
  `awaiting_user_input`, otherwise the dialog history), best-effort scrolled to
  that Q&A.
- **Markdown:** block render with per-line comment anchoring — multi-line
  constructs (lists, blockquotes, paragraphs) collapse into one correctly
  rendered block anchored to the block's FIRST source line; genuinely single
  lines keep today's `formatLine` behavior. Applies to both plan and question
  (both use `parseLineBlocks`).

## Design

**Key data facts driving the approach (verified against the code):**

- The `ask_user`/`user_answered` FSM transitions carry no dialog id, so mapping a
  transition to a specific dialog entry can only be heuristic (by order) —
  fragile across auto-answers, revise/re-ask cycles, reused ids, and multiple
  phases. So the feed text is sourced from the **dialog records themselves**,
  which carry a real `ID`, `TS`, `Question`, `Answer`, `AnswerTS`,
  `AutoAnswered` (`mcp.Entry`).
- `user_answered` is **not** published to the live UI bus today (only
  `EventAskUser` is, in the poller). So a "user_answered live publish site" does
  not exist to hook.
- `auto_answered` already solves exactly this shape: a non-FSM UI event published
  live (`o.ui.Publish`) **and** persisted to `notices.jsonl`
  (`stagefiles.AppendNotice`), so the read path (`reconstructNotices`) and the
  live path replay the same record with the same dedup — proven, id-carrying,
  no order-correlation. The two new feed messages mirror it exactly.

### A. Two new notice-backed feed messages (mirror `auto_answered`)

- **`dialog_question`** — emitted where the poller detects an interactive
  question and publishes `EventAskUser` (`pkg/orchestrator/dialog_poller.go`, the
  `QuestionFile` in hand): `o.ui.Publish` a live event + `stagefiles.AppendNotice`
  with `{phase, id, title}` where `title = dialogSnippet(question)`. The FSM
  `EvAskUser` (→ `awaiting_user_input` status) is untouched; only the feed
  message now carries text.
- **`dialog_answer`** — emitted where a human answer is recorded
  (`pkg/server/handlers.go`, `handleDialogAnswer`, which already has `runDir` and
  the answer text): live event + `AppendNotice` with `{phase, id, title,
  auto_answered:false}`, `title = dialogSnippet(answer)`. Auto-answers keep their
  existing `auto_answered` notice (already shows text) — no change, no double
  emission.
- `dialogSnippet(text)` — tiny pure helper: first non-empty line, trimmed,
  ≤120-rune ellipsized, rune-safe; `""` for empty.

### B. Suppress the text-less rows in the CLIENT (not the server)

`ask_user` is already published live AND replayed (`transitionToFeedEvents`), and
other consumers/status may rely on it — so the backend is left untouched. Instead
the client (`feed-view-model.ts`) stops turning `ask_user`/`user_answered` into
feed rows. The rich `dialog_question`/`dialog_answer` messages (A) replace them.
This avoids a double row (the live `ask_user` + the new `dialog_question`) without
touching the server event stream. Status still shows via `stage_status_changed`.

**Dedup requirement:** `dialog_question`/`dialog_answer` are seq-less, so the feed
dedupes a live event against its notice replay by `type|stageId|JSON(payload)`.
The live `Publish` Data and the `AppendNotice` Data must therefore be the
byte-identical map — produced by one shared constructor `mcp.DialogFeedNotice`.
This is exactly how `auto_answered` already dedupes.

### C. Frontend: render text + make items clickable

- `types/afm-event.ts`: add `dialog_question`/`dialog_answer` to the known event
  types (both the live-WS and notice-replay lists), like `auto_answered`.
- `feed-view-model.ts`: map `dialog_question` → `{actor:'agent', kind:'dialog',
  text: data.title}` and `dialog_answer` → `{actor:'user', kind:'message', text:
  data.title}`; carry `phase`/`id` and a `navigable` marker on the
  `FeedItem`/group for those two kinds. **`ask_user`/`user_answered` produce NO
  feed item** (filtered out) — the backend still emits/replays them unchanged
  (`transitionToFeedEvents` is NOT touched), the client just ignores them, so
  there is exactly one row per question/answer (the `dialog_*` one) and no double
  row. No old-backend fallback (the feature ships backend+frontend together).
- `FeedWorkspace.tsx`: a navigable item is a real `<button>` (keyboard focusable)
  calling `onOpenDialog(stageId, phase, id)`; non-navigable items unchanged.
- `App.tsx`: `onOpenDialog` selects the stage and opens its dialog view — the
  live question if `status === 'awaiting_user_input'`, else dialog history — and
  passes the target `id` down so `DialogChannel` best-effort scrolls to
  `qa-<phase>-<id>` / the pending question. Scroll is polish: a missing anchor
  still leaves the correct tab open.

### D. Markdown: block spans from markdown-it token `.map`, anchored to first line

The current `parseLineBlocks`/`nextLineBlock` hand-rolls block detection by
line-prefix scanning, which cannot correctly segment CommonMark: lazy
continuations (`- item` / `> quote` followed by an unprefixed continuation line
belong to the same block), loose lists (`- a`⏎⏎`  b` is one list), and the
asymmetry that `paragraph`⏎`- item` splits while `paragraph`⏎`2. item` stays one
paragraph. Prefix runs get these wrong.

**Use the parser's own segmentation, shared by both consumers.** A new
`blockSpans(text)`: `md.parse(text, {})` → select only tokens with `level === 0 &&
map !== null` (TOP-LEVEL only, so nested list/blockquote children are not
double-rendered); for each, slice its `.map = [start, endExclusive]` source lines,
`md.render` the slice, emit one `LineBlock{ line: start+1, html }`. This yields
correct HTML for lists/ordered/nested/loose/blockquotes/paragraphs/code/tables in
one uniform mechanism, anchored to the block's first source line.

BOTH block consumers switch to it together (or the build breaks / plan stays
broken): `parseLineBlocks` (DialogChannel question) returns `blockSpans`, and
`PlanPanel`'s `parseReviewPlan` — which today drives its own loop over
`nextLineBlock` while interleaving special-section wrapping — iterates
`blockSpans` instead, keeping its `isSpecialSection`/`isHeading2` wrapping keyed on
each span's first line. Per-line comment attachment (`PlanPanel`) and feedback
quoting (`DialogChannel`) keep working (they key on the anchored line number). The
special-section wrapping in `splitAndRender` (Assumptions / Acceptance Criteria)
is untouched. Empty/blank input returns no blocks.

## Testing

- `markdown.test.ts`: ordered list, bulleted list, nested list, blockquote,
  wrapped multi-line paragraph, a list immediately after a paragraph, code/table
  still collapse, single line unchanged — assert correct HTML AND that the block
  anchors to its first line number.
- `dialogSnippet` unit test: first-line extraction, rune-safe truncation, empty.
- server test: the `dialog_question`/`dialog_answer` notices round-trip through
  `reconstructNotices` with `{phase,id,title}` (`transitionToFeedEvents` is
  unchanged and still emits ask_user/user_answered — that's fine, the client
  drops them).
- `feed-view-model` test: renders `dialog_question`/`dialog_answer` title, marks
  navigable, carries phase/id; `ask_user`/`user_answered` yield no feed item.
- `use-event-feed` test: a live `dialog_*` event and its notice replay dedupe to
  ONE row in BOTH arrival orders (live-then-history AND history-then-live).
- `FeedWorkspace`/`App` test: clicking a navigable item calls `onOpenDialog`
  with the right stage/phase/id and opens the dialog view.
- Live verification (Docker disabled, mock interactive agent): a question and a
  user answer both show real text in the feed; clicking each opens the dialog
  tab scrolled to that Q&A; a plan/question containing an ordered list, a nested
  list, a blockquote and a wrapped paragraph renders correctly and is still
  commentable per block.

## Out of scope

No change to the FSM, `events.jsonl` schema, the dialog file protocol, or
`/api/stages/<id>/dialog`. No change to how comments are submitted (feedback.md).
Auto-answered items already carry their text and are unchanged. The new
`dialog_question`/`dialog_answer` records live in the existing `notices.jsonl`
non-FSM UI channel (same as `auto_answered`/script output/context warnings) — not
in `events.jsonl`.
