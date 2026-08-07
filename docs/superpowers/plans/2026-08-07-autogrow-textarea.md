# Auto-growing textareas with manual-resize override — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashboard's three textareas (plan/dialog line-comments, the dialog free-answer box, the agent-note modal) grow to fit their content up to a 400px cap, without disturbing the caret, while still letting the user manually drag them taller/shorter — a manual drag disables further auto-growth until the field is emptied again.

**Architecture:** One shared hook, `useAutoGrowTextarea(value, maxHeightPx)`, does the work via a `useLayoutEffect` (grows synchronously before paint, so there's no visible jump) plus a `ResizeObserver` (detects a manual drag by noticing the element's height no longer matches what the hook itself last set, and locks out further auto-adjustment until the value goes back to `''`). Each of the three textareas gets the hook's ref attached, plus a small CSS addition (`max-height: 400px; overflow-y: auto;`, and a `min-height` for the one textarea that lacks one today).

**Tech Stack:** React 18 + TypeScript, Vitest + @testing-library/react, native `ResizeObserver` (no new npm dependency).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-07-autogrow-textarea-design.md`
- `max-height` is `400px` for all three textareas.
- Each textarea keeps its existing `min-height` unchanged (`.line-comment-form textarea`: 56px, `.dialog-pending .dialog-custom`: 60px); `.agent-note-textarea` gets a new `min-height: 60px` (it has none today).
- The manual resize handle (`resize: vertical`) is kept on all three — never removed.
- A manual resize (detected via `ResizeObserver`) disables auto-grow for that textarea instance until its value becomes `''` again.
- Commit messages must be in Russian, no `Co-Authored-By` trailer.

---

### Task 1: `useAutoGrowTextarea` hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.ts`
- Create: `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/index.ts`
- Test: `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.test.ts`

**Interfaces:**
- Produces: `useAutoGrowTextarea(value: string, maxHeightPx: number): RefObject<HTMLTextAreaElement>` — a plain `useRef`-backed ref object (not a callback ref; none of the three consumers have the "late mount" problem `useStickToBottom` had, so the simpler shape is enough). Tasks 2–4 import this from `'../../hooks/use-auto-grow-textarea'` and attach the returned ref to their `<textarea ref={...}>`.

- [ ] **Step 1: Write the failing tests**

Create `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAutoGrowTextarea } from './use-auto-grow-textarea'

class MockResizeObserver {
  callback: ResizeObserverCallback
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
  }
  observe() {}
  disconnect() {}
  unobserve() {}
  trigger() {
    this.callback([] as unknown as ResizeObserverEntry[], this as unknown as ResizeObserver)
  }
}

function makeTextarea(scrollHeight: number): HTMLTextAreaElement {
  const el = document.createElement('textarea')
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
  return el
}

describe('useAutoGrowTextarea', () => {
  let lastObserver: MockResizeObserver | null = null

  beforeEach(() => {
    lastObserver = null
    vi.stubGlobal(
      'ResizeObserver',
      class extends MockResizeObserver {
        constructor(callback: ResizeObserverCallback) {
          super(callback)
          lastObserver = this
        }
      },
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('grows height to scrollHeight as the value changes', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el

    rerender({ value: 'some text' })

    expect(el.style.height).toBe('120px')
  })

  it('clamps growth at maxHeightPx', () => {
    const el = makeTextarea(999)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el

    rerender({ value: 'a very long comment' })

    expect(el.style.height).toBe('400px')
  })

  it('locks out auto-grow after a manual resize, and stays put on further value changes', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el
    rerender({ value: 'first draft' })
    expect(el.style.height).toBe('120px')

    // Simulate the user dragging the resize handle (the browser sets an
    // inline height on the element, same as our hook does).
    el.style.height = '250px'
    lastObserver?.trigger()

    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    rerender({ value: 'first draft, now much longer' })

    expect(el.style.height).toBe('250px')
  })

  it('resets the lock once the value goes back to empty', () => {
    const el = makeTextarea(120)
    const { result, rerender } = renderHook(({ value }) => useAutoGrowTextarea(value, 400), {
      initialProps: { value: '' },
    })
    result.current.current = el
    rerender({ value: 'first draft' })

    el.style.height = '250px'
    lastObserver?.trigger()
    rerender({ value: 'first draft, locked' })
    expect(el.style.height).toBe('250px')

    Object.defineProperty(el, 'scrollHeight', { value: 90, configurable: true })
    rerender({ value: '' })
    rerender({ value: 'a fresh comment' })

    expect(el.style.height).toBe('90px')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.test.ts`
