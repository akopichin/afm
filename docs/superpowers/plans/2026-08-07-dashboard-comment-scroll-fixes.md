# Dashboard: line-comment closing, Approve gating, dialog scroll — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three dashboard UX bugs: line-comment forms closing on outside clicks (breaking text selection/copy), the Approve button staying active while draft line comments exist, and dialog history rendering scrolled to the top instead of the bottom.

**Architecture:** Three independent, small changes in `pkg/web/dashboard/src`: (1)/(2) guard the shared `handleLineClick` pattern in `PlanPanel.tsx` and `DialogChannel.tsx` so a non-empty draft can only be closed via an explicit `×`, never by clicking elsewhere; (3) disable `Approve` in `PlanPanel.tsx` while `commentCount > 0`; (4) switch the shared `useStickToBottom` hook from a mount-once `useRef` to a state-backed callback ref so it reattaches whenever its container actually mounts, and make it jump to the bottom immediately on attach when content already exists.

**Tech Stack:** React 18 + TypeScript, Vitest + @testing-library/react, existing CSS classes (no new styles needed).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-07-dashboard-comment-scroll-fixes-design.md`
- No confirmation dialog when discarding a draft comment via `×` — it is just dropped.
- One active draft comment line at a time (no multi-draft support) — unchanged from today.
- The `×` close button reuses the existing `.comment-display-header` / `.comment-remove` CSS classes (no new CSS).
- Point 2 (Approve gating) is scoped to `PlanPanel.tsx` only — `DialogChannel.tsx` has no separate Approve action.
- All commit messages must be in Russian (per user's global instructions) and must NOT include a `Co-Authored-By` trailer.
- Run `cd pkg/web/dashboard && npm run lint` (or whatever the project's existing lint script is called — check `package.json`) and `npm test` after each task; do not leave lint or test failures uncommitted.

---

### Task 1: `PlanPanel.tsx` — comment form closes only via ×

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx:75-83` (`handleLineClick`), `:281-334` (`renderPlanLine`'s active-form JSX)
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`

**Interfaces:**
- Consumes: existing `comments: Record<number, string>`, `activeCommentLine: number | null`, `draft: string` state and `saveComment`/`deleteComment` functions already defined in `PlanPanel.tsx` — unchanged.
- Produces: a new `closeCommentForm(): void` function in `PlanPanel.tsx`, called only by the new `×` button. `handleLineClick(line: number): void` keeps its existing signature but changes behavior (see below). No other file depends on these — self-contained to this component.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`, inside the existing `describe('PlanPanel', ...)` block (after the `'awaiting_approval: renders review lines...'` test):

```tsx
  test('a non-empty draft ignores clicks on other lines and on itself; only × discards it', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    const line2 = container.querySelector('[data-line="2"]') as HTMLElement

    fireEvent.click(line1)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'in progress' } })

    // Re-clicking the same row (e.g. the click that ends a text-selection drag) must not close it.
    fireEvent.click(line1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('in progress')

    // Clicking a different row must not switch away from the open draft either.
    fireEvent.click(line2)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()
    expect(container.querySelector('[data-line="2"] .line-comment-form')).toBeNull()

    // Only the × discards it.
    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(container.querySelector('.line-comment-form')).toBeNull()
  })

  test('an empty draft still lets a row click switch to a different line', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).toBeNull()
    expect(container.querySelector('[data-line="2"] .line-comment-form')).not.toBeNull()
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: the two new tests FAIL — the first because clicking `line2` currently switches the form away from line 1 (and there is no button named `Close comment on line 1`); the second should already pass (it's a regression guard) but run it anyway to confirm the suite executes.

- [ ] **Step 3: Implement the guard and the × button**

In `PlanPanel.tsx`, replace `handleLineClick` (currently lines 75-83):

```ts
  function handleLineClick(line: number) {
    if (activeCommentLine !== null && draft.trim() !== '') return

    if (activeCommentLine === line) {
      setActiveCommentLine(null)
      return
    }

    setActiveCommentLine(line)
    setDraft(comments[line] ?? '')
  }

  function closeCommentForm() {
    setActiveCommentLine(null)
    setDraft('')
  }
```

Then replace the active-form block inside `renderPlanLine` (currently lines 314-331):

```tsx
        {activeCommentLine === item.line && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            <div className="comment-display-header">
              <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>{`Comment on line ${item.line}`}</span>
              <button
                type="button"
                className="comment-remove"
                aria-label={`Close comment on line ${item.line}`}
                title="Close"
                onClick={closeCommentForm}
              >
                ✕
              </button>
            </div>
            <textarea placeholder={`Comment on line ${item.line}...`} value={draft} onChange={(event) => setDraft(event.target.value)} />
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={() => saveComment(item.line)}>
                {hasComment ? 'Update' : 'Add'}
              </button>
              {hasComment && (
                <button className="btn btn-cancel" type="button" onClick={() => deleteComment(item.line)}>
                  Delete
                </button>
              )}
            </div>
          </div>
        )}
