# Auto-growing textareas with manual-resize override — design

Date: 2026-08-07

## Context

Three textareas in the afm dashboard (`pkg/web/dashboard`) currently only
support manual drag-resize (`resize: vertical`), with a small fixed
`min-height` and no `max-height`:

- `.line-comment-form textarea` — line-comment input, used identically in
  both `PlanPanel.tsx` and `DialogChannel.tsx` (`min-height: 56px`).
- `.dialog-pending .dialog-custom` — the free-text answer box in
  `DialogChannel.tsx` ("Or type your own answer…") (`min-height: 60px`).
- `.agent-note-textarea` — the note field in `AgentNoteModal.tsx` (no
  `min-height` currently).

Typing a long comment/answer/note is awkward: the box stays whatever size
it started at, forcing the user to manually drag it taller (or scroll
inside a tiny box) as they type.

## Requirements

1. Each textarea keeps a minimum height so it never collapses smaller than
   today even when empty (same values as today: 56px / 60px / a new ~60px
   floor for `agent-note-textarea`, which has none today).
2. As the user types past that floor, the textarea grows to fit the
   content, up to a shared `max-height: 400px`. Beyond that, it stops
   growing and gets an internal scrollbar instead.
3. Growing must not visually disturb the caret — no jump, no flash, no
   momentary collapse-then-regrow the user can see.
4. The existing manual resize handle (`resize: vertical`) stays. If the
   user manually drags the textarea to a new height (e.g. because they hit
   the 400px cap and want more room), auto-grow stops adjusting that
   textarea's height from then on — the user's manual choice is
   authoritative until the field goes back to empty (closing/reopening a
   comment form, a new question replacing an old one, submitting an
   answer), at which point auto-grow resumes for the next use.

## Rejected approaches

- **`react-textarea-autosize` (npm package):** handles auto-grow and the
  no-jump requirement well, but it fully owns the element's height and
  does not support running alongside a real native resize handle — its own
  docs call combining it with manual resize unsupported. Fails requirement 4.
- **CSS-only "grid mirror" trick** (a hidden sibling `<div>` replicating the
  text, sized via CSS grid so the textarea's height tracks it) or the
  native `field-sizing: content` property: both hand the element's height
  entirely to layout, leaving no room for a user-driven override, and
  `field-sizing` also isn't supported across all browsers this dashboard
  targets. Fails requirement 4; `field-sizing` also fails on support.

Both are the "textbook" answers to auto-grow in isolation, but neither
composes with "let the user override by dragging," which is now a hard
requirement — so a small hand-rolled hook is the right size of solution
here, not a shortcut.

## Design

**One shared hook**, `useAutoGrowTextarea(value: string, maxHeightPx: number): RefObject<HTMLTextAreaElement>`,
added at `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/`, mirroring
the existing `use-stick-to-bottom` hook's shape and location convention.

- **Growing:** a `useLayoutEffect` keyed on `value` sets
  `el.style.height = 'auto'` then immediately
  `el.style.height = Math.min(el.scrollHeight, maxHeightPx) + 'px'`.
  `useLayoutEffect` runs synchronously after DOM mutations but before the
  browser paints, so the shrink-to-`auto`-then-grow-to-`scrollHeight` pair
  never produces a visible intermediate frame — this is what satisfies
  requirement 3. Because `value` changes on every keystroke (all three
  textareas are already React-controlled), this single effect covers both
  "grow as you type" and "size correctly the moment a pre-filled value
  loads" (e.g. clicking "Update" on an existing long comment) — no separate
  mount-time codepath needed.
- **Floor:** left entirely to CSS `min-height` (unchanged per textarea) —
  `min-height` clamps the browser's used height regardless of what JS sets,
  so no JS-side clamping against the minimum is needed.
- **Ceiling:** the JS clamp (`Math.min(scrollHeight, maxHeightPx)`) stops
  growth at 400px; CSS also sets `max-height: 400px` on each textarea as a
  defensive backstop (belt-and-braces if the effect hasn't run yet), plus
  `overflow-y: auto` so a scrollbar appears only once content actually
  exceeds the cap.
- **Manual-resize lock:** a `ResizeObserver` (set up once per textarea, via
  a plain `useEffect`) watches the element's rendered height. The hook
  tracks "the height we last set programmatically" in a ref; whenever the
  observed height doesn't match that ref's value, the change didn't come
  from the hook itself — i.e., the user dragged the resize handle — and a
  second `locked` ref flips to `true`. The growth effect checks `locked`
  first and returns immediately if set, leaving the user's manual height
  alone from then on.
- **Lock reset:** at the top of the growth effect, if `value === ''`, reset
  `locked.current = false` before anything else. This means the lock only
  ever applies to the textarea's current "session" of content — clearing a
  comment form, submitting an answer, or a fresh question replacing an old
  one all naturally return the textarea to auto-grow for next time, without
  any extra bookkeeping.

**Wiring:** each of the three call sites passes its own current value and
`400` as `maxHeightPx`, and spreads the returned ref onto its `<textarea>`
alongside its existing `value`/`onChange` props — no change to how any of
the three manage their controlled value today.

**CSS changes** (all three existing rules, no new selectors):
- `.line-comment-form textarea` (`plan-panel.css`): add `max-height: 400px; overflow-y: auto;`. `resize: vertical` and `min-height: 56px` unchanged.
- `.dialog-pending .dialog-custom` (`dialog-channel.css`): add `max-height: 400px; overflow-y: auto;`. `resize: vertical` and `min-height: 60px` unchanged.
- `.agent-note-textarea` (`agent-note-modal.css`): add `min-height: 60px; max-height: 400px; overflow-y: auto;`. `resize: vertical` unchanged (already present).

## Testing

- Hook unit tests (jsdom): growth effect sets `style.height` based on a
  mocked `scrollHeight`, clamped at `maxHeightPx`; a simulated
  `ResizeObserver` callback with a mismatched height sets the lock, after
  which a `value` change no longer touches `style.height`; setting `value`
  back to `''` clears the lock and growth resumes.
- No new component-level tests needed in `PlanPanel.test.tsx` /
  `DialogChannel.test.tsx` / `AgentNoteModal.test.tsx` beyond confirming the
  hook is wired in (existing tests don't assert on textarea height today
  and jsdom's `scrollHeight` is always 0, so behavior is fully covered at
  the hook level, matching how `use-stick-to-bottom` is tested).
- Manual verification in a real browser (jsdom can't do real layout):
  type past the min-height floor and confirm smooth growth with no visible
  caret jump; keep typing past 400px and confirm an internal scrollbar
  appears instead of further growth; drag the resize handle taller, then
  keep typing, and confirm the manual height sticks (no snap-back);
  clear the field and confirm auto-grow resumes on the next use.
