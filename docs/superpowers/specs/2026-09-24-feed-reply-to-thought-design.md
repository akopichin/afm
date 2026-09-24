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

### State ownership — `FeedWorkspace` (stage-scoped, race-safe)

`FeedWorkspace` already owns the feed render and conditionally renders
`FeedComposer`. It gains one piece of state, **scoped to the target stage** to
close codex BLOCKER #1/#8 (a bare `{key,text}` leaks across stage switches for
one render and lets a stale async send clear a freshly-picked target):

```ts
const [replyTo, setReplyTo] = useState<{ stageId: string; key: string; text: string } | null>(null)
// The chip/selection are only ever shown for the stage the composer is on.
// Derived, so a mid-flight stage switch can never paint the old quote:
const activeReply = replyTo !== null && replyTo.stageId === noteTarget ? replyTo : null
```

- `stageId` = the stage this reply targets (== `noteTarget` at pick time).
- `key` = the target `FeedItem.key` (stable, unique) — marks the selected row,
  detects "clicked the same one again", and gates race-safe clears.
- `text` = the thought's raw text (`item.text`, the Markdown source) — builds
  both the chip preview and the delivered quote.

Reset points:
- **Stage switch:** `useEffect(() => setReplyTo(null), [noteTarget])`. Even
  before the effect runs, `activeReply` is `null` for the new stage (the
  `stageId` guard), so no stale-quote frame is possible. The `FeedComposer` also
  remounts (its `key={noteTarget}`), clearing the draft as today.