```

Note the `Cancel` button is removed — the new `×` in the header is the only way to close a form now.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: PASS, including all pre-existing tests in the file (the `'awaiting_approval: renders review lines...'` and `'sendRevision(): ...'` and `'the X on a saved comment removes it...'` tests must still pass unchanged).

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx
git commit -m "fix(dashboard): форма комментария к строке плана закрывается только по ×"
```

---

### Task 2: `DialogChannel.tsx` — same fix for the question-line comment form

**Files:**
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx:200-208` (`handleLineClick`), `:253-315` (`renderQuestionLine`'s active-form JSX)
- Test: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`

**Interfaces:**
- Consumes: existing `comments`, `activeCommentLine`, `draft` state and `saveComment`/`deleteComment` already defined in `DialogChannel.tsx` — unchanged.
- Produces: `closeCommentForm(): void` in `DialogChannel.tsx`, mirroring Task 1's function of the same name (separate component, no shared import — this is intentionally the same duplicated pattern the codebase already uses for `handleLineClick`/`saveComment`/`deleteComment`).

- [ ] **Step 1: Write the failing tests**

Add to `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`, inside the existing `describe('DialogChannel', ...)` block (after the `'clicking a question line opens a comment form'` test, following the same `pending` fixture shape used by the neighboring tests):

```tsx
  test('a non-empty draft ignores clicks on other question lines and on itself; only × discards it', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = render(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    const line2 = container.querySelector('[data-line="2"]') as HTMLElement

    fireEvent.click(line1)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'in progress' } })

    fireEvent.click(line1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('in progress')

    fireEvent.click(line2)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()
    expect(container.querySelector('[data-line="2"] .line-comment-form')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(container.querySelector('.line-comment-form')).toBeNull()
  })

  test('an empty draft still lets a row click switch to a different question line', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = render(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).toBeNull()
    expect(container.querySelector('[data-line="2"] .line-comment-form')).not.toBeNull()
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx`
Expected: the first new test FAILS the same way as Task 1's equivalent (line-switch not blocked, no `Close comment on line 1` button yet); the second should pass already.

- [ ] **Step 3: Implement the guard and the × button**

In `DialogChannel.tsx`, replace `handleLineClick` (currently lines 200-208):

```ts
  function handleLineClick(line: number) {
    if (activeCommentLine !== null && draft.trim() !== '') return

    if (activeCommentLine === line) {
      setActiveCommentLine(null)
      return
    }

    setActiveCommentLine(line)
    setDraft(comments[line] ?? '')
  }

  function closeCommentForm() {
    setActiveCommentLine(null)
    setDraft('')
  }
```

Then replace the active-form block inside `renderQuestionLine` (currently lines 285-312):

```tsx
        {activeCommentLine === item.line && (
          <div className="line-comment-form" onClick={(event) => event.stopPropagation()}>
            <div className="comment-display-header">
              <span style={{ color: 'var(--c-awaiting)', fontSize: '12px' }}>{`Comment on line ${item.line}`}</span>
              <button
                type="button"
                className="comment-remove"
                aria-label={`Close comment on line ${item.line}`}
                title="Close"
                onClick={closeCommentForm}
              >
                ✕
              </button>
            </div>
            <textarea
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                  e.preventDefault()
                  saveComment(item.line)
                }
              }}
            />
            <div className="comment-actions">
              <button className="btn btn-send" type="button" onClick={() => saveComment(item.line)}>
                {hasComment ? 'Update' : 'Add'}
              </button>
              {hasComment && (
                <button className="btn btn-cancel" type="button" onClick={() => deleteComment(item.line)}>
                  Delete
                </button>
              )}
            </div>
          </div>
        )}
```

