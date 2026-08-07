# Dashboard: line-comment closing, Approve gating, dialog scroll — design

Date: 2026-08-07

## Context

Three independent UX bugs/annoyances reported in the afm dashboard
(`pkg/web/dashboard`), all in the plan review / interactive dialog panels:

1. Typing a line comment on a table row, then drag-selecting text in the
   same table to copy it, wipes the in-progress comment.
2. On the plan review panel, `Approve` stays clickable while the user has
   unsent line comments — nothing stops an accidental approve that silently
   discards the drafted feedback.
3. The dialog/communication channel sometimes renders its history scrolled
   to the top (oldest message) instead of the bottom (latest), after a
   render/refresh.

All three are scoped to `pkg/web/dashboard/src`.

## 1. Line-comment form: close only via ×, never via outside click

**Current behavior.** `PlanPanel.tsx` and `DialogChannel.tsx` each implement
the same pattern: a table/question line is a `<div className="plan-line"
onClick={() => handleLineClick(item.line)}>`; clicking it opens an inline
`.line-comment-form` with a textarea. There is no real click-outside
listener — `handleLineClick` just runs on every click that bubbles up from
the row, including:

- Re-clicking the same row (currently toggles the form closed).
- Clicking a different row (currently switches `activeCommentLine` to it,
  closing the first form).
- The native `click` fired on mouseup when a text-selection drag inside
  `.line-content` ends on that row — indistinguishable, at the DOM level,
  from an intentional click.

Any of these silently discards the draft in `draft` state.

**New behavior.**

- The comment form gets a `×` close button in its header, visually
  consistent with the existing saved-comment display's
  `comment-display-header` / `comment-remove` (✕) button. It replaces the
  current `Cancel` text button.
- `handleLineClick(line)`:
  - If no form is open (`activeCommentLine === null`), unchanged: opens a
    form on `line`.
  - If a form is open and its `draft` is empty, unchanged: same-row click
    toggles closed, different-row click switches to the new line.
  - If a form is open and its `draft` is **non-empty**, any row click
    (same row or a different row) is a no-op — the open form does not
    close and does not switch lines. This is what makes the
    text-selection case safe: the click that ends a selection drag is
    just another row click, now ignored.
- The `×` button always calls `setActiveCommentLine(null)` (and clears
  `draft`) unconditionally, discarding the text with no confirmation
  dialog — this is the only way to close a form that has unsent text.
- Applied identically in both `PlanPanel.tsx` and `DialogChannel.tsx`
  (`renderPlanLine` / `renderQuestionLine`), since both duplicate the exact
  same comment-form pattern today.

Out of scope: supporting multiple simultaneously-open draft comments on
different lines. One active draft at a time, as today.

## 2. Plan review: Approve disabled while there are draft comments

**Current behavior** (`PlanPanel.tsx`): `Approve`'s `disabled` prop is
`busy` only. `Send revision`'s `disabled` is `busy || commentCount === 0`.
A user can click `Approve` while line comments are pending — they're
silently dropped (never sent anywhere except via `sendRevision`).

**New behavior:** `Approve`'s `disabled` becomes `busy || commentCount >
0`. As soon as `comments` has at least one entry, `Approve` grays out and
only `Send revision` is actionable. Deleting all comments (`commentCount`
back to `0`) re-enables `Approve` immediately (no page reload needed —
already implied by existing reactive state). No change to `Send revision`.

Scoped to `PlanPanel.tsx` only. `DialogChannel.tsx` has no separate
Approve action to gate — it already swaps its one send button between
"answer" and "feedback" mode based on `commentCount`.

## 3. Dialog history: preserve/restore bottom-anchored scroll

**Current behavior.** `useStickToBottom` (shared hook, also used by
`EventFeedPanel`) tracks a scroll container via a `useRef` object and a
`useEffect(..., [])` (mount-once) that attaches a `scroll` listener and a
`MutationObserver`. `DialogChannel.tsx` conditionally skips rendering the
`#dialog-scroll` container entirely (`if (!hasContent) return <></>`,
*after* the hook has already run) for stages that start with no dialog
entries. When entries later arrive via the 2-second poll and the container
finally mounts, the hook's effect has already run once (with a `null` ref)
and never reruns — no listener, no observer, no scroll tracking. The list
sits at native `scrollTop = 0`.

Separately, even when attachment isn't an issue, the hook only reacts to
*future* DOM mutations — it has no "jump to bottom once, on attach" step
for content that's already present at the moment the container mounts.
`EventFeedPanel` avoids hitting this because its container is unconditionally
rendered from the start (always empty at first attach, then grows
incrementally, so every mutation is observed).

**New behavior — fix in `use-stick-to-bottom.ts` only:**

- Replace the `useRef` + mount-once effect with a callback ref backed by
  `useState<T | null>`: `const [node, setNode] = useState<T | null>(null)`,
  returned as the hook's `ref`. The tracking `useEffect` depends on
  `[node]` instead of `[]`, so it (re)attaches correctly regardless of
  when the consumer's JSX actually mounts the container — including the
  late-mount case in `DialogChannel`.
- When the effect (re)attaches to a non-null `node` and `stick` is `true`
  (the default), immediately set `node.scrollTop = node.scrollHeight`
  once, before wiring up the listener/observer — so a dialog whose history
  is already fully loaded at mount time opens at the bottom, not the top.
- The existing "don't disturb scroll position if the user has scrolled up"
  behavior (the `stick` flag, updated on the container's native `scroll`
  event) is unchanged — poll-driven refreshes that call `setEntries(...)`
  keep behaving like today: auto-stick to bottom only while the user is
  already near the bottom.

No changes needed to `DialogChannel.tsx` or `EventFeedPanel.tsx` JSX —
both already pass `ref={feed.ref}` directly as a React `ref` prop, which
accepts a callback ref exactly like it accepts a ref object. The existing
`use-stick-to-bottom.test.ts` unit test needs updating for the new
(callback-based) ref shape.

## Testing

- Unit tests for `PlanPanel`/`DialogChannel` comment-form close behavior:
  non-empty draft + row click (same/different) → form stays open; `×` →
  form closes and draft is discarded.
- Unit test for `PlanPanel`: `Approve` disabled state toggles with
  `commentCount`.
- Update `use-stick-to-bottom.test.ts` for the callback-ref API; add a
  case covering late attachment (node arrives after first render) and
  initial jump-to-bottom when content is already present at attach time.
