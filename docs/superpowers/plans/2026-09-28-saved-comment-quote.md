# Show quote preview on saved line-comments — Plan

> Extends the existing quote-preview feature: the quoted source line currently
> shows only while a line-comment form is OPEN (`LineCommentForm`). Once the
> comment is saved (`LineCommentDisplay`) the quote disappears, so later it's
> unclear what the comment refers to (see user screenshots). Make the saved
> display render the same quote block. Small, TDD, one component + reuse of
> existing CSS. Steps use `- [ ]`.

**Goal:** A saved line-comment shows, above its text, the same dimmed quoted
source line (big accent quotation mark) that the composer shows — so the
reader sees exactly which line the comment is anchored to. Lands in BOTH the
plan-approval panel and the dialog answer automatically (shared
`LineCommentedDocument`).

## Global Constraints
- Frontend gate = `npm run typecheck` + `npx vitest run` in
  `pkg/web/dashboard` (dashboard has **no ESLint** — don't run/expect
  `npm run lint`).
- Reuse the EXISTING quote markup/CSS (`.line-comment-quote` /
  `.line-comment-quote-src`) — no new CSS, no new tokens. Cross-skin safety is
  already handled by the existing rules.
- No data-model / backend change. The quote is DERIVED, not persisted: it is
  recomputed from the same stable `sourceLines` via the existing pure
  `quotedSourceLine(sourceLines, line)`. `sourceLines` recomputes when the
  `text` prop changes; its stability is guaranteed by the CURRENT parents,
  which `key` the mount by document identity (a text change remounts and
  resets all comments). This dependency lives in the parents, not in this
  component — a future consumer that mounts without such a `key` could keep a
  stale comment and quote it against new text. (codex review, MINOR — noted,
  not a change: both current consumers key correctly.)
- Commit messages Russian; no `Co-Authored-By`.

## Key facts (verified)
- `LineCommentedDocument.tsx`:
  - `quotedSourceLine(sourceLines, line)` (line ~24) — exported pure fn:
    1-based `line`, returns `sourceLines[line-1].trim()`, or `''` for
    blank / horizontal-rule lines.
  - `LineCommentDisplay` (line ~312) — saved comment. Props today:
    `{ line, text, onRemove }`. Renders header (`Comment on line N` + ✕) then
    the text. **No quote.**
  - `LineCommentForm` (line ~334) — composer. Already receives
    `quotedLine: string` and renders the quote block (lines ~376-380) when
    non-empty.
  - Call site `renderBlock` (line ~230): renders `LineCommentDisplay` per
    commented line; `sourceLines` is already in scope (used at line ~239 for
    the form's `quotedLine`).
- The quote block that must be reused (already in `LineCommentForm`):
  ```tsx
  {quotedLine !== '' && (
    <div className="line-comment-quote">
      <span className="line-comment-quote-src">{quotedLine}</span>
    </div>
  )}
  ```

---

## Task 1: Render quote on the saved display (TDD)

**Files:** `pkg/web/dashboard/src/components/plan-panel/LineCommentedDocument.tsx`;
test `LineCommentedDocument.test.tsx`.

**Step 1 — RED: write failing tests.**
- [ ] A saved comment on a non-empty source line renders the quoted source
      line (assert `.line-comment-quote-src` text == trimmed source line) in
      the SAME container as the display (`.line-comment-display`), above the
      comment text.
- [ ] **Correct line, not active line (codex MINOR):** two saved comments on
      different lines of the same block, with the form open on a THIRD line —
      assert each saved card quotes ITS OWN source line and the form quotes
      its own. Guards against confusing `line` with `activeCommentLine`.
- [ ] A saved comment on a horizontal-rule line renders NO quote block
      (`.line-comment-quote` absent) — mirrors composer via `quotedSourceLine`
      returning `''`. (Blank-line saved comments are unreachable in the UI —
      blank lines get no anchor — so the blank case stays covered only by the
      existing `quotedSourceLine` unit test, not a display test. codex MINOR.)
- [ ] **Editing shows one quote, not two (codex MINOR):** open an existing
      saved comment for editing (activate its line). Assert the block renders
      exactly one `.line-comment-quote` for that line (the form's), and the
      stale display card for the active line is NOT rendered.
- [ ] Regression: the open composer form still shows its quote (unchanged).
- [ ] Run `npx vitest run LineCommentedDocument` — confirm the new tests FAIL
      for the right reason, others pass.

**Step 2 — GREEN: implement.**
- [ ] Add `quotedLine: string` to `LineCommentDisplay`'s props.
- [ ] Render the existing quote block (`.line-comment-quote` /
      `.line-comment-quote-src`) between `comment-display-header` and the text
      `<div>`, guarded by `quotedLine !== ''`. Reuse the EXACT markup the form
      uses (a11y parity — no new semantics introduced vs the shipped composer
      quote; codex MINOR accepted as parity, out of scope to change both).
- [ ] At the call site (line ~230) pass
      `quotedLine={quotedSourceLine(sourceLines, line)}`.
- [ ] **Exclude the active line from `displays`** so editing an existing
      comment shows only the form (with its quote), not a stale display card
      too: `commentedLinesIn(block).filter((line) => line !== activeCommentLine)`
      (or filter inline at the `displays.map`). This removes the pre-existing
      card+form redundancy during edit and prevents the double quote.
- [ ] `npm run typecheck` clean; `npx vitest run` all green.

**Step 3 — REFACTOR (optional).**
- [ ] If the quote-block JSX is now duplicated verbatim in `LineCommentForm`
      and `LineCommentDisplay`, extract a tiny local `QuoteBlock({ quotedLine })`
      component and use it in both. Only if it reads cleaner; do not
      over-abstract. Keep tests green.

## Verification
- [ ] `npm run typecheck` + `npx vitest run` green.
- [ ] Live run: open dashboard, add a line-comment in the plan panel AND in a
      dialog answer, confirm the quote persists on the saved comment; edit an
      existing comment and confirm only one quote shows (form only, no stale
      card).

## Out of scope
- Feed line-comments (`FeedComposer`) — separate path, not touched.
- Any persistence / backend / event-schema change.
- Restyling the quote block.
