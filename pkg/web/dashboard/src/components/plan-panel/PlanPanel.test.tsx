import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
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

// PlanPanel's line-comment textarea now renders with allowFileReferences
// (Task 14) — its "Attach project file" button calls useFileBrowser(), which
// throws outside a FileBrowserProvider. Every render in this file goes
// through this wrapper, same as App.tsx wraps the whole dashboard in prod.
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

  test('awaiting_approval: renders review lines with line numbers and opens a comment form on click', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    expect(line1.querySelector('.line-num')?.textContent).toBe('1')
    expect(container.querySelector('.line-comment-form')).toBeNull()

    fireEvent.click(line1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
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

  test('Approve is disabled while a draft comment exists, and re-enables once it is removed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
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

  test('Approve is disabled while a draft comment is open but unsaved, and re-enables once the form is closed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

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

  test('sendRevision(): no-op without comments; posts feedback and clears comments once one exists', async () => {
    const calls: { url: string; body?: string }[] = []

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, body: init?.body as string | undefined })

      if (url.endsWith('/plan')) return textResponse('First line\nSecond line')
      if (url.endsWith('/revise')) return { ok: true } as Response
      return textResponse('')
    })

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

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
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

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
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
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

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).toBeNull()
    expect(container.querySelector('[data-line="2"] .line-comment-form')).not.toBeNull()
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

  test('the line-comment textarea offers the file-browser picker (Attach project file)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)

    expect(screen.getByRole('button', { name: /attach project file/i })).toBeInTheDocument()
  })

  // Finding 5: capabilities.file_browser=false must hide the comment picker
  // too, not just the header button — otherwise host mode still shows
  // "Attach project file" and clicking it hits the disabled /api/files/*.
  test('capabilities.file_browser=false: the line-comment textarea has no Attach project file button and never calls the files API', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />, false)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)

    expect(screen.queryByRole('button', { name: /attach project file/i })).not.toBeInTheDocument()
    const filesCalls = fetchSpy.mock.calls.filter(([input]) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.includes('/api/files/')
    })
    expect(filesCalls).toHaveLength(0)
  })

  // Kept before the scrollHeight-spying test below, not after: vi.spyOn(...,
  // 'get').mockRestore() on this jsdom/tinyspy version does not fully restore
  // the accessor on window.HTMLTextAreaElement.prototype — any later test in
  // this file that renders a textarea (useAutoGrowTextarea reads scrollHeight
  // on every render) hits a corrupted getter that recurses into itself and
  // blows the call stack (see the identical note in PasteableTextarea.test.tsx).
  test('the comment textarea grows to fit its content via the auto-grow hook', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))
    const scrollHeightSpy = vi
      .spyOn(window.HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockReturnValue(150)

    const { container } = renderPlanPanel(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

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

  test('stage=null: renders the panel shell without fetching or crashing', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = renderPlanPanel(<PlanPanel stage={null} />)

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(container.querySelector('#plan-empty')).not.toBeNull()
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(container.querySelector('#retry-section')).toBeNull()
  })
})