- **On `✕` in the chip** → `setReplyTo(null)`.
- **On successful send (race-safe, codex SHOULD #2):** the composer reports the
  key it sent via `onSent(sentKey)`; `FeedWorkspace` clears only the matching
  target: `setReplyTo((cur) => (cur?.key === sentKey ? null : cur))`. If the user
  picked a different thought while the send was in flight, the new target
  survives. (The draft text is already cleared race-safely inside `FeedComposer`
  via the existing `cur === submitted` snapshot check.)

### Making a thought repliable — dedicated button, no nested interactive (codex BLOCKER #6)

Agent thought Markdown is rendered with `renderPlainMarkdown` (**`linkify: true`**),
so a thought can contain real `<a>` elements. Wrapping the row in a
`role="button"` container would nest interactive controls (invalid DOM) and make
link clicks/focus also fire thought-selection. Instead:

- The prose container stays **non-interactive** (may contain `<a>`). No
  `role="button"`, no `tabIndex` on it.
- Selection is driven by a **dedicated real `<button class="feed-thought-reply">`**
  — the hover/focus affordance ("↩ reply") rendered as a sibling next to the
  prose. Native button semantics give correct Enter/Space activation, focus ring,
  and AT labeling for free (closes codex SHOULD #7 — no hand-rolled keydown, no
  Space-scroll). It is visually revealed on row hover / keyboard focus-within and
  is always focusable via Tab.
- **Convenience row click:** the item container also gets an `onClick` that calls
  `onReplyToThought(item.key, item.text)` — but guarded: if
  `event.target.closest('a')` is non-null the click is a link, so we return
  without selecting (link works normally). This is a plain `div` handler, not a
  button role, so no nested-interactive violation.
- Class `feed-item-thought` (hover affordance + cursor) and, when
  `item.key === activeReplyKey`, `is-reply-target` (selected background + accent).
- `splitImageMarkers` segmentation is unchanged; images inside still render.

Repliable rendering happens only when a new `onReplyToThought?: (key, text) => void`
prop is present — passed only by the per-stage Feed instance that also passes
`onSendNote`. In Full-feed (no composer) thoughts render exactly as today
(non-interactive prose).

This does not conflict with the existing `navigable` branch (dialog rows):
dialog rows are `message`/`dialog` **without** `markdown` and are already a
`<button>` for `onOpenDialog`; thoughts are the disjoint `markdown === true` set
and are never navigable.

`activeReplyKey` (`activeReply?.key ?? null`) is threaded from `FeedWorkspace` →
`FeedGroupView` so exactly one row shows `is-reply-target`.

### Chip + inline attach — `FeedComposer`

`FeedComposer` gains props: `replyQuote?: string | null` (the active target's
text, or null — derived by the parent from `activeReply`), `replyKey?: string`
(the target key, passed back on send), `onCancelReply?: () => void` (the `✕`),
and `onSent?: (key: string) => void` (race-safe success signal).

- When `replyQuote` is non-null, render the quote chip above the input:
  a `.feed-reply-chip` with a small "↩ In reply to" label, the quoted text
  clamped to 2 lines (`-webkit-line-clamp`), and an `✕` button calling
  `onCancelReply`. Reuses the `--quote-bar` / blockquote look from the
  line-comment CSS via shared tokens.
- **Inline attach (codex SHOULD #4):** the input row is laid out as
  `[attach] [textarea] [✈]`. Because `PasteableTextarea` owns the Attach control
  and its strip, we add an opt-in **`attachInline?: boolean`** prop.
  - **Default `false` is a pure no-op for existing consumers:** the current
    `showStrip = attachments.length > 0 || showAttachButton` strip (attach button
    + any queued image previews) renders exactly as today — same DOM, same
    classes. The new inline branch is additive and gated on `attachInline === true`;
    CSS for it is under a new scoping class only, so it cannot reposition the
    default strip. Regression guard test asserts the default DOM is unchanged.
  - **When `attachInline === true`:** the Attach **button** renders inline in the
    input row (adjacent to the textarea). **Queued image previews still render on
    the strip above** the input row — only the button relocates; a preview
    thumbnail is not part of the single input row (keeps the change small, avoids
    reflowing the preview strip). So with `attachInline`, `showStrip` reduces to
    "previews only" (`attachments.length > 0`), and the button lives in the row.
  - Tests must cover, for `attachInline`: file-reference insertion at caret,
    image upload via the hidden input, caret/focus preserved, and simultaneous
    queued previews rendering above while the button stays in the row.

### Delivery (reuse Revise, prepend the quote)

On submit, `FeedComposer` builds the body (codex SHOULD #5 — normalize line
endings before prefixing so a CRLF thought doesn't yield `> line\r`):

```ts
function buildReplyBody(quote: string | null, comment: string): string {
  const c = comment.trim()
  if (quote === null) return c
  const q = quote.replace(/\r\n?/g, '\n').split('\n').map((l) => `> ${l}`).join('\n')
  return `${q}\n\n${c}`
}
```

- Every line of the thought is prefixed with `> ` — including blank lines
  (`> `) and lines that are themselves blockquotes/fences. This is intentionally
  naive: it is always **safe** (feedback.md is read as text by the agent), and
  nesting a `> ` in front of an existing `> ` or a ```` ``` ```` fence produces a
  valid, readable nested quote. Test cases cover: single line, multi-line,
  embedded blockquote (`> x` → `> > x`), and fenced code block.
- Empty comment is a no-op (send disabled) regardless of a selected thought.

The body goes to the existing `onSend(body)` → `handleSendNote` →
`reviseStage(id, body)`. No change to `App.handleSendNote` or `run-client.ts`.

Success/failure handling (race-safe):
- On resolve: clear the text via the existing `cur === submitted` snapshot check,
  then call `onSent(sentKey)` so the parent clears `replyTo` **only if it still
  points at the same key** (see State ownership). If the user selected a
  different thought mid-send, that new target and its draft survive.
- On reject (409 / network): keep both the draft text and the reply chip so the
  user can retry.

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
  - clicking a thought (row or its `↩` reply button) calls `onReplyToThought`
    with key+text; a tool row does not; repliable only when `onReplyToThought`
    present.
  - **link guard:** clicking an `<a>` inside a thought does **not** select
    (`closest('a')` guard); the dedicated `↩` `<button>` activates via
    Enter/Space (native) and does not scroll on Space.
  - exactly one row gets `is-reply-target`; clicking another moves it.
  - **stage-scope:** `activeReply` derives null when `replyTo.stageId !== noteTarget`
    (no stale quote on stage switch); `onSent(key)` clears only the matching key.
  - Full-feed instance (no composer) renders thoughts non-repliable.
- `FeedComposer`:
  - `buildReplyBody` — no quote → plain trimmed comment; single/multi-line quote
    → each line `> `-prefixed + blank line + comment; **CRLF** normalized (no
    `\r`); embedded blockquote → `> > x`; fenced code block preserved.
  - chip renders when `replyQuote` set, `✕` calls `onCancelReply`; hidden when
    null.
  - send disabled on empty comment even with a quote; on resolve clears text and
    calls `onSent(sentKey)`; on reject preserves text (chip preserved by parent).
  - `attachInline` prop: attach button in the input row; previews still above;
    default strip layout unchanged.
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
