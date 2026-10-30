import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import type { Stage, StageQuota } from '../../types'
import { PlanPanel } from './PlanPanel'
import { FileBrowserProvider } from '../file-browser'
import { CommentProvider } from '../comments/CommentProvider'

function makeStage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 's1', name: 'Stage', status: 'running', updatedAt: '',
    interactive: false, autonomous: false, autoApprove: false, hasDialog: false,
    showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [], ...overrides,
  }
}

function textResponse(text: string): Response {
  return { ok: true, text: async () => text } as Response
}

// PlanPanel's line-comment textarea renders with allowFileReferences — its "Attach project file" button calls
// useFileBrowser, which throws outside a FileBrowserProvider. CommentProvider owns drafts for the whole run, so
// rerenderPanel keeps it mounted while the panel itself comes and goes.
function Wrap({ children, enabled }: { children: ReactElement; enabled: boolean }): ReactElement {
  return (
    <CommentProvider runId="run-1">
      <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={enabled}>
        {children}
      </FileBrowserProvider>
    </CommentProvider>
  )
}

function renderPlanPanel(ui: ReactElement, enabled = true) {
  const view = render(<Wrap enabled={enabled}>{ui}</Wrap>)
  return { ...view, rerenderPanel: (next: ReactElement) => view.rerender(<Wrap enabled={enabled}>{next}</Wrap>) }
}