Expected: FAIL — the module `./use-auto-grow-textarea` doesn't exist yet.

- [ ] **Step 3: Implement the hook**

Create `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.ts`:

```ts
import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'

// Растит textarea по мере ввода текста (через scrollHeight) до maxHeightPx —
// дальше работает overflow-y из CSS. useLayoutEffect выполняется синхронно
// ДО отрисовки браузера: схлопывание в 'auto' и обратный рост до scrollHeight
// происходят за один кадр, поэтому курсор/вьюпорт не дёргаются.
//
// ResizeObserver ловит РУЧНОЙ ресайз через уголок textarea: браузер при
// перетаскивании сам выставляет el.style.height, поэтому сравнение текущего
// style.height с тем, что последним выставил сам хук (lastAutoHeight),
// однозначно отличает «потянул пользователь» от «выставили мы». Once
// detected, дальнейший авто-рост блокируется до следующего опустошения поля.
export function useAutoGrowTextarea(value: string, maxHeightPx: number): RefObject<HTMLTextAreaElement> {
  const ref = useRef<HTMLTextAreaElement>(null)
  const lastAutoHeight = useRef<number | null>(null)
  const locked = useRef(false)

  useLayoutEffect(() => {
    const el = ref.current
    if (el === null) return

    if (value === '') {
      locked.current = false
    }
    if (locked.current) return

    el.style.height = 'auto'
    const next = Math.min(el.scrollHeight, maxHeightPx)
    el.style.height = `${next}px`
    lastAutoHeight.current = next
  }, [value, maxHeightPx])

  useEffect(() => {
    const el = ref.current
    if (el === null) return

    const obs = new ResizeObserver(() => {
      const current = parseFloat(el.style.height || '0')
      if (lastAutoHeight.current !== null && current !== lastAutoHeight.current) {
        locked.current = true
      }
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [])

  return ref
}
```

Create `pkg/web/dashboard/src/hooks/use-auto-grow-textarea/index.ts`:

```ts
export { useAutoGrowTextarea } from './use-auto-grow-textarea'
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-auto-grow-textarea/use-auto-grow-textarea.test.ts`
Expected: PASS, all 4 tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-auto-grow-textarea/
git commit -m "feat(dashboard): добавляем useAutoGrowTextarea с блокировкой авто-роста при ручном ресайзе"
```

---

### Task 2: Wire into `PlanPanel.tsx`'s line-comment textarea

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx:1` (imports), `:23` (state block), `:316` (textarea JSX)
- Modify: `pkg/web/dashboard/skins/base/plan-panel.css:366-375` (`.line-comment-form textarea` rule)
- Test: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`

**Interfaces:**
- Consumes: `useAutoGrowTextarea` from Task 1 (`import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'`).

- [ ] **Step 1: Write the failing test**

Add to `pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx`, after the existing `'awaiting_approval: renders review lines...'` test. This mocks `scrollHeight` on the textarea prototype so jsdom (which always reports `0`) exercises the real growth path:

```tsx
  test('the comment textarea grows to fit its content via the auto-grow hook', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(150)

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer comment' } })

    expect(textarea.style.height).toBe('150px')

    scrollHeightSpy.mockRestore()
  })
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: FAIL — `textarea.style.height` is `''` (nothing sets it yet).

- [ ] **Step 3: Wire in the hook**

In `PlanPanel.tsx`, add the import (line 1 currently reads
`import { useEffect, useState, type ReactElement, type ReactNode } from 'react'`) — add a second import line right after it:

```ts
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
```

In the component body, right after `const [draft, setDraft] = useState('')` (line 23):

```ts
  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
```

In `renderPlanLine`, change the textarea (currently a single self-closing line):

```tsx
            <textarea placeholder={`Comment on line ${item.line}...`} value={draft} onChange={(event) => setDraft(event.target.value)} />
```

to:

```tsx
            <textarea
              ref={commentTextareaRef}
              placeholder={`Comment on line ${item.line}...`}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
            />
