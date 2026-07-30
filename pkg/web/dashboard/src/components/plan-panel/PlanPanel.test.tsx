import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { PlanPanel } from './PlanPanel'

function makeStage(overrides: Partial<Stage> = {}): Stage {
  return { id: 's1', name: 'Stage', status: 'running', updatedAt: '', interactive: false, autonomous: false, ...overrides }
}

function textResponse(text: string): Response {
  return { ok: true, text: async () => text } as Response
}

describe('PlanPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('normal mode: fetches the plan and renders it as markdown', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('# Hello\n\nWorld'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(container.querySelector('.md')).not.toBeNull())
    expect(container.querySelector('.md')?.innerHTML).toContain('Hello')
    expect(container.querySelector('#actions-section')).toBeNull()
    expect(container.querySelector('#retry-section')).toBeNull()
  })

  test('awaiting_approval: renders review lines with line numbers and opens a comment form on click', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('First line\nSecond line'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

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

    render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)

    const approveBtn = await screen.findByRole('button', { name: 'Approve' })
    expect(approveBtn).not.toBeDisabled()

    fireEvent.click(approveBtn)
    expect(approveBtn).toBeDisabled()
    expect(calls.some((c) => c.endsWith('/approve'))).toBe(true)

    resolveApprove({ ok: true } as Response)
    await waitFor(() => expect(approveBtn).not.toBeDisabled())
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

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
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

    const { container } = render(<PlanPanel stage={makeStage({ status: 'awaiting_approval' })} />)
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

  test('retry section is hidden unless the stage failed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'running' })} />)

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

    render(<PlanPanel stage={makeStage({ status: 'failed' })} />)

    const retryBtn = await screen.findByRole('button', { name: 'Retry' })
    fireEvent.click(retryBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/retry'))).toBe(true))
  })

  test('shows Retry and Skip buttons when the stage is hook_failed', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse(''))

    render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

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

    render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

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

    render(<PlanPanel stage={makeStage({ status: 'hook_failed' })} />)

    const retryBtn = await screen.findByRole('button', { name: 'Retry' })
    fireEvent.click(retryBtn)

    await waitFor(() => expect(calls.some((c) => c.endsWith('/retry-hook'))).toBe(true))
  })

  test('stage.status done: renders the plan with all checkboxes checked', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(textResponse('- [ ] item one\n- [ ] item two'))

    const { container } = render(<PlanPanel stage={makeStage({ status: 'done' })} />)

    await waitFor(() => expect(container.querySelectorAll('.cb-done').length).toBe(2))
    expect(container.querySelectorAll('.cb-open').length).toBe(0)
  })
})