describe('PlanPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('normal mode: fetches the plan and renders it as markdown', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Hello\n\nWorld'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(container.querySelector('.md')).not.toBeNull())
    expect(container.querySelector('.md')?.innerHTML).toContain('Hello')
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(container.querySelector('#retry-section')).toBeNull()
  })

  test('script stage: does not fetch a plan (no 404 noise — script stages never have plan.md)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ isScript: true, status: 'done' })} />)

    // Никаких запросов на /plan для скрипт-стадии — именно они давали 404 в консоли.
    await Promise.resolve()
    const planCalls = fetchSpy.mock.calls.filter(([input]) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.endsWith('/plan')
    })
    expect(planCalls).toHaveLength(0)
    // Панель всё равно рендерится (для failed/paused-скриптов там живут кнопки),
    // просто без плана.
    expect(container.querySelector('#plan-content')).not.toBeNull()
    expect(container.querySelector('#plan-empty')).not.toHaveClass('hidden')
  })

  test('awaiting_approval: anchors each line and opens a comment form (sibling with data-comment-line) on click', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    // The visible number plate names the line; there is no native tooltip that could get stuck on screen.
    expect(line1.getAttribute('title')).toBeNull()
    expect(line1.getAttribute('aria-description')).toBe('Press Enter to comment on line 1')
    expect(container.querySelector('.line-comment-form')).toBeNull()

    fireEvent.click(line1)
    // The form is NOT a descendant of the anchor (can't nest a div in an h1/p);
    // it is a sibling marked data-comment-ui + data-comment-line.
    const form = container.querySelector('.line-comment-form[data-comment-line="1"]') as HTMLElement
    expect(form).not.toBeNull()
    expect(form.closest('[data-line]')).toBeNull()
    // The anchor itself carries has-comment/is-active classes (applied imperatively).
    expect(line1.classList.contains('is-active')).toBe(true)
  })

  test('awaiting_approval: an Assumptions heading is a plain heading row, and the list after it stays commentable per item', async () => {
    // Plan headings are ordinary rows (no collapsible coloured wrapper); each list item is anchored on its own line.
    const plan = ['## Assumptions', '- risk one', '- risk two', '## Next', '- item a', '- item b'].join('\n')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(plan))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    await waitFor(() => expect(container.querySelector('[data-line="2"]')).not.toBeNull())

    expect(container.querySelector('.plan-section-wrapper')).toBeNull()
    expect(container.querySelector('button.section-header')).toBeNull()
    expect(container.querySelector('h2[data-line="1"]')?.textContent).toBe('Assumptions')
    const line2 = container.querySelector('[data-line="2"]') as HTMLElement
    expect(line2.tagName).toBe('LI')
    expect(line2.textContent).toContain('risk one')
    expect(container.querySelector('[data-line="3"]')?.textContent).toContain('risk two')

    fireEvent.click(line2)
    expect(container.querySelector('.line-comment-form[data-comment-line="2"]')).not.toBeNull()

    const outsideLine = container.querySelector('[data-line="5"]') as HTMLElement
    expect(outsideLine.tagName).toBe('LI')
    expect(outsideLine.textContent).toContain('item a')

    fireEvent.click(outsideLine)
    expect(container.querySelector('.line-comment-form[data-comment-line="5"]')).not.toBeNull()
  })

  test('awaiting_approval: the hint row sits above the document and the action bar puts its hint before the buttons', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} banner={<div className="attention-banner" />} />)

    await waitFor(() => expect(container.querySelector('[data-line="1"]')).not.toBeNull())
    const section = container.querySelector('#plan-section') as HTMLElement
    const childClasses = Array.from(section.children).map((child) => child.id || child.className)
    expect(childClasses.slice(0, 3)).toEqual(['attention-banner', 'plan-hint-row', 'plan-content'])
    expect(container.querySelector('.plan-hint-row')?.textContent).toBe('Click any line to commentImages can be pasted into a comment')

    const bar = container.querySelector('#actions-section .actions-bar') as HTMLElement
    expect(Array.from(bar.children).map((child) => child.id || child.className)).toEqual(['comment-hint', 'actions-row'])
    expect(Array.from(bar.querySelectorAll('button')).map((button) => button.textContent?.replace(/✓.*$/, ''))).toEqual([
      'Request changes',
      'Approve plan',
    ])
  })

  test('approve(): posts to the approve endpoint and disables the button while in flight', async () => {
    let resolveApprove!: (value: Response) => void
    const approvePromise = new Promise<Response>((resolve) => {
      resolveApprove = resolve
    })
    const calls: string[] = []

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)

      if (url.endsWith('/approve')) return approvePromise
      if (url.endsWith('/plan')) return textResponse('Plan text')
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    const approveBtn = await screen.findByRole('button', { name: 'Approve plan' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(approveBtn)
    expect(approveBtn).toBeDisabled()
    expect(calls.some((c) => c.endsWith('/approve'))).toBe(true)

    resolveApprove({ ok: true } as Response)
    await waitFor(() => expect(approveBtn).not.toBeDisabled())
  })

  test('keeps Approve available when the plan fails to load (empty/non-OK)', async () => {
    // Регресс (codex F2): fallback-панель действий должна остаться, даже если
    // /plan вернул не-OK/пусто — иначе awaiting_approval-стадию нельзя одобрить.
    const calls: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)
      if (url.endsWith('/approve')) return { ok: true } as Response
      if (url.endsWith('/plan')) return { ok: false, status: 404, text: async () => '' } as unknown as Response
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    // Approve появляется после оседания неудачного fetch плана.
    const approveBtn = await screen.findByRole('button', { name: 'Approve plan' })
    expect(approveBtn).not.toBeDisabled()
    fireEvent.click(approveBtn)
    await waitFor(() => expect(calls.some((c) => c.endsWith('/approve'))).toBe(true))
  })

  test('Approve is disabled while a draft comment exists, and re-enables once it is removed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const approveBtn = screen.getByRole('button', { name: 'Approve plan' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please fix' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    expect(approveBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))

    expect(approveBtn).not.toBeDisabled()
  })

  test('Approve stays disabled with a visible reason after an unsaved draft is folded, and re-enables once it is removed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const approveBtn = screen.getByRole('button', { name: 'Approve plan' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'not saved yet' },
    })
    expect(approveBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(approveBtn).toBeDisabled()
    expect(container.querySelector('#comment-hint')?.textContent).toMatch(/send a revision or delete/i)
    expect(approveBtn.getAttribute('aria-describedby')).toBe('comment-hint')

    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))
    expect(approveBtn).not.toBeDisabled()
    expect(container.querySelector('#comment-hint')?.textContent).toBe('Comment on any line, then send a revision.')
  })

  test('Send revision is disabled while a new, empty comment form is open (even with an already-submitted comment), and re-enables once it is closed', async () => {
    // Bug repro: opening a comment form on ANOTHER line while a comment is already submitted must still
    // gate Send revision — the old `hasOpenDraft` let an open-but-empty form through.
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    // Submit a comment on line 1 so commentCount > 0.
    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please fix' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    const reviseBtn = screen.getByRole('button', { name: 'Send revision (1)' })
    expect(reviseBtn).not.toBeDisabled()

    // Open a NEW, empty comment form on line 2.
    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    expect(container.querySelector('.line-comment-form[data-comment-line="2"]')).not.toBeNull()
    expect(reviseBtn).toBeDisabled()

    // Close it without saving: Send revision re-enables.
    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 2' }))
    expect(reviseBtn).not.toBeDisabled()
  })

  test('Approve is disabled while an empty comment form is open with no submitted comments, and re-enables once it is closed', async () => {
    // Bug repro: opening a fresh, empty comment form on a plan with zero
    // submitted comments must still gate Approve.
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const approveBtn = screen.getByRole('button', { name: 'Approve plan' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('.line-comment-form[data-comment-line="1"]')).not.toBeNull()
    expect(approveBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(approveBtn).not.toBeDisabled()
  })

  test('sendRevision(): no-op without comments; posts feedback and clears comments once one exists', async () => {
    const calls: { url: string; body?: string }[] = []

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, body: init?.body as string | undefined })

      if (url.endsWith('/plan')) return textResponse('# First line\n# Second line')
      if (url.endsWith('/revise')) return { ok: true } as Response
      return textResponse('')
    })

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const reviseBtn = screen.getByRole('button', { name: 'Request changes' })
    expect(reviseBtn).toBeDisabled()

    // A disabled button does not dispatch a click in jsdom, so this exercises the no-op path.
    fireEvent.click(reviseBtn)
    expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(false)

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'please fix' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    const reviseBtnWithComment = screen.getByRole('button', { name: 'Send revision (1)' })
    expect(reviseBtnWithComment).not.toBeDisabled()

    fireEvent.click(reviseBtnWithComment)

    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(true))
    const reviseCall = calls.find((c) => c.url.endsWith('/revise'))
    expect(JSON.parse(reviseCall?.body ?? '{}')).toEqual({ feedback: 'Line 1: please fix' })

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled()
    })
  })

  test('the X on a saved comment removes it without opening the edit form', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    // Add a comment on line 1.
    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please fix' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

    // The comment is saved: display bubble present, revise button counts it.
    expect(container.querySelector('.line-comment-display')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Send revision (1)' })).not.toBeDisabled()

    // Click the X on the comment: it's removed and no edit form is opened.
    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))
    expect(container.querySelector('.line-comment-display')).toBeNull()
    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled()
  })

  test('a non-empty draft ignores clicks on other lines and on itself; × folds it into a Draft card', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

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
    expect(container.querySelector('.line-comment-form[data-comment-line="1"]')).not.toBeNull()
    expect(container.querySelector('.line-comment-form[data-comment-line="2"]')).toBeNull()

    // The × folds the editor; the text stays as a Draft card until it is removed explicitly.
    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(container.querySelector('.line-comment-form:not(.line-comment-display)')).toBeNull()
    expect(container.querySelector('.line-comment-display[data-comment-line="1"] .comment-text')?.textContent).toBe('in progress')
  })

  test('an empty draft still lets a row click switch to a different line', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('.line-comment-form[data-comment-line="1"]')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    expect(container.querySelector('.line-comment-form[data-comment-line="1"]')).toBeNull()
    expect(container.querySelector('.line-comment-form[data-comment-line="2"]')).not.toBeNull()
  })

  test('retry section is hidden unless the stage failed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container.querySelector('#retry-section')).toBeNull()
  })

  test('retry(): posts to the retry endpoint when the stage failed', async () => {
    const calls: string[] = []

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'failed' })} />)

    const retryBtn = await screen.findByRole('button', { name: 'Retry' })
    fireEvent.click(retryBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/retry'))).toBe(true))
  })

  test('retry(): a rejected request shows the reason and leaves Retry available', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.endsWith('/retry') ? ({ ok: false, status: 500 } as Response) : textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'failed' })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('/api/stages/s1/retry -> 500')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).not.toBeDisabled())
  })

  test('shows Retry and Skip buttons when the stage is hook_failed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

    const retryBtn = await screen.findByRole('button', { name: 'Retry' })
    const skipBtn = await screen.findByRole('button', { name: 'Skip' })
    expect(retryBtn).toBeInTheDocument()
    expect(skipBtn).toBeInTheDocument()
  })

  test('skip(): posts to the skip-hook endpoint when the stage is hook_failed', async () => {
    const calls: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

    const skipBtn = await screen.findByRole('button', { name: 'Skip' })
    fireEvent.click(skipBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/skip-hook'))).toBe(true))
  })

  test('retry section for hook_failed posts to retry-hook, not retry', async () => {
    const calls: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

    const retryBtn = await screen.findByRole('button', { name: 'Retry' })
    fireEvent.click(retryBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/retry-hook'))).toBe(true))
  })

  test('hook_failed: Skip and Retry stay disabled through success until a new episode', async () => {
    let finishRetry: (response: Response) => void = () => undefined
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (!url.endsWith('/retry-hook')) return textResponse('')
      return new Promise<Response>((resolve) => {
        finishRetry = resolve
      })
    })

    const old = makeStage({ status: 'hook_failed', updatedAt: '2026-10-29T10:00:00Z' })
    const { rerenderPanel } = renderPlanPanel(<PlanPanel stage={old} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(screen.getByRole('button', { name: 'Skip' })).toBeDisabled())
    await act(async () => finishRetry(textResponse('')))
    expect(screen.getByRole('button', { name: 'Skip' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled()
    rerenderPanel(<PlanPanel stage={{ ...old, updatedAt: '2026-10-29T10:01:00Z' }} />)
    expect(screen.getByRole('button', { name: 'Skip' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
  })

  test('hook_failed: a rejected retry-hook shows the reason and leaves Retry available', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.endsWith('/retry-hook') ? ({ ok: false, status: 500 } as Response) : textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('/api/stages/s1/retry-hook -> 500')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).not.toBeDisabled())
  })

  test('stage.status done: renders the plan with all checkboxes checked', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('- [ ] item one\n- [ ] item two'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'done' })} />)

    await waitFor(() => expect(container.querySelectorAll('.cb-done').length).toBe(2))
    expect(container.querySelectorAll('.cb-open').length).toBe(0)
  })

  test('autoApprove: true hides Approve/Revise and shows an Auto-approved badge instead', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Plan\n\nSome content'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval', autoApprove: true })} />)

    await waitFor(() => expect(container.querySelector('.auto-approved-badge')).not.toBeNull())
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve plan' })).not.toBeInTheDocument()
  })

  test('autoApprove: false keeps the normal Approve/Revise actions (no badge)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Plan'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval', autoApprove: false })} />)

    await waitFor(() => expect(container.querySelector('#actions-section')).not.toBeNull())
    expect(container.querySelector('.auto-approved-badge')).toBeNull()
  })

  test('the line-comment textarea offers the file-browser picker (Attach → Choose project file…)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)

    // Скрепка-меню (Finding #6): кнопка «Attach» раскрывает два пути, среди
    // которых «Choose project file…».
    fireEvent.click(screen.getByRole('button', { name: 'Attach' }))
    expect(screen.getByRole('menuitem', { name: /choose project file/i })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /upload image/i })).toBeInTheDocument()
  })

  // Disabled file_browser must hide the comment picker as well as the header action.
  test('capabilities.file_browser=false: the line-comment textarea offers Upload image but no project picker and never calls the files API (R2 #6a)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />, false)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)

    fireEvent.click(screen.getByRole('button', { name: 'Attach' }))
    expect(screen.getByRole('menuitem', { name: /upload image/i })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /choose project file/i })).toBeNull()
    const filesCalls = fetchSpy.mock.calls.filter(([input]) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.includes('/api/files/')
    })
    expect(filesCalls).toHaveLength(0)
  })

  describe('drafts across remounts and versions', () => {
    const PLAN = '# First line\n# Second line'

    async function renderAwaiting(plan = PLAN, stage = makeStage({ status: 'awaiting_approval' })) {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(plan))
      const view = renderPlanPanel(<PlanPanel stage={stage} />)
      await waitFor(() => expect(view.container.querySelector('[data-line]')).not.toBeNull())
      return view
    }

    function type(container: HTMLElement, line: number, text: string): void {
      fireEvent.click(container.querySelector(`[data-line="${line}"]`) as HTMLElement)
      fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: text } })
    }

    test('Plan → Feed → Plan restores the saved card and the open editor text', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'saved one')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
      type(container, 2, 'typing two')

      rerenderPanel(<div />)
      expect(container.querySelector('[data-line]')).toBeNull()
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

      expect(container.querySelector('.line-comment-display[data-comment-line="1"] .comment-text')?.textContent).toBe('saved one')
      const editor = container.querySelector('.line-comment-form:not(.line-comment-display)[data-comment-line="2"] textarea') as HTMLTextAreaElement
      expect(editor.value).toBe('typing two')
    })

    test('a stage switch keeps each stage drafts independent', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'for s1')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      rerenderPanel(<PlanPanel stage={makeStage({ id: 's2', status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.querySelector('.line-comment-display')).toBeNull())
      expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled()

      rerenderPanel(<PlanPanel stage={makeStage({ id: 's1', status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.querySelector('.line-comment-display[data-comment-line="1"]')).not.toBeNull())
    })

    test('a new plan text shows old comments as a previous-version notice, not attached to the new text', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'old note')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Rewritten\n# Second line'))
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'revising' })} />)
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.textContent).toContain('Rewritten'))

      expect(container.querySelector('.line-comment-display')).toBeNull()
      expect(screen.getByText('1 comment from previous version')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Approve plan' })).not.toBeDisabled()

      fireEvent.click(screen.getByRole('button', { name: 'Show' }))
      expect(screen.getByText('old note')).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
      expect(screen.queryByText('1 comment from previous version')).toBeNull()
    })

    test('no previous-version notice while the plan reloads after a remount', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'live note')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      rerenderPanel(<div />)
      vi.spyOn(globalThis, 'fetch').mockReturnValue(new Promise<Response>(() => {}))
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

      expect(container.querySelector('.stale-comments')).toBeNull()
    })

    test('a failed plan fetch shows the live comments as unsent, not as a previous version, until they are discarded', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'unsent feedback')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      rerenderPanel(<div />)
      vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 404 } as Response)
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

      await waitFor(() => expect(screen.getByRole('button', { name: 'Approve plan' })).toBeDisabled())
      expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled()
      expect(screen.queryByText(/previous version/)).toBeNull()
      expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
      expect(screen.getByText('1 unsent comment - the plan could not be loaded')).toBeInTheDocument()
      expect(screen.getByText(/discard them to approve|discard it to approve/)).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
      expect(screen.queryByText(/unsent comment/)).toBeNull()
      expect(screen.getByRole('button', { name: 'Approve plan' })).not.toBeDisabled()
    })

    test('a network error on the plan fetch also uses the unsent label', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      type(container, 1, 'one')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      rerenderPanel(<div />)
      vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network down'))
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

      expect(await screen.findByText('1 unsent comment - the plan could not be loaded')).toBeInTheDocument()
      expect(screen.queryByText(/previous version/)).toBeNull()
    })

    test('Plan → Feed → Plan restores the plan scroll position of the same text', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      const section = container.querySelector('#plan-section') as HTMLElement
      section.scrollTop = 240
      fireEvent.scroll(section)

      rerenderPanel(<div />)
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.querySelector('[data-line]')).not.toBeNull())

      expect((container.querySelector('#plan-section') as HTMLElement).scrollTop).toBe(240)
    })

    test('a new plan text opens at the top, not at the old scroll position', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      const section = container.querySelector('#plan-section') as HTMLElement
      section.scrollTop = 240
      fireEvent.scroll(section)

      rerenderPanel(<div />)
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Rewritten\n# Second line'))
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.textContent).toContain('Rewritten'))

      expect((container.querySelector('#plan-section') as HTMLElement).scrollTop).toBe(0)
    })

    test('comments on a blank line and a paragraph continuation are sent with their own line numbers', async () => {
      const plan = '# Title\n\nfirst line\nsecond line'
      const calls: { url: string; body?: string }[] = []
      const { container } = await renderAwaiting(plan)
      vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        calls.push({ url, body: init?.body as string | undefined })
        return url.endsWith('/revise') ? ({ ok: true } as Response) : textResponse(plan)
      })
      for (const [line, text] of [[4, 'tighten this'], [2, 'add a summary']] as const) {
        type(container, line, text)
        fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
      }

      fireEvent.click(screen.getByRole('button', { name: 'Send revision (2)' }))

      await waitFor(() => expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(true))
      expect(JSON.parse(calls.find((c) => c.url.endsWith('/revise'))?.body ?? '{}')).toEqual({
        feedback: 'Line 2: add a summary\n\nLine 4: tighten this',
      })
    })

    test('a comment on a blank line stays bound to the old version when the plan text changes', async () => {
      const { container, rerenderPanel } = await renderAwaiting('# Title\n\nbody')
      type(container, 2, 'blank-line note')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Title\n\nrewritten body'))
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'revising' })} />)
      rerenderPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.textContent).toContain('rewritten body'))

      expect(container.querySelector('.line-comment-display')).toBeNull()
      expect(container.querySelector('[data-line="2"]')?.classList.contains('has-comment')).toBe(false)
      expect(screen.getByText('1 comment from previous version')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled()
    })

    test('Send revision uses the latest text of a folded unsaved draft', async () => {
      const calls: { url: string; body?: string }[] = []
      const { container } = await renderAwaiting()
      vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        calls.push({ url, body: init?.body as string | undefined })
        return url.endsWith('/revise') ? ({ ok: true } as Response) : textResponse(PLAN)
      })
      type(container, 1, 'draft only')
      fireEvent.keyDown(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { key: 'Escape' })

      fireEvent.click(screen.getByRole('button', { name: 'Send revision (1)' }))

      await waitFor(() => expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(true))
      expect(JSON.parse(calls.find((c) => c.url.endsWith('/revise'))?.body ?? '{}')).toEqual({ feedback: 'Line 1: draft only' })
    })

    test('a failed revision keeps the comments and lets the user send them again', async () => {
      const { container } = await renderAwaiting()
      vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        return url.endsWith('/revise') ? ({ ok: false, status: 500 } as Response) : textResponse(PLAN)
      })
      type(container, 1, 'keep me')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      fireEvent.click(screen.getByRole('button', { name: 'Send revision (1)' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('Failed to send revision')

      expect(container.querySelector('.line-comment-display[data-comment-line="1"] .comment-text')?.textContent).toBe('keep me')
      await waitFor(() => expect(screen.getByRole('button', { name: 'Send revision (1)' })).not.toBeDisabled())
    })

    test('a revision rejected after switching to another stage shows no error there', async () => {
      const { container, rerenderPanel } = await renderAwaiting()
      let rejectRevise!: (r: Response) => void
      vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        if (url.endsWith('/revise')) return new Promise<Response>((resolve) => { rejectRevise = resolve })
        return Promise.resolve(textResponse(PLAN))
      })
      type(container, 1, 'for s1')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
      fireEvent.click(screen.getByRole('button', { name: 'Send revision (1)' }))

      rerenderPanel(<PlanPanel stage={makeStage({ id: 's2', status: 'awaiting_approval' })} />)
      await waitFor(() => expect(container.querySelector('[data-line]')).not.toBeNull())
      rejectRevise({ ok: false, status: 500 } as Response)

      await waitFor(() => expect(screen.getByRole('button', { name: 'Request changes' })).toBeDisabled())
      await new Promise((resolve) => setTimeout(resolve, 0))
      expect(screen.queryByRole('alert')).toBeNull()
    })

    test('an accepted revision clears only the captured text; edits made during the request stay', async () => {
      const { container } = await renderAwaiting()
      let acceptRevise!: (r: Response) => void
      vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
        const url = typeof input === 'string' ? input : (input as Request).url
        if (url.endsWith('/revise')) return new Promise<Response>((resolve) => { acceptRevise = resolve })
        return Promise.resolve(textResponse(PLAN))
      })
      type(container, 1, 'first')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))
      type(container, 2, 'second')
      fireEvent.click(screen.getByRole('button', { name: 'Save comment' }))

      fireEvent.click(screen.getByRole('button', { name: 'Send revision (2)' }))
      fireEvent.click(screen.getByRole('button', { name: 'Edit comment on line 2' }))
      fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, { target: { value: 'second, continued' } })
      await act(async () => acceptRevise({ ok: true } as Response))

      await waitFor(() => expect(container.querySelector('.line-comment-display[data-comment-line="1"]')).toBeNull())
      expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('second, continued')
    })
  })

  // Keep before the scrollHeight spy: this jsdom/tinyspy version fails to restore its getter for later
  // textarea renders.
  test('the comment textarea grows to fit its content via the auto-grow hook', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(150)

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer comment' } })

    expect(textarea.style.height).toBe('150px')

    scrollHeightSpy.mockRestore()
  })

  test('paused, pending: shows the pending-specific reason and a Continue button', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'pending' })} />)

    expect(await screen.findByText(/before its first run/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
  })

  test('paused, running: shows the running-specific reason', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'running' })} />)

    expect(await screen.findByText(/manually paused while it was running/i)).toBeInTheDocument()
  })

  test('paused, retrying: shows the retrying-specific reason', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'retrying' })} />)

    expect(await screen.findByText(/waiting to retry/i)).toBeInTheDocument()
  })

  test('Continue: posts to the continue endpoint', async () => {
    const calls: string[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push(url)
      return textResponse('')
    })

    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'pending' })} />)

    const continueBtn = await screen.findByRole('button', { name: 'Continue' })
    fireEvent.click(continueBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/continue'))).toBe(true))
  })

  test('Continue: uses the Hold owner when supplied instead of issuing a direct request', async () => {
    const api = await import('../../api/run-client')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
    const direct = vi.spyOn(api, 'continueStage').mockResolvedValue(undefined)
    const heldContinue = vi.fn()
    renderPlanPanel(<PlanPanel stage={makeStage({ status: 'paused' })} onContinue={heldContinue} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    expect(heldContinue).toHaveBeenCalledOnce()
    expect(direct).not.toHaveBeenCalled()
  })

  test.each([
    ['paused', 'Continue', 'continueStage'],
    ['hook_failed', 'Skip', 'skipHookStage'],
  ] as const)('%s: %s exposes rejection and allows retry, then locks through success', async (status, label, endpoint) => {
    const api = await import('../../api/run-client')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
    const request = vi.spyOn(api, endpoint).mockRejectedValueOnce(new Error('Action refused')).mockResolvedValue(undefined)
    const old = makeStage({ status, updatedAt: '2026-10-29T10:00:00Z' })
    const { rerenderPanel } = renderPlanPanel(<PlanPanel stage={old} />)
    fireEvent.click(await screen.findByRole('button', { name: label }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Action refused')
    expect(screen.getByRole('button', { name: label })).toBeEnabled()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: label })) })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('button', { name: label })).toBeDisabled()
    expect(request).toHaveBeenCalledTimes(2)
    rerenderPanel(<PlanPanel stage={{ ...old, updatedAt: '2026-10-29T10:01:00Z' }} />)
    expect(screen.getByRole('button', { name: label })).toBeEnabled()
  })

  describe('usage-limit wait and quota pauses', () => {
    const quota: StageQuota = {
      provider: 'claude', window: 'five_hour', source: 'structured', until: '2026-10-23T17:02:06Z', resetAt: '2026-10-23T17:02:00Z', pauseReason: '',
    }
    // Хост приложения живёт в UTC+0: абсолютное время берётся из контекста зоны хоста, а не браузера.
    function renderInHostZone(ui: ReactElement) {
      return renderPlanPanel(<MessageTimeZoneProvider timeZone="UTC" utcOffsetMinutes={0}>{ui}</MessageTimeZoneProvider>)
    }
    const pausedFor = (pauseReason: StageQuota['pauseReason'], extra: Partial<StageQuota> = {}) =>
      makeStage({ status: 'paused', pausedFrom: 'awaiting_quota', quota: { ...quota, pauseReason, ...extra } })

    afterEach(() => {
      vi.useRealTimers()
    })

    test('awaiting_quota: shows "Usage limit", the absolute resume time, a countdown and how to retry sooner — no Continue', async () => {
      vi.useFakeTimers({ now: new Date('2026-10-23T16:50:00Z'), toFake: ['Date', 'setInterval', 'clearInterval'] })
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const { container } = renderInHostZone(<PlanPanel stage={makeStage({ status: 'awaiting_quota', quota })} />)
      const section = container.querySelector('#quota-section') as HTMLElement
      expect(within(section).getByText('Usage limit')).toBeInTheDocument()
      expect(section.querySelector('time')).toHaveTextContent('Resumes at Oct 23, 17:02')
      expect(section.querySelector('.quota-countdown')).toHaveTextContent('12m 06s')
      expect(within(section).getByText(/pause the stage/i)).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
      expect(container.querySelector('#paused-section')).toBeNull()
      act(() => { vi.advanceTimersByTime(1000) })
      expect(section.querySelector('.quota-countdown')).toHaveTextContent('12m 05s')
    })

    test('awaiting_quota countdown freezes when the status poll is stale', () => {
      vi.useFakeTimers({ now: new Date('2026-10-23T16:50:00Z'), toFake: ['Date', 'setInterval', 'clearInterval'] })
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const stage = makeStage({ status: 'awaiting_quota', quota })
      const { container, rerender } = renderPlanPanel(
        <MessageTimeZoneProvider timeZone="UTC" utcOffsetMinutes={0}><PlanPanel stage={stage} statusAvailable /></MessageTimeZoneProvider>,
      )
      rerender(
        <CommentProvider runId="run-1">
          <FileBrowserProvider flowName="flow1" startedAt="t1" enabled>
            <MessageTimeZoneProvider timeZone="UTC" utcOffsetMinutes={0}><PlanPanel stage={stage} statusAvailable={false} /></MessageTimeZoneProvider>
          </FileBrowserProvider>
        </CommentProvider>,
      )
      act(() => { vi.advanceTimersByTime(30_000) })
      expect(container.querySelector('.quota-countdown')).toHaveTextContent('12m 06s')
    })

    test('probe source says "Next check"', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const { container } = renderInHostZone(<PlanPanel stage={makeStage({ status: 'awaiting_quota', quota: { ...quota, source: 'probe', window: '', resetAt: '' } })} />)
      expect(container.querySelector('#quota-section time')).toHaveTextContent('Next check at Oct 23, 17:02')
    })

    test('paused exceeds_max_wait: says the reset is later than the wait limit, with the time, and Continue explains the immediate probe', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const { container } = renderInHostZone(<PlanPanel stage={pausedFor('exceeds_max_wait', { until: '', resetAt: '2026-10-24T05:00:00Z' })} />)
      const section = container.querySelector('#paused-section') as HTMLElement
      expect(within(section).getByText('Usage limit')).toBeInTheDocument()
      expect(await within(section).findByText(/resets at Oct 24, 05:00.*later than the maximum wait/i)).toBeInTheDocument()
      expect(within(section).getByText(/Continue.*once, right now, with a fresh session/i)).toBeInTheDocument()
      expect(within(section).getByRole('button', { name: 'Continue' })).toBeInTheDocument()
      expect(container.textContent).not.toMatch(/paused while it was running/i)
      expect(container.querySelector('.quota-countdown')).toBeNull()
    })

    test('paused no_reset: "Quota exhausted" with no time and no countdown', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const { container } = renderInHostZone(<PlanPanel stage={pausedFor('no_reset', { source: 'text', until: '', resetAt: '' })} />)
      const section = container.querySelector('#paused-section') as HTMLElement
      expect(within(section).getByText('Quota exhausted')).toBeInTheDocument()
      expect(within(section).getByText(/no reset time/i)).toBeInTheDocument()
      expect(section.querySelector('time')).toBeNull()
      expect(section.querySelector('.quota-countdown')).toBeNull()
    })

    test('paused max_waits: names the consecutive-limit reason', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      renderInHostZone(<PlanPanel stage={pausedFor('max_waits')} />)
      expect(screen.getByText(/several times in a row/i)).toBeInTheDocument()
    })

    test('manual pause of a parked stage (no pause_reason): says it was paused while waiting for the limit', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      const { container } = renderInHostZone(<PlanPanel stage={pausedFor('')} />)
      expect(screen.getByText(/manually paused while waiting for the claude usage limit/i)).toBeInTheDocument()
      expect(container.textContent).toMatch(/Continue.*once, right now, with a fresh session/i)
      expect(container.textContent).not.toMatch(/paused while it was running/i)
    })

    test('a quota pause without the quota object (older server) still gets a quota-specific reason', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))
      renderInHostZone(<PlanPanel stage={makeStage({ status: 'paused', pausedFrom: 'awaiting_quota' })} />)
      expect(screen.getByText(/manually paused while waiting for the usage limit/i)).toBeInTheDocument()
    })
  })

  test('stage=null: renders the panel shell without fetching or crashing', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = renderPlanPanel(<PlanPanel stage={null} />)

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(container.querySelector('#plan-empty')).not.toBeNull()
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(container.querySelector('#retry-section')).toBeNull()
  })
})