```

(`renderPlanLine` is a nested function inside `PlanPanel`, so it already has closure access to `commentTextareaRef` — no prop threading needed.)

In `pkg/web/dashboard/skins/base/plan-panel.css`, change the `.line-comment-form textarea` rule (currently lines 366–375):

```css
.line-comment-form textarea {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
  min-height: 56px;
}
```

to:

```css
.line-comment-form textarea {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
  min-height: 56px;
  max-height: 400px;
  overflow-y: auto;
}
```

(This rule is shared with `DialogChannel.tsx`'s identical `.line-comment-form textarea` — Task 3 does not need to touch this CSS again.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/plan-panel/PlanPanel.test.tsx`
Expected: PASS, including all pre-existing tests in the file unchanged.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/plan-panel/PlanPanel.test.tsx pkg/web/dashboard/skins/base/plan-panel.css
git commit -m "feat(dashboard): комментарий к строке плана растёт по мере ввода текста"
```

---

### Task 3: Wire into `DialogChannel.tsx`'s two textareas

**Files:**
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx:1` (imports), state block (~line 39), `:295` (line-comment textarea JSX), `:354` (dialog-custom textarea JSX)
- Modify: `pkg/web/dashboard/skins/base/dialog-channel.css:188-197` (`.dialog-pending .dialog-custom` rule)
- Test: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`

**Interfaces:**
- Consumes: `useAutoGrowTextarea` from Task 1, same import path as Task 2.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`, after the existing `'clicking a question line opens a comment form'` test:

```tsx
  test('the question-line comment textarea grows to fit its content', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(150)

    const { container } = render(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer comment' } })

    expect(textarea.style.height).toBe('150px')

    scrollHeightSpy.mockRestore()
  })

  test('the free-answer textarea grows to fit its content', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(200)

    const { container } = render(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a much longer free-text answer' } })

    expect(textarea.style.height).toBe('200px')

    scrollHeightSpy.mockRestore()
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx`
Expected: both new tests FAIL — neither textarea's `style.height` is set yet.

- [ ] **Step 3: Wire in the hook**

In `DialogChannel.tsx`, add the import (line 1 currently reads
`import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'`) — add right after it:

```ts
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'
```

In the component body, alongside the existing `draft`/`customText` state declarations:

```ts
  const commentTextareaRef = useAutoGrowTextarea(draft, 400)
  const customTextareaRef = useAutoGrowTextarea(customText, 400)
```

In `renderQuestionLine`, change the line-comment textarea:

```tsx
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
```

to:

```tsx
            <textarea
              ref={commentTextareaRef}
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
```

In the main render's dialog-custom textarea:

```tsx
                    <textarea
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={(event) => onCustomInput(event.target.value)}
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                          e.preventDefault()
                          void sendAnswer()
                        }
                      }}
                    />
```

to:

```tsx
                    <textarea
                      ref={customTextareaRef}
                      className="dialog-custom"
                      placeholder="Or type your own answer…"
                      value={customText}
                      disabled={pending.allow_custom !== true}
                      onChange={(event) => onCustomInput(event.target.value)}
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                          e.preventDefault()
                          void sendAnswer()
                        }
                      }}
                    />
```

In `pkg/web/dashboard/skins/base/dialog-channel.css`, change the `.dialog-pending .dialog-custom` rule (currently lines 188–197):

```css
.dialog-pending .dialog-custom {
  width: 100%;
  min-height: 60px;
  background: var(--panel-bg);
  border: 1px solid rgba(111, 212, 204, 0.2);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
}
```

to:

```css
.dialog-pending .dialog-custom {
  width: 100%;
  min-height: 60px;
  max-height: 400px;
  overflow-y: auto;
  background: var(--panel-bg);
  border: 1px solid rgba(111, 212, 204, 0.2);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/dialog-channel/DialogChannel.test.tsx`
Expected: PASS, including all pre-existing tests in the file unchanged.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx pkg/web/dashboard/skins/base/dialog-channel.css
git commit -m "feat(dashboard): комментарий к строке вопроса и свободный ответ в диалоге растут по мере ввода"
```

---

### Task 4: Wire into `AgentNoteModal.tsx`

**Files:**
- Modify: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx` (full file is 46 lines — see current content below)
- Modify: `pkg/web/dashboard/skins/base/agent-note-modal.css:93-104` (`.agent-note-textarea` rule)
- Test: `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.test.tsx`

**Interfaces:**
- Consumes: `useAutoGrowTextarea` from Task 1, same import path as Tasks 2–3.

- [ ] **Step 1: Write the failing test**

The existing file (`AgentNoteModal.test.tsx`) uses `describe`/`it` (not
`test`) and selects the textarea via `screen.getByRole('textbox')` (it's the
only textbox in the modal) rather than `container.querySelector` — match
that convention. Add this inside the existing `describe('AgentNoteModal', ...)`
block, after the existing `'disables Send while the note is empty'` test:

