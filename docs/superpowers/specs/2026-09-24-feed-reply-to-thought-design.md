# Reply to agent thought — quote a feed thought into the composer (branch `feed-reply-to-thought`)

## Problem

The feed renders each agent "thought" (`agent_action` with `tool="text"`) as a
white prose line — the messenger-styled narrative of what the agent is doing
(see the screenshot: the highlighted white lines between the grey tool/bash
lines). The only way to give feedback to a live agent today is the bottom
`FeedComposer`, which sends **free-text** with no anchor to any particular
thought. When the agent has emitted several thoughts, free-text feedback forces
the user to re-describe which thought they mean.

We want the user to click a specific thought and reply to it: the thought's text
appears as a **quote chip** above the composer input (the same blockquote
treatment used by the markdown line-comments), the user types a comment in the
same input, and sending delivers **the quoted thought + the comment** to the
live agent through the existing Revise path. This makes feedback precise —
"about *this* thought, do X" — without any new backend or transport.

## Non-goals (YAGNI)

- **No backend / no new HTTP endpoint / no new FSM state.** Delivery reuses
  `POST /api/stages/{id}/revise` (`reviseStage`) exactly as `FeedComposer` does
  today. The quote is assembled client-side and prepended to the feedback body.
- **No new event type and no `notices.jsonl` write.** The reply is a Revise like
  any other; it already surfaces in the feed via the existing
  `stage_status_changed` line. We do not invent a "replied-to-thought" event.
- **No reuse of the F2 line-comments machinery** (`LineCommentedDocument` /
  `use-line-comments` / anchored markdown). That system batches many comments
  over one persistent markdown document with `data-line` anchors and roving
  tabindex; the feed is a live-appending stream of many independent items across
  groups — wiring it in is heavy and a poor fit. We reuse only its **visual
  convention** (the `.line-comment-quote` blockquote look), not its logic.
- **No multi-thought batching.** One reply targets one thought at a time
  (clicking another thought replaces the target). A user who wants to reference
  several thoughts sends several replies.
- **No persisted reply target across reloads / stage switches.** Switching the
  selected stage clears the draft (already true) and the reply target.
