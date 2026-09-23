# Line-comment quote preview + softer active-line highlight — Plan

> Small UI polish on the shared `LineCommentedDocument` (used by BOTH the plan-approval panel and the dialog answer). TDD for the quote; CSS-only for the highlight. Steps use `- [ ]`.

**Goal (two approved visuals):**
1. **Quote preview** — when a line-comment form is open, show a dimmed, quoted preview of the exact source line you're commenting on, just above the textarea, with a **big accent quotation mark** (messenger-style, Variant 1, faint ~0.30 glyph).
2. **Softer active-line highlight (Option A)** — replace the current 1px accent "frame" (`box-shadow: … , 0 0 0 1px var(--accent-primary)`) on the selected line with a soft accent fill + a thick left accent bar, no outline.

Both land in the plan panel AND the dialog automatically (shared component/CSS).

## Global Constraints
- Frontend gate = `npm run typecheck` + `npx vitest run` (dashboard has **no ESLint** — don't run/expect `npm run lint`).
- **Cross-skin safety:** use theme tokens, NOT hardcoded hex or `--accent-primary-rgb`. `--accent-primary-rgb` defaults to blue (95,120,255) in `base/tokens.css` while `--accent-primary` bridges to the skin's amber on coffee/goga/novacorps — they MISMATCH. Derive translucent accent fills with `color-mix(in srgb, var(--accent-primary) N%, transparent)` so glyph, bar, and fill always agree per skin (graphite/coffee/goga/novacorps × light/dark). (`color-mix` is already used in `plan-panel.css` — with `currentColor`, per codex; the `var(--accent-primary)` form is valid, supported CSS.)
- Commit messages Russian; no `Co-Authored-By`.

## Key facts (verified)
- `data-line` values are **1-based** (`markdown.ts` `annotateAnchors`: each anchor = its element's `token.map[0] + 1`). The raw source line for a 1-based line number `L` is `text.split('\n')[L - 1]` (precedent: `markdown.ts:429` `lines[range.map[0]]`).
- **Anchor semantics (codex BLOCKER → reframed, NOT a code change).** Each `<li>`, `<tr>`, paragraph, heading, `hr`, and fenced-code block gets its OWN anchor at its START line (documented uniqueness guarantee in `annotateAnchors`). So the quote shows the source line the comment is **anchored to** — for list items, table rows, and headings (the overwhelming majority of plan/answer content) that IS the clicked line. For a **multi-line paragraph** it's the paragraph's first line; for a **fenced code block** it's the opening ` ``` ` line (the anchor), not an interior code line. This is correct behavior for a block-anchored comment model — the feature promises "the line you're commenting on" (= the anchor), not "an arbitrary interior line." Frame the UI/tests accordingly; do NOT promise the exact visual line for multi-line blocks.
- `LineCommentedDocument` already receives the raw markdown as its `text` prop and owns `activeCommentLine`. `LineCommentForm` currently gets `line` (number) but NOT the source text.
- Current active-line CSS to change (`skins/base/plan-panel.css`): `[data-line].is-active` (≈260), `.md tr.is-active > :is(th,td)` (≈276), `hr[data-line].is-active` (≈287).

---

## Task 1: Quote preview above the input

**Files:** `pkg/web/dashboard/src/components/plan-panel/LineCommentedDocument.tsx`; `skins/base/plan-panel.css`; test `LineCommentedDocument.test.tsx` (or `PlanPanel.test.tsx` — match where line-comment-form tests live).

**Behavior:**
- In `LineCommentedDocument`, compute the quoted source line for the open form: `const sourceLines = useMemo(() => text.split('\n'), [text])`; for `activeCommentLine` (1-based), `raw = sourceLines[activeCommentLine - 1] ?? ''`. The quoted value is `raw.trim()`, EXCEPT return `''` (→ no quote) when the line is blank OR a horizontal rule (`/^\s*([-*_])(\s*\1){2,}\s*$/`). Pass it to `LineCommentForm` as a new prop `quotedLine: string` (empty ⇒ don't render the quote).
- In `LineCommentForm`, when `quotedLine !== ''`, render between the `comment-display-header` and the `PasteableTextarea`:
  ```tsx
  {quotedLine !== '' && (
    <div className="line-comment-quote">
      <span className="line-comment-quote-src">{quotedLine}</span>
    </div>
  )}
  ```
  Add `quotedLine: string` to `LineCommentForm`'s props and thread it at the call site (`renderBlock`, ≈216).

**CSS (`skins/base/plan-panel.css`) — Variant 1, faint glyph:**
```css
.line-comment-quote {
  position: relative;
  padding: 4px 10px 4px 40px;
  margin-bottom: 8px;
  min-height: 26px;
  display: flex;
  align-items: center;
}
.line-comment-quote::before {
  content: "\201C";                 /* “ */
  position: absolute;
  left: 6px;
  top: -6px;
  font-family: Georgia, "Times New Roman", serif;
  font-size: 44px;
  line-height: 1;
  color: var(--accent-primary);
  opacity: 0.30;                    /* faint, per approved mockup */
  pointer-events: none;
}
.line-comment-quote-src {
  font-family: var(--font-mono);
  font-size: 12.5px;
  color: var(--text-muted);
  display: -webkit-box;
  -webkit-line-clamp: 2;            /* clamp long lines to 2 lines */
  -webkit-box-orient: vertical;
  overflow: hidden;
}
```

- [ ] **Step 1: Failing tests.** In the line-comment-form test suite, with a `text` prop containing distinct, identifiable lines: (a) **list item** → open a comment on an `<li>` line → the quote shows that item's source (`- Task two: …`); (b) **table row** → open a comment on a `<tr>` → the quote shows that row's source; (c) **heading/paragraph** → its source line; (d) **fenced code** (codex) → the quote shows the opening fence line (documents the anchor semantics — assert it equals the ` ``` ` line, NOT an interior line); (e) **hr / blank** → NO `.line-comment-quote` element. All assert the 1-based→0-based mapping (`text.split('\n')[N-1]`). Reuse the existing harness for clicking a line to open the form.
- [ ] **Step 2:** Run → RED (`npx vitest run src/components/plan-panel`).
- [ ] **Step 3:** Implement the `quotedLine` computation + prop threading + `LineCommentForm` render + the CSS above.
- [ ] **Step 4:** Run → GREEN + `npm run typecheck`.
- [ ] **Step 5:** Commit `feat(dashboard): quoted-строка над полем комментария (мессенджер-стиль, большая кавычка)`.

---

## Task 2: Softer active-line highlight (Option A)

**Files:** `skins/base/plan-panel.css` (CSS only).

Replace the three `.is-active` rules (drop every `0 0 0 1px var(--accent-primary)` / `inset 0 0 0 1px` ring and the amber on the active state; active is now accent-colored):

```css
/* text line: soft accent fill + thick left accent bar, no outline */
.line-commented [data-line].is-active {
  background: color-mix(in srgb, var(--accent-primary) 10%, transparent);
  box-shadow: inset 3px 0 0 var(--accent-primary);
}

/* table row: soft accent fill only. The general `[data-line].is-active` rule
   ALSO matches the tr (a tr carries data-line), so its `inset 3px` bar would
   apply per-cell — explicitly clear it on the row (codex MAJOR). */
.line-commented .md tr.is-active { box-shadow: none; }
.line-commented .md tr.is-active > :is(th, td) {
  background: color-mix(in srgb, var(--accent-primary) 12%, transparent);
}

/* hr: left accent bar instead of the ring */
.line-commented hr[data-line].is-active {
  box-shadow: inset 3px 0 0 var(--accent-primary);
}
```

Leave `.has-comment` (amber left bar) and `.is-hover` (mint tint) and their table/hr variants UNCHANGED — only the *active/selected* line changes. Keep `[data-line]:focus-visible` (keyboard focus ring) as-is — that's accessibility, not the selection style.

- [ ] **Step 1:** Apply the three CSS edits above. (CSS-only; the visual was pre-approved via the mockup. No unit test — verified live in Task 3.)
- [ ] **Step 2:** `npm run typecheck` (sanity; CSS doesn't typecheck but confirm nothing else broke) + `npx vitest run src/components/plan-panel` still green (no test asserts the removed ring).
- [ ] **Step 3:** Commit `feat(dashboard): мягкая подсветка активной строки (заливка+левый акцент, без рамки)`.

---

## Task 3: Full suite + build + live verification

- [ ] `cd pkg/web/dashboard && npx vitest run` (full suite green) + `npm run typecheck`.
- [ ] `npm run build`; commit the bundle.
- [ ] Live (reuse the mock planning-agent flow → `awaiting_approval`): open the dashboard, click a plan line → confirm (a) the selected line shows the soft accent fill + left bar (NO 1px frame), and (b) the comment box shows the big faint quote glyph + the quoted source line above the textarea. Same in a dialog question's line comment. Screenshot for the user.
- [ ] **Light-theme contrast check (codex MINOR):** switch to a light skin (coffee/novacorps light `--text-muted` is intentionally below AA) and confirm the 12.5px quoted text and the 0.30-opacity glyph are still legible. If they look weak, bump the glyph to `opacity: 0.4` and/or the src text to a slightly stronger token — decide from the live look.

## Self-Review
- Quote preview: correct 1-based→0-based source lookup; hidden for blank/hr; big faint accent glyph (Variant 1, opacity 0.30); shared → plan + dialog. ✓
- Highlight: Option A (fill + thick left bar, no ring), accent-token-based via `color-mix` for cross-skin consistency; only the active state changes. ✓
- No API/handler/commentCount changes; the `hasOpenDraft`/gating logic from the prior branch is untouched. ✓