```tsx
  it('grows the textarea to fit its content', () => {
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(180)

    render(<AgentNoteModal stageId="s1" onSubmit={vi.fn()} onCancel={vi.fn()} />)
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer note for the agent' } })

    expect(textarea.style.height).toBe('180px')

    scrollHeightSpy.mockRestore()
  })
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/agent-note-modal/AgentNoteModal.test.tsx`
Expected: FAIL — `textarea.style.height` is `''`.

- [ ] **Step 3: Wire in the hook**

Replace the full contents of `pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx` with:

```tsx
import { useState, type ReactElement } from 'react'
import { useAutoGrowTextarea } from '../../hooks/use-auto-grow-textarea'

type AgentNoteModalProps = {
  stageId: string
  onSubmit: (note: string) => void
  onCancel: () => void
}

// Модалка «Добавить поправку агенту» (agent_suggest): открывается из кебаб-
// меню StagesList. Предупреждает, что агент доведёт текущее действие до
// конца перед перезапуском с этой фразой в контексте.
export function AgentNoteModal({ stageId, onSubmit, onCancel }: AgentNoteModalProps): ReactElement {
  const [note, setNote] = useState('')
  const noteTextareaRef = useAutoGrowTextarea(note, 400)

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={`Add a note for stage ${stageId}`}>
      <div className="modal-content agent-note-modal">
        <p className="agent-note-warning">
          The agent will finish its current action, then restart with this note in context.
        </p>
        <textarea
          ref={noteTextareaRef}
          className="agent-note-textarea"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What should the agent take into account?"
          autoFocus
        />
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={note.trim() === ''}
            onClick={() => onSubmit(note.trim())}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  )
}
```

(`rows={4}` is dropped — it's superseded by the CSS `min-height` added below, and now-unused.)

In `pkg/web/dashboard/skins/base/agent-note-modal.css`, change the `.agent-note-textarea` rule (currently lines 93–104):

```css
.agent-note-textarea {
  width: 100%;
  background: var(--bg);
  color: var(--ink);
  border: 1px solid var(--mint-soft);
  border-radius: 4px;
  padding: 10px;
  font-family: inherit;
  font-size: 12px;
  resize: vertical;
  margin-bottom: 14px;
}
```

to:

```css
.agent-note-textarea {
  width: 100%;
  background: var(--bg);
  color: var(--ink);
  border: 1px solid var(--mint-soft);
  border-radius: 4px;
  padding: 10px;
  font-family: inherit;
  font-size: 12px;
  resize: vertical;
  margin-bottom: 14px;
  min-height: 60px;
  max-height: 400px;
  overflow-y: auto;
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/agent-note-modal/AgentNoteModal.test.tsx`
Expected: PASS, including all pre-existing tests in the file unchanged (in particular, confirm no test asserts on the removed `rows` attribute — if one does, update it to assert on `min-height` behavior via CSS instead, or drop that specific assertion since `rows` is no longer part of the component's contract).

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.tsx pkg/web/dashboard/src/components/agent-note-modal/AgentNoteModal.test.tsx pkg/web/dashboard/skins/base/agent-note-modal.css
git commit -m "feat(dashboard): текстовое поле заметки агенту растёт по мере ввода"
```

---

### Task 5: Full verification pass

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Run the full lint + test + build pipeline**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make lint && make build && make test`
Expected: all three succeed with no new warnings/failures.

- [ ] **Step 2: Manually verify in a real browser**

Using the same kind of native (non-Docker — check `~/.afm/config.yaml` for a lingering `docker.enabled: true` and override it with `docker: { enabled: false }` in the scratch project's own `.afm/config.yaml`, as needed last time) `afm run` setup with a mock agent as used for the previous plan's manual verification, drive a browser to:

1. Open a plan-review comment form and type a long, multi-paragraph comment — confirm the box grows smoothly as you type, with no visible caret/viewport jump, and stays within a reasonable width (doesn't reflow the surrounding layout oddly).
2. Keep typing well past ~400px worth of text — confirm growth stops and an internal scrollbar appears instead of the box continuing to grow.
3. Drag the resize handle to make the box taller than its current auto-grown height, then keep typing — confirm the box does NOT snap back to the auto-computed height; your manual size sticks.
4. Clear the comment (× or Delete) and open a fresh comment on a different line — confirm it starts back at the normal auto-grow behavior (not stuck at the previous manual size).
5. Repeat a quick smoke check (type a long value, confirm growth) on the dialog's free-answer textarea and on the agent-note modal's textarea (reachable via the stage list's kebab menu → "Add a note").

Report the outcome of each of these five checks explicitly; do not report the task as complete without having exercised them in an actual browser.
