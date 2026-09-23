# Gate submit/commit buttons while a comment form is open — Plan

> **For agentic workers:** small, focused UI-gating fix. TDD. Steps use `- [ ]`.

**Goal:** In the plan-approval and dialog-answer UIs, you can open a line-comment form (click a line) and then accidentally hit a submit button — and it fires, discarding your in-progress comment. Make every submit/commit button **disabled while a comment form is open** (adding OR editing), so it's active only when all comments are submitted and no editing/adding window is open.

**Root cause:** the buttons gate on the wrong signal. `LineCommentDocumentApi` (`pkg/web/dashboard/src/components/plan-panel/LineCommentedDocument.tsx:19`) exposes:
- `activeCommentLine: number | null` — a comment form is **open** (add or edit), regardless of text.
- `hasOpenDraft = activeCommentLine !== null && draft.trim() !== ''` — open form **with non-empty text**.

The submit buttons either don't gate on the open form at all, or gate on `hasOpenDraft` (which misses an open-but-empty form — the exact misclick: click a line → empty form opens → hit submit → fires).

**Fix:** gate all four submit/commit buttons on `doc.activeCommentLine !== null` (open form). For the two that currently use `hasOpenDraft`, replace it with `activeCommentLine !== null` (strengthen from "open non-empty" to "open at all"). This reuses the existing `activeCommentLine` — no API change.

## Global Constraints
- Frontend gate = `npm run typecheck` + `npx vitest run` (dashboard has **no ESLint** — don't run/expect `npm run lint`).
- No behavior change beyond the disabled-gating. Do NOT change `commentCount` logic, the ternary that picks answer-SEND vs Send-feedback, or any submit handler.
- Commit messages Russian; no `Co-Authored-By`.

## The changes (exact)

**File `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`:**
- **Approve** (line 193): `disabled={busy || doc.commentCount > 0 || doc.hasOpenDraft}` → `disabled={busy || doc.commentCount > 0 || doc.activeCommentLine !== null}`.
- **Send revision** (line 204): `disabled={busy || doc.commentCount === 0}` → `disabled={busy || doc.commentCount === 0 || doc.activeCommentLine !== null}`.

**File `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`:**
- **answer SEND** (line 518, shown when `commentCount === 0`): `disabled={doc.hasOpenDraft || submitting}` → `disabled={doc.activeCommentLine !== null || submitting}`.
- **Send feedback** (line 529, shown when `commentCount > 0`): `disabled={submitting}` → `disabled={submitting || doc.activeCommentLine !== null}`.
- **answer textarea Ctrl+Enter (codex MEDIUM)** (`PasteableTextarea` `onSubmit` at line 508): a disabled SEND button does NOT block the textarea's own keyboard submit. Guard the closure: `onSubmit={() => { if (doc.activeCommentLine !== null) return; void sendAnswer() }}` so Ctrl+Enter in the answer field is also inert while a comment form is open. (The `doc` is in scope in `renderPending`.)

**Doc comments (codex LOW):** after the change, update the now-false comments to reference `activeCommentLine`: the `hasOpenDraft` line in `LineCommentedDocument.tsx`'s `LineCommentDocumentApi` type (note it's no longer what gates the actions) and the `DialogChannel.tsx:457` comment ("`doc.hasOpenDraft` гейтит SEND" → `activeCommentLine`).

Note: `hasOpenDraft` may become unused in these files after the change — if so, remove the now-dead reference from the destructure/`PlanPanelDoc` fallback ONLY if it's truly unused everywhere (grep first; it's part of the shared `LineCommentDocumentApi` type and may still be consumed elsewhere — do NOT remove the field from the type).

## Scope decision (ruling — flag for review)
"All submit buttons" here means exactly the **four comment-feedback / answer-commit actions**: Approve + Send revision (plan), answer SEND + Send feedback (dialog), plus the answer textarea's Ctrl+Enter path. It does NOT touch Retry/Continue/retry-hook/skip-hook (plan) or the option/Cancel buttons (dialog) — those aren't comment-commit actions (codex LOW clarification).