- **No clickability for non-thought rows** — tool/bash/script/status/dialog
  lines are not repliable (delivery would still be free-text; the anchor only
  makes sense for the agent's own prose).

## Decisions (user-confirmed)

1. **Only white agent thoughts are clickable.** A row is a "thought" iff its
   `FeedItem` has `kind === 'message' && markdown === true` (the only mapping
   that sets both is `agent_action tool="text"` → see `feed-view-model.ts`).
2. **The quote is delivered to the agent.** The Revise body is the thought text
   as a Markdown blockquote (every line prefixed `> `), a blank line, then the
   user's comment:
   ```
   > <thought line 1>
   > <thought line 2>

   <user comment>
   ```
3. **The quote chip lives above the composer input**, styled like the markdown
   line-comment quote (left accent bar + muted source text), with an `✕` at the
   end to cancel the reply. Clicking another thought replaces the chip's target.
4. **Attach (paperclip) moves inline into the input row — in both states.**
   Today `PasteableTextarea` renders its Attach control on a strip **above** the
   textarea. In the feed composer the attach affordance must sit **inline in the
   input row** next to the textarea and the ✈ button, whether or not a thought is
   selected (matches the approved mockup). This is scoped to `FeedComposer`; it
   must not regress the strip layout for the other `PasteableTextarea` consumers
   (plan line-comments, dialog custom answer, `AgentNoteModal`).
5. **Live-agent gate is inherited, not re-implemented.** Thoughts are only
   clickable where the composer is present — i.e. per-stage Feed with
   `noteTarget != null` (`running` && not a script). In the Full-feed view (no
   composer) thoughts are plain, non-clickable. No new gating logic: if the
   composer isn't rendered, thoughts aren't repliable.

## Architecture

### State ownership — `FeedWorkspace`

`FeedWorkspace` already owns the feed render and conditionally renders
`FeedComposer`. It gains one piece of state:

```ts
const [replyTo, setReplyTo] = useState<{ key: string; text: string } | null>(null)
```

- `key` = the target `FeedItem.key` (stable, unique) — used to mark the row
  `selected` and to detect "clicked the same one again".
- `text` = the thought's raw text (`item.text`, the Markdown source), used to
  build both the chip preview and the delivered quote.

Reset points (all one-liners):
- Composer key already changes on `noteTarget` change (stage switch) → the
  `FeedComposer` remounts. `replyTo` is cleared in the same effect that keys the
  composer, so a target never leaks across stages.
- On successful send (see delivery) → clear `replyTo`.
- On `✕` in the chip → clear `replyTo`.

### Making a thought clickable

`FeedGroupView` maps `group.items`. For an item that is a thought
(`kind === 'message' && markdown === true`) **and** replying is enabled
(a new `onReplyToThought?: (key, text) => void` prop is present — passed only by
the per-stage Feed instance that also passes `onSendNote`), the item's rendered
container becomes clickable:

- Add `onClick={() => onReplyToThought(item.key, item.text)}`, `role="button"`,
  `tabIndex=0`, and Enter/Space keyboard activation (a11y).
- Add class `feed-item-thought` (hover affordance + cursor) and, when
  `item.key === replyToKey`, `is-reply-target` (selected background + accent).
- The existing image-marker segmentation (`splitImageMarkers`) is unchanged;
  the click target is the item container, images inside still render.

This does not conflict with the existing `navigable` branch (dialog rows) —
dialog rows are `kind === 'dialog'`/`'message'` **without** `markdown` and are
already rendered as `<button>` for `onOpenDialog`; thoughts are a disjoint set
(`markdown === true`). A thought is never navigable.

`replyToKey` (the currently-selected key, or `null`) is threaded from
`FeedWorkspace` → `FeedGroupView` so exactly one row shows `is-reply-target`.

### Chip + inline attach — `FeedComposer`

`FeedComposer` gains two props: `replyQuote?: string | null` (the target
thought's text, or null) and `onCancelReply?: () => void`.

- When `replyQuote` is non-null, render the quote chip above the input:
  a `.feed-reply-chip` with a small "↩ In reply to" label, the quoted text
  clamped to 2 lines (`-webkit-line-clamp`), and an `✕` button calling
  `onCancelReply`. Reuses the `--quote-bar` / blockquote look from the
  line-comment CSS via shared tokens.
- **Inline attach:** the input row is laid out as `[attach] [textarea] [✈]`.
  Because `PasteableTextarea` owns the Attach control (and its strip layout),
  we add an opt-in **`attachInline?: boolean`** prop to `PasteableTextarea`
  that renders the Attach button inline (before/adjacent to the textarea in the
  same row) instead of on the top strip. Default `false` preserves today's strip
  layout for every existing consumer; `FeedComposer` passes `attachInline`.
  Image-attachment thumbnails (paste/upload previews) still render on the strip
  above — only the Attach **button** relocates; a queued image preview is not
  part of the single input row. (This keeps the change small and avoids
  reflowing the preview strip.)

### Delivery (reuse Revise, prepend the quote)

On submit, `FeedComposer` builds the body:

```ts
function buildReplyBody(quote: string | null, comment: string): string {
  const c = comment.trim()
  if (quote === null) return c
  const q = quote.split('\n').map((l) => `> ${l}`).join('\n')
  return `${q}\n\n${c}`
}
```

then calls the existing `onSend(body)` → `handleSendNote` → `reviseStage(id, body)`.
No change to `App.handleSendNote` or `run-client.ts`. Empty comment is still a
no-op (send disabled), regardless of whether a thought is selected. On the
promise resolving (delivered), the composer clears its text **and** signals the
parent to clear `replyTo` (via `onCancelReply()` on success, or a dedicated
`onSent` — see Decisions/impl note); on reject, both the text and the reply
target are preserved so the user can retry.

## Data flow (end to end)

1. User clicks a white thought in the per-stage Feed → `FeedGroupView` calls
   `onReplyToThought(key, text)` → `FeedWorkspace` sets `replyTo`.
2. `FeedComposer` shows the quote chip; textarea focuses.
3. User types a comment, presses ⌘/Ctrl+↵ or clicks ✈.
4. `buildReplyBody` prepends `> quote\n\n` → `onSend(body)` →
   `reviseStage` → `POST /revise` → `Orchestrator.Revise` → `feedback.md`
   (graceful-pause + resume, exactly as a plain note today).
5. On success, text + `replyTo` clear. The feed shows the usual
   `stage_status_changed` line for the revise; the delivered body (quote +
   comment) is what the agent reads from `feedback.md`.

## Error handling

- **Send fails (409 stage no longer running / network):** `onSend` rejects →
  `FeedComposer` keeps the draft text and the reply chip (no clear). Same
  behavior as the existing composer, extended to also preserve the chip.
- **Thought text empty / whitespace-only:** cannot occur for a rendered thought
  (`agent_action tool="text"` with empty detail produces an empty message item
  that is still keyable); defensively, `buildReplyBody` with an all-whitespace
  quote still prefixes `> ` lines — harmless. No special-casing.
- **Reply target row scrolls out of view / feed re-renders:** `replyTo` is held
  by key/text in `FeedWorkspace`, independent of DOM presence — the chip and the
  delivered quote survive feed appends. If the target item disappears entirely
  (it won't for replayed events), the chip still holds the captured text; send
  still works.
- **Stage leaves `running` while composing:** `noteTarget` becomes null →
  composer unmounts (existing behavior) → `replyTo` cleared by the same effect.

## Testing

Frontend only (vitest + RTL); no Go tests (no backend change).

- `feed-view-model` — (already covered) confirm `agent_action tool="text"` →
  `kind:'message', markdown:true`; a guard test that tool/bash rows are
  **not** `markdown` (so the "is a thought" predicate is exact).
- `FeedWorkspace`:
  - clicking a thought row calls `onReplyToThought` with its key+text; a
    tool row does not; only present when `onReplyToThought`/composer enabled.
  - exactly one row gets `is-reply-target`; clicking another moves it.
  - Full-feed instance (no composer) renders thoughts non-clickable.
- `FeedComposer`:
  - `buildReplyBody` — quote prepended as `> `-blockquote + blank line +
    comment; no quote → plain comment; multi-line quote → each line prefixed.
  - chip renders when `replyQuote` set, `✕` calls `onCancelReply`; hidden when
    null.
  - send disabled on empty comment even with a quote; on resolve clears text +
    reply; on reject preserves both.
  - `attachInline` prop: attach button in the input row; default strip layout
    unchanged.
- `PasteableTextarea`:
  - `attachInline` renders the Attach button inline; default renders it on the
    strip (regression guard for other consumers).

## Files touched (all frontend)

- `components/feed-workspace/FeedWorkspace.tsx` — `replyTo` state, thread
  `onReplyToThought`/`replyToKey`, clickable thought rows, pass chip props to
  `FeedComposer`.
- `components/feed-workspace/FeedComposer.tsx` — quote chip, `buildReplyBody`,
  inline attach, clear-on-success.
- `components/pasteable-textarea/PasteableTextarea.tsx` — opt-in `attachInline`.
- CSS (feed skin) — `.feed-item-thought` hover/cursor, `.is-reply-target`,
  `.feed-reply-chip` (+ reuse quote tokens); inline-attach layout.
- Tests as above. Rebuild + commit the embedded dashboard bundle.

## Verification gate

`vitest` (dashboard) + `tsc --noEmit` green; `make lint` 0 on darwin **and**
linux; embedded bundle rebuilt and committed; then a live run in the dashboard
(reply to a real running agent's thought; confirm `feedback.md` receives the
`> quote` + comment and the agent acts on it).