The Ctrl/Cmd+Enter-to-save shortcut on the textarea is preserved (it existed in `DialogChannel.tsx` but not in `PlanPanel.tsx` — keep this difference, don't add it to `PlanPanel.tsx`). The `Cancel` button is removed, same as Task 1.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx`
Expected: PASS, including all pre-existing tests in the file — in particular `'adding a comment hides options+textarea and shows Send feedback; deleting the only comment restores them'`, `'the X on a saved comment removes it without opening the edit form'`, and `'Ctrl+Enter in a comment textarea saves the comment instead of sending feedback'` must still pass unchanged.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx
git commit -m "fix(dashboard): форма комментария к строке вопроса в диалоге закрывается только по ×"
```

---

### Task 3: `PlanPanel.tsx` — disable Approve while draft comments exist

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx:188-198` (the `Approve` button's `disabled` prop)
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`

**Interfaces:**
- Consumes: existing `commentCount` (already computed as `Object.keys(comments).length` at `PlanPanel.tsx:33`) and `busy` state — no new state needed.
- Produces: no new exports; purely a `disabled` expression change on the existing `#btn-approve` button.

- [ ] **Step 1: Write the failing test**

Add to `PlanPanel.test.tsx`, after the existing `'approve(): posts to the approve endpoint...'` test:

```tsx
  test('Approve is disabled while a draft comment exists, and re-enables once it is removed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const approveBtn = screen.getByRole('button', { name: 'Approve' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please fix' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    expect(approveBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))

    expect(approveBtn).not.toBeDisabled()
  })
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: FAIL at `expect(approveBtn).toBeDisabled()` — Approve is currently only disabled by `busy`.

- [ ] **Step 3: Implement the gating**

In `PlanPanel.tsx`, change the `Approve` button's `disabled` prop (currently `disabled={busy}` at line 192):

```tsx
              <button
                id="btn-approve"
                className={`btn btn-approve${clicked === 'approve' ? ' ok' : ''}`}
                type="button"
                disabled={busy || commentCount > 0}
                onClick={approve}
              >
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: PASS, including the pre-existing `'approve(): posts to the approve endpoint...'` test (it never adds a comment, so `commentCount` stays `0` and `Approve` remains enabled/disableable by `busy` exactly as before).

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx
git commit -m "fix(dashboard): блокируем Approve, пока есть неотправленные комментарии к плану"
```

---

### Task 4: `useStickToBottom` — callback ref + initial jump-to-bottom on late attach

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts`
- Test: `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`

**Interfaces:**
- Consumes: nothing external.
- Produces: `useStickToBottom<T extends HTMLElement>(): { ref: (node: T | null) => void; stick: boolean; jumpToBottom: () => void }` — the **shape of `ref` changes** from a `React.RefObject<T>` (object with `.current`) to a plain callback function `(node: T | null) => void`. Both `EventFeedPanel.tsx` (`ref={feed.ref}`) and `DialogChannel.tsx` (`ref={feed.ref}`) already pass `feed.ref` straight into a JSX `ref` prop, which accepts a callback ref exactly like it accepts a ref object — **no changes needed in either consumer file**. `DialogChannel.test.tsx` mocks the whole hook (`vi.mock('../../hooks/use-stick-to-bottom', ...)` returning its own `{ ref: { current: null }, stick: true, jumpToBottom: mockJumpToBottom }`), so it is unaffected by this signature change and needs no edits.

- [ ] **Step 1: Write the failing tests**

Replace the full contents of `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts` with:

```ts
import { describe, it, expect } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useStickToBottom } from './use-stick-to-bottom'

describe('useStickToBottom', () => {
  it('stick=true by default; ref is a callback, jumpToBottom is a function', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
    expect(result.current.stick).toBe(true)
    expect(typeof result.current.jumpToBottom).toBe('function')
    expect(typeof result.current.ref).toBe('function')
  })

  it('scrolls to the bottom immediately when the node attaches after content already exists', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const el = document.createElement('div')
    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    el.scrollTop = 0

    // Simulates DialogChannel: the container mounts (ref callback fires) only after
    // history/pending content is already in the DOM, unlike a container that's
    // always mounted from the first render.
    act(() => {
      result.current.ref(el)
    })

    expect(el.scrollTop).toBe(500)
  })

  it('re-attaching to a new node (e.g. after the container remounts) tracks the new node, not the old one', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const first = document.createElement('div')
    Object.defineProperty(first, 'scrollHeight', { value: 100, configurable: true })

    act(() => {
      result.current.ref(first)
    })
    expect(first.scrollTop).toBe(100)

    act(() => {
      result.current.ref(null)
    })

    const second = document.createElement('div')
    Object.defineProperty(second, 'scrollHeight', { value: 900, configurable: true })
    second.scrollTop = 0

    act(() => {
      result.current.ref(second)
    })
    expect(second.scrollTop).toBe(900)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`
Expected: FAIL — `result.current.ref` is currently a `RefObject` (has `.current`, is not callable as `result.current.ref(el)`), so the second and third tests throw `TypeError: result.current.ref is not a function`.

- [ ] **Step 3: Implement the callback ref + initial jump**

Replace the full contents of `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts` with:

```ts
import { useCallback, useEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх.
// stick=true → MutationObserver докручивает вниз при росте контента.
// stick=false → не трогаем скролл; jumpToBottom() возвращается к хвосту.
//
// ref — callback (не RefObject): некоторые контейнеры (DialogChannel/#dialog-scroll)
// монтируются не сразу, а только когда появляется контент (hasContent). Callback
// ref гарантирует, что эффект ниже переустановит наблюдатели именно в момент
// реального появления DOM-узла, а не только один раз при первом рендере хука.
export function useStickToBottom<T extends HTMLElement>(): {
  ref: (node: T | null) => void
  stick: boolean
  jumpToBottom: () => void
} {
  const [node, setNode] = useState<T | null>(null)
  const [stick, setStick] = useState(true)
  const stickRef = useRef(true)
  stickRef.current = stick

  const jumpToBottom = useCallback(() => {
    if (node === null) return
    node.scrollTop = node.scrollHeight
    setStick(true)
  }, [node])

  useEffect(() => {
    if (node === null) return

    // Узел мог примонтироваться уже с готовым контентом (та самая задержка
    // DialogChannel выше) — без явного скролла здесь MutationObserver ниже
    // реагирует только на БУДУЩИЙ рост контента, и список остаётся наверху.
    if (stickRef.current) node.scrollTop = node.scrollHeight

    const onScroll = () => {
      const near = node.scrollHeight - node.scrollTop - node.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) node.scrollTop = node.scrollHeight
    })

    node.addEventListener('scroll', onScroll, { passive: true })
    obs.observe(node, { childList: true, subtree: true, characterData: true })
    return () => {
      node.removeEventListener('scroll', onScroll)
      obs.disconnect()
    }
  }, [node])

  return { ref: setNode, stick, jumpToBottom }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`
Expected: PASS, all three tests.

- [ ] **Step 5: Run the full dashboard test suite to confirm no regressions in consumers**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS — in particular `EventFeedPanel.test.tsx` and `DialogChannel.test.tsx` (which mocks this hook entirely, so it's unaffected by the signature change) must still pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts
git commit -m "fix(dashboard): useStickToBottom реагирует на поздний монтаж контейнера и сразу докручивает вниз"
```

---

### Task 5: Full verification pass

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Run the full lint + test + build pipeline**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make lint && make build && make test`
Expected: all three succeed with no new warnings/failures (this project's `make lint`/`make build`/`make test` cover both the Go backend and, via `make build`'s `npm run build` step, a production build of the dashboard — confirms the TypeScript compiles cleanly with the new callback-ref type).

- [ ] **Step 2: Manually verify in a real browser**

Start the dashboard against a real or mock afm run (use the project's existing `run`/`verify` skill or `afm run` against a test flow with an `awaiting_approval` stage and an interactive stage), then in the browser:

1. Open a plan review panel, click a line to open a comment, type text, then drag-select some plan text on a *different* line to copy it — the comment form must stay open with the typed text intact.
2. With the comment form still open and non-empty, click a different plan line — the form must not move or close.
3. Click the `×` on the open form — it closes and the text is gone.
4. Add a comment and confirm `Approve` is now disabled and only `Send revision` is clickable; delete the comment and confirm `Approve` re-enables.
5. Open an interactive stage's dialog channel that already has prior Q&A history, and confirm it opens scrolled to the latest message, not the first one. Scroll up manually, then wait for the 2-second poll to tick — confirm your scroll position is not disturbed.

Report the outcome of each of these five checks explicitly; do not report the task as complete without having exercised them in an actual browser.