The user framed this as "the sendFeedback button" but stated the rule as "active only if all comments are written and there's no active comment editing/adding window", and said it applies "in the plan/answer". So this plan gates **all four** submit/commit buttons (Approve + Send revision in the plan; answer SEND + Send feedback in the dialog) consistently — leaving Approve/answer-SEND gated on the weaker `hasOpenDraft` while Send revision/feedback use `activeCommentLine` would be an inconsistent, confusing mix. If the reviewer/user wants it narrowed to ONLY Send feedback + Send revision, dropping the Approve + answer-SEND lines is trivial.

## Tasks

### Task 1: PlanPanel — gate Approve + Send revision on an open comment form
**Files:** `PlanPanel.tsx`; test `PlanPanel.test.tsx`.

- [ ] **Step 1: Failing tests.** In `PlanPanel.test.tsx`, for a review-stage plan (`isReview`, not auto-approve): (a) submit a comment (commentCount>0), then OPEN a new comment form on another line with an EMPTY draft → assert **Send revision** is `disabled`; close the form → assert it's enabled. (b) With no submitted comments, open an empty comment form → assert **Approve** is `disabled`; close → enabled. Use the existing test harness for opening a line-comment form (look at how PlanPanel.test.tsx / LineCommentedDocument.test.tsx simulate clicking a line + the form appearing). If simulating the open form is hard at the PlanPanel level, assert at the boundary the component exposes (the `disabled` attribute given a doc with `activeCommentLine` set) — but prefer a real open-form interaction.
- [ ] **Step 2:** Run → RED (`npx vitest run src/components/plan-panel`).
- [ ] **Step 3:** Apply the two PlanPanel edits above.
- [ ] **Step 4:** Run → GREEN + `npm run typecheck`.
- [ ] **Step 5:** Commit `fix(dashboard): блокировать Approve/Send revision при открытой форме комментария`.

### Task 2: DialogChannel — gate answer SEND + Send feedback on an open comment form
**Files:** `DialogChannel.tsx`; test `DialogChannel.test.tsx`.

- [ ] **Step 1: Failing tests.** (a) With ≥1 submitted comment (Send feedback shown), open a new empty comment form → assert **Send feedback** `disabled`; close → enabled. (b) With 0 comments (answer SEND shown), open an empty comment form → assert **SEND** `disabled`; close → enabled (and confirm the pre-existing non-empty-draft gate still disables). (c) **Ctrl+Enter guard:** with an empty comment form open, invoking the answer textarea's `onSubmit` (Ctrl+Enter) must NOT call `sendAnswer` (assert the answer POST/`sendAnswer` isn't triggered while `activeCommentLine !== null`). Match the existing DialogChannel test harness for opening a line comment on the question.
- [ ] **Step 2:** Run → RED (`npx vitest run src/components/dialog-channel`).
- [ ] **Step 3:** Apply the three DialogChannel edits above (answer SEND, Send feedback, the `onSubmit` guard) + the doc-comment update.
- [ ] **Step 4:** Run → GREEN + `npm run typecheck`.
- [ ] **Step 5:** Commit `fix(dashboard): блокировать answer SEND/Send feedback при открытой форме комментария`.

### Task 3: Full suite + dead-code check
- [ ] Run `cd pkg/web/dashboard && npx vitest run` (full suite green) + `npm run typecheck`.
- [ ] `grep -rn hasOpenDraft pkg/web/dashboard/src` — confirm no now-dead usage was left dangling (the type field stays; only remove a genuinely-unused local reference if one appears). Commit any cleanup.

### Task 4: Build bundle + live verification
- [ ] `cd pkg/web/dashboard && npm run build`; commit the bundle.
- [ ] Live: run a flow with an `interactive` review/approval stage, open the dashboard, click a plan line to open a comment form → confirm **Send revision** (and **Approve**) are visually disabled while the form is open, and enabled after submitting/closing. Same for a dialog question's line comment → **Send feedback**/**SEND** disabled while a form is open.

## Self-Review
- Covers: Send feedback (dialog), Send revision (plan), answer SEND (dialog), Approve (plan) — all gated on `activeCommentLine !== null`. ✓
- No API/type change; no handler change; `commentCount` logic and the answer-vs-feedback ternary untouched. ✓
- Open item surfaced: whether to narrow scope to only the two "Send …" buttons.
