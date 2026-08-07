import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { MaximizeProvider } from '../layout/Maximizable'
import { DialogChannel } from './DialogChannel'

// jumpToBottom заспайен на уровне модуля (не через spyOn существующего хука, у которого
// в jsdom scrollHeight всегда 0 и реальный скролл незаметен) — так тест #7 может
// проверить сам факт вызова при переходе панели в maximized, не завися от layout jsdom.
const mockJumpToBottom = vi.fn()

vi.mock('../../hooks/use-stick-to-bottom', () => ({
  useStickToBottom: () => ({ ref: { current: null }, stick: true, jumpToBottom: mockJumpToBottom }),
}))

type RawDialogEntry = {
  id?: string
  phase?: string
  question?: string
  answer?: string | null
  options?: string[]
  allow_custom?: boolean
  type?: string
}

type FetchCall = { url: string; method: string; body?: string }

function jsonResponse(data: unknown): Response {
  return { ok: true, json: async () => data } as Response
}

function makeStage(overrides: Partial<Stage> = {}): Stage {
  return { id: 's1', name: 'Stage', status: 'awaiting_user_input', updatedAt: '', interactive: true, autonomous: false, autoApprove: false, ...overrides }
}

describe('DialogChannel', () => {
  beforeEach(() => {
    mockJumpToBottom.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('hasContent=false: no entries and status not awaiting_user_input renders nothing', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([]))

    const stage = makeStage({ status: 'running' })
    const { container } = render(<DialogChannel stage={stage} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('pending question with options and an enabled free-input field when allow_custom is true', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    expect(screen.getByRole('button', { name: 'Beta' })).toBeInTheDocument()

    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    expect(textarea).not.toBeDisabled()
  })

  test('новый pending-вопрос даёт one-shot класс dialog-flash', async () => {
    const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    const { container } = render(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelector('#dialog-pending.dialog-flash')).not.toBeNull())
  })

  test('free-input field is disabled when allow_custom is false', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha'],
      allow_custom: false,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    expect(textarea).toBeDisabled()
  })

  test('selecting an option and sending posts from_options:true and reloads the dialog', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))
    fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(true)
    })

    const answerCall = calls.find((c) => c.url.endsWith('/dialog/answer'))
    const body = JSON.parse(answerCall?.body ?? '{}') as Record<string, unknown>
    expect(body).toMatchObject({ id: 'q1', answer: 'Alpha', from_options: true })

    await waitFor(() => {
      const getCalls = calls.filter((c) => c.method === 'GET' && c.url.endsWith('/dialog'))
      expect(getCalls.length).toBeGreaterThanOrEqual(2)
    })
  })

  test('▸ SEND без выбора не показывает success-морф (нет класса ok)', async () => {
    const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    render(<DialogChannel stage={makeStage()} />)
    const send = await screen.findByRole('button', { name: '▸ SEND' })
    fireEvent.click(send)
    expect(send.className).not.toContain('ok')
  })

  test('free text takes priority over a previously selected option (from_options:false)', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))

    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'my own answer' } })

    fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(true)
    })

    const answerCall = calls.find((c) => c.url.endsWith('/dialog/answer'))
    const body = JSON.parse(answerCall?.body ?? '{}') as Record<string, unknown>
    expect(body).toMatchObject({ answer: 'my own answer', from_options: false })
  })

  test('Ctrl+Enter in the custom textarea sends the answer', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'ctrl enter answer' } })
    fireEvent.keyDown(textarea, { key: 'Enter', ctrlKey: true })

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(true)
    })

    const answerCall = calls.find((c) => c.url.endsWith('/dialog/answer'))
    const body = JSON.parse(answerCall?.body ?? '{}') as Record<string, unknown>
    expect(body).toMatchObject({ answer: 'ctrl enter answer', from_options: false })
  })

  test('Cmd+Enter (metaKey) in the custom textarea sends the answer', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'cmd enter answer' } })
    fireEvent.keyDown(textarea, { key: 'Enter', metaKey: true })

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(true)
    })

    const answerCall = calls.find((c) => c.url.endsWith('/dialog/answer'))
    const body = JSON.parse(answerCall?.body ?? '{}') as Record<string, unknown>
    expect(body).toMatchObject({ answer: 'cmd enter answer', from_options: false })
  })

  test('Enter without Ctrl/Cmd in the custom textarea does not send', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha', 'Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'plain enter' } })
    fireEvent.keyDown(textarea, { key: 'Enter' })

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
    expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(false)
  })

  test('clicking a question line opens a comment form', async () => {
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
    expect(container.querySelector('.line-comment-form')).toBeNull()

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    fireEvent.click(line1)

    expect(container.querySelector('.line-comment-form')).not.toBeNull()
  })

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

  test('adding a comment hides options+textarea and shows Send feedback; deleting the only comment restores them', async () => {
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
    expect(screen.getByRole('button', { name: 'Alpha' })).toBeInTheDocument()
    expect(container.querySelector('textarea.dialog-custom')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const commentTextarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(commentTextarea, { target: { value: 'please clarify' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    expect(screen.queryByRole('button', { name: 'Alpha' })).toBeNull()
    expect(container.querySelector('textarea.dialog-custom')).toBeNull()
    expect(screen.queryByRole('button', { name: '▸ SEND' })).toBeNull()
    const sendFeedbackBtn = screen.getByRole('button', { name: 'Send feedback (1)' })
    expect(sendFeedbackBtn).toBeInTheDocument()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

    expect(screen.queryByRole('button', { name: /Send feedback/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Alpha' })).toBeInTheDocument()
    expect(container.querySelector('textarea.dialog-custom')).not.toBeNull()
  })

  test('the X on a saved comment removes it without opening the edit form', async () => {
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
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'please clarify' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    expect(container.querySelector('.line-comment-display')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Send feedback (1)' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 1' }))

    expect(container.querySelector('.line-comment-display')).toBeNull()
    expect(container.querySelector('.line-comment-form')).toBeNull()
    // Removing the last comment flips the action back from "Send feedback" to
    // the normal answer UI: options + the ▸ SEND button reappear.
    expect(screen.queryByRole('button', { name: /Send feedback/ })).toBeNull()
    expect(screen.getByRole('button', { name: '▸ SEND' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Alpha' })).toBeInTheDocument()
    expect(container.querySelector('textarea.dialog-custom')).not.toBeNull()
  })

  test('Send feedback posts comments as the answer with from_options:false', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="2"]') as HTMLElement)
    const commentTextarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(commentTextarea, { target: { value: 'please clarify this' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    fireEvent.click(screen.getByRole('button', { name: 'Send feedback (1)' }))

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(true)
    })

    const answerCall = calls.find((c) => c.url.endsWith('/dialog/answer'))
    const body = JSON.parse(answerCall?.body ?? '{}') as Record<string, unknown>
    expect(body).toMatchObject({ id: 'q1', phase: 'p1', from_options: false })
    expect(body.answer as string).toContain('please clarify this')
    expect(body.answer as string).toContain('Line 2:')
    expect(body.answer as string).toContain('Second line')
  })

  test('Ctrl+Enter in a comment textarea saves the comment instead of sending feedback', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })

      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })

    const { container } = render(<DialogChannel stage={makeStage()} />)

    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const commentTextarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(commentTextarea, { target: { value: 'ctrl enter comment' } })
    fireEvent.keyDown(commentTextarea, { key: 'Enter', ctrlKey: true })

    expect(calls.some((c) => c.url.endsWith('/dialog/answer'))).toBe(false)
    expect(screen.getByRole('button', { name: 'Send feedback (1)' })).toBeInTheDocument()
  })

  test('cancel(): confirmed cancellation posts to the cancel endpoint', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = { id: 'q1', phase: 'p1', question: 'Pick one', answer: null, options: [], allow_custom: true }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET' })

      if (url.endsWith('/dialog/cancel')) return { ok: true } as Response
      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'CANCEL STAGE' })
    fireEvent.click(screen.getByRole('button', { name: 'CANCEL STAGE' }))

    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith('/dialog/cancel'))).toBe(true)
    })
  })

  test('cancel(): declining the confirmation does not call fetch for cancellation', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = { id: 'q1', phase: 'p1', question: 'Pick one', answer: null, options: [], allow_custom: true }

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      calls.push({ url, method: init?.method ?? 'GET' })

      if (url.endsWith('/dialog')) return jsonResponse([pending])
      return jsonResponse([])
    })
    vi.spyOn(window, 'confirm').mockReturnValue(false)

    render(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'CANCEL STAGE' })
    fireEvent.click(screen.getByRole('button', { name: 'CANCEL STAGE' }))

    // Give any (incorrect) fetch call a chance to happen before asserting its absence.
    // Wrapped in act: a pending requestAnimationFrame (auto-scroll-to-pending-question)
    // may still flush a state update while we wait.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
    expect(calls.some((c) => c.url.endsWith('/dialog/cancel'))).toBe(false)
  })

  test('loadDialog: a rejected fetch leaves entries empty and does not crash', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network down'))

    const { container } = render(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('loadDialog: response.ok=false leaves entries empty and does not crash', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false } as Response)

    const { container } = render(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('loadDialog: a non-array JSON payload leaves entries empty and does not crash', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ not: 'an array' }))

    const { container } = render(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('maximizing the panel scrolls the feed to the bottom via jumpToBottom', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([]))

    render(
      <MaximizeProvider>
        <DialogChannel stage={makeStage()} />
      </MaximizeProvider>,
    )

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    mockJumpToBottom.mockClear()

    const maximizeButton = await screen.findByRole('button', { name: 'Expand' })
    fireEvent.click(maximizeButton)

    await waitFor(() => expect(mockJumpToBottom).toHaveBeenCalled())
  })

  test('un-maximizing the panel does not call jumpToBottom again', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([]))

    render(
      <MaximizeProvider>
        <DialogChannel stage={makeStage()} />
      </MaximizeProvider>,
    )

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())

    const maximizeButton = await screen.findByRole('button', { name: 'Expand' })
    fireEvent.click(maximizeButton)
    await waitFor(() => expect(mockJumpToBottom).toHaveBeenCalled())
    mockJumpToBottom.mockClear()

    const restoreButton = await screen.findByRole('button', { name: 'Collapse' })
    fireEvent.click(restoreButton)

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
    expect(mockJumpToBottom).not.toHaveBeenCalled()
  })
})
