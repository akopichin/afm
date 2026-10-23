import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import type { Stage, StageQuota } from '../../types'
import { PlanPanel } from './PlanPanel'
import { FileBrowserProvider } from '../file-browser'

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

// PlanPanel's line-comment textarea now renders with allowFileReferences — its "Attach project file"
// button calls useFileBrowser, which throws outside a FileBrowserProvider.
function renderPlanPanel(ui: ReactElement, enabled = true) {
  return render(
    <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={enabled}>
      {ui}
    </FileBrowserProvider>,
  )
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
    // No number gutter anymore; the "Line N" affordance is the anchor's title.
    expect(line1.getAttribute('title')).toBe('Comment on line 1')
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

  test('awaiting_approval: a special section plus a list renders both correctly, and each stays commentable (block segmentation)', async () => {
    // Список внутри спецсекции и после неё рендерятся целыми <ul>-блоками, а КАЖДЫЙ пункт <li> заякорен на
    // своей строке.
    const plan = ['## Assumptions', '- risk one', '- risk two', '## Next', '- item a', '- item b'].join('\n')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(plan))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    await waitFor(() => expect(container.querySelector('.plan-section-wrapper.plan-section-assumptions')).not.toBeNull())

    // Секция Assumptions: заголовок — настоящая кнопка; список внутри — целый
    // <ul>; первый пункт заякорен на строке 2 (сам <li>, а не <ul>).
    const section = container.querySelector('.plan-section-wrapper.plan-section-assumptions') as HTMLElement
    expect(section.querySelector('button.section-header')?.textContent).toContain('Assumptions')
    expect(section.querySelector('.md ul')).not.toBeNull()
    const sectionLine = section.querySelector('[data-line="2"]') as HTMLElement
    expect(sectionLine).not.toBeNull()
    expect(sectionLine.tagName).toBe('LI')
    expect(sectionLine.textContent).toContain('risk one')
    expect(section.querySelector('[data-line="3"]')?.textContent).toContain('risk two')

    // Список внутри секции остаётся комментируемым: форма — sibling внутри
    // тела секции, помеченный data-comment-line.
    fireEvent.click(sectionLine)
    expect(section.querySelector('.line-comment-form[data-comment-line="2"]')).not.toBeNull()

    // Список ПОСЛЕ секции (## Next закрывает Assumptions) — отдельный блок вне
    // обёртки; первый пункт заякорен на строке 5.
    const outsideLine = container.querySelector('[data-line="5"]') as HTMLElement
    expect(outsideLine).not.toBeNull()
    expect(outsideLine.closest('.plan-section-wrapper')).toBeNull()
    expect(outsideLine.tagName).toBe('LI')
    expect(outsideLine.textContent).toContain('item a')

    fireEvent.click(outsideLine)
    expect(container.querySelector('.line-comment-form[data-comment-line="5"]')).not.toBeNull()
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

    const approveBtn = await screen.findByRole('button', { name: 'Approve' })
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
    const approveBtn = await screen.findByRole('button', { name: 'Approve' })
    expect(approveBtn).not.toBeDisabled()
    fireEvent.click(approveBtn)
    await waitFor(() => expect(calls.some((c) => c.endsWith('/approve'))).toBe(true))
  })

  test('Approve is disabled while a draft comment exists, and re-enables once it is removed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

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

  test('Approve is disabled while a draft comment is open but unsaved, and re-enables once the form is closed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# First line\n# Second line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('[data-line]').length).toBe(2))

    const approveBtn = screen.getByRole('button', { name: 'Approve' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'not saved yet' },
    })

    expect(approveBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))

    expect(approveBtn).not.toBeDisabled()
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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

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

    const approveBtn = screen.getByRole('button', { name: 'Approve' })
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

    const reviseBtn = screen.getByRole('button', { name: 'Send revision' })
    expect(reviseBtn).toBeDisabled()

    // A disabled button does not dispatch a click in jsdom, so this exercises the no-op path.
    fireEvent.click(reviseBtn)
    expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(false)

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'please fix' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    const reviseBtnWithComment = screen.getByRole('button', { name: 'Send revision (1)' })
    expect(reviseBtnWithComment).not.toBeDisabled()

    fireEvent.click(reviseBtnWithComment)

    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/revise'))).toBe(true))
    const reviseCall = calls.find((c) => c.url.endsWith('/revise'))
    expect(JSON.parse(reviseCall?.body ?? '{}')).toEqual({ feedback: 'Line 1: please fix' })

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Send revision' })).toBeDisabled()
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
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    // The comment is saved: display bubble present, revise button counts it.
    expect(container.querySelector('.line-comment-display')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Send revision (1)' })).not.toBeDisabled()

    // Click the X on the comment: it's removed and no edit form is opened.
    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))
    expect(container.querySelector('.line-comment-display')).toBeNull()
    expect(container.querySelector('.line-comment-form')).toBeNull()
    expect(screen.getByRole('button', { name: 'Send revision' })).toBeDisabled()
  })

  test('a non-empty draft ignores clicks on other lines and on itself; only × discards it', async () => {
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

    // Only the × discards it.
    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(container.querySelector('.line-comment-form')).toBeNull()
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
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
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
        <FileBrowserProvider flowName="flow1" startedAt="t1" enabled>
          <MessageTimeZoneProvider timeZone="UTC" utcOffsetMinutes={0}><PlanPanel stage={stage} statusAvailable={false} /></MessageTimeZoneProvider>
        </FileBrowserProvider>,
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
