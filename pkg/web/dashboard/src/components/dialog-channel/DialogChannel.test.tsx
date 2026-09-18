import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { MaximizeProvider } from '../layout/Maximizable'
import { DialogChannel } from './DialogChannel'
import { FileBrowserProvider } from '../file-browser'
import { answerDialog, FlowApiError } from '../../api/run-client'

// answerDialog заменяется на vi.fn(), обёрнутый вокруг реальной реализации
// (пропускает вызовы дальше, к fetch) — так существующие тесты, проверяющие
// POST-тело через spy на globalThis.fetch, продолжают работать без изменений,
// а тесты answer_out_of_order/generic-error переопределяют его по одному
// разу через mockRejectedValueOnce.
vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return {
    ...actual,
    answerDialog: vi.fn(actual.answerDialog),
  }
})

// jumpToBottom заспайен на уровне модуля (не через spyOn существующего хука, у которого
// в jsdom scrollHeight всегда 0 и реальный скролл незаметен) — так тест #7 может
// проверить сам факт вызова при переходе панели в maximized, не завися от layout jsdom.
const mockJumpToBottom = vi.fn()
const mockRelease = vi.fn()

vi.mock('../../hooks/use-stick-to-bottom', () => ({
  useStickToBottom: () => ({ ref: { current: null }, stick: true, jumpToBottom: mockJumpToBottom, release: mockRelease }),
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

// use-auto-grow-textarea also calls the SAME HTMLElement.prototype.scrollIntoView
// spied on below (with block:'nearest', for a growing pending-question textarea) —
// noise unrelated to the scrollTarget effect under test, which always calls it
// with block:'start' (align the target's TOP to the container top). Filters that
// noise out and returns the target elements.
function centeredScrollCalls(spy: ReturnType<typeof vi.spyOn>): HTMLElement[] {
  return spy.mock.calls
    .map((call, index) => ({ arg: call[0] as ScrollIntoViewOptions | undefined, context: spy.mock.contexts[index] }))
    .filter(({ arg }) => arg?.block === 'start')
    .map(({ context }) => context as HTMLElement)
}

function makeStage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 's1',
    name: 'Stage',
    status: 'awaiting_user_input',
    updatedAt: '',
    interactive: true,
    autonomous: false,
    autoApprove: false,
    hasDialog: false,
    showPlan: true,
    showDialog: true,
    isScript: false,
    pausedFrom: '',
    preNote: '',
    buttons: [],
    ...overrides,
  }
}

// DialogChannel's per-line question-comment textarea now renders with
// allowFileReferences (Task 14) — its "Attach project file" button calls
// useFileBrowser(), which throws outside a FileBrowserProvider. Every render
// in this file goes through this wrapper, same as App.tsx wraps the whole
// dashboard in prod.
function renderDialogChannel(ui: ReactElement, enabled = true) {
  return render(
    <FileBrowserProvider flowName="flow1" startedAt="t1" enabled={enabled}>
      {ui}
    </FileBrowserProvider>,
  )
}

describe('DialogChannel', () => {
  beforeEach(() => {
    mockJumpToBottom.mockClear()
    mockRelease.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('hasContent=false: no entries and status not awaiting_user_input renders nothing', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([]))

    const stage = makeStage({ status: 'running' })
    const { container } = renderDialogChannel(<DialogChannel stage={stage} />)

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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    await screen.findByRole('button', { name: 'Alpha' })
    expect(screen.getByRole('button', { name: 'Beta' })).toBeInTheDocument()

    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    expect(textarea).not.toBeDisabled()
  })

  test('новый pending-вопрос даёт one-shot класс dialog-flash', async () => {
    const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    renderDialogChannel(<DialogChannel stage={makeStage()} />)

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
    renderDialogChannel(<DialogChannel stage={makeStage()} />)
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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))
    expect(container.querySelector('.line-comment-form')).toBeNull()

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    fireEvent.click(line1)

    expect(container.querySelector('.line-comment-form')).not.toBeNull()
  })

  test('the question-line comment textarea offers the file-browser picker (Attach project file)', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    // M3: поле кастомного ответа тоже имеет скрепку, поэтому до открытия
    // line-комментария она уже одна.
    expect(screen.getAllByRole('button', { name: 'Attach' })).toHaveLength(1)
    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    // Форма line-комментария добавляет свою скрепку → теперь две.
    expect(screen.getAllByRole('button', { name: 'Attach' })).toHaveLength(2)
  })

  // Finding 5 + R2 #6a: capabilities.file_browser=false must hide the PROJECT
  // picker (no /api/files call), но НЕ саму скрепку — «Upload image…» работает и
  // в host mode. Раньше отсутствие capability прятало весь Attach.
  test('capabilities.file_browser=false: the comment textarea offers Upload image but no project picker and never calls the files API', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />, false)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)

    // Скрепка есть (upload-only), но проект-пикера нет.
    const attachButtons = screen.getAllByRole('button', { name: 'Attach' })
    expect(attachButtons.length).toBeGreaterThan(0)
    fireEvent.click(attachButtons[0]!)
    expect(screen.getByRole('menuitem', { name: /upload image/i })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /choose project file/i })).toBeNull()
    const filesCalls = fetchSpy.mock.calls.filter(([input]) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      return url.includes('/api/files/')
    })
    expect(filesCalls).toHaveLength(0)
  })

  test('M3: the custom-answer textarea now offers the file-browser picker', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelector('textarea.dialog-custom')).not.toBeNull())

    // Поле кастомного ответа теперь имеет свою скрепку (M3) — до открытия
    // line-комментария она одна и принадлежит именно этому полю.
    expect(screen.getAllByRole('button', { name: 'Attach' })).toHaveLength(1)
    const customAnswerWrap = (container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement).closest(
      '.pasteable-textarea-wrap',
    ) as HTMLElement
    expect(customAnswerWrap.querySelector('.pasteable-attach-btn')).not.toBeNull()
  })

  test('the question-line comment textarea grows to fit its content', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const originalScrollHeight = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      'scrollHeight',
    )
    Object.defineProperty(window.HTMLTextAreaElement.prototype, 'scrollHeight', {
      get: () => 150,
      configurable: true,
    })

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a longer comment' } })

    expect(textarea.style.height).toBe('150px')

    if (originalScrollHeight) {
      Object.defineProperty(window.HTMLTextAreaElement.prototype, 'scrollHeight', originalScrollHeight)
    }
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

    const originalScrollHeight = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      'scrollHeight',
    )
    Object.defineProperty(window.HTMLTextAreaElement.prototype, 'scrollHeight', {
      get: () => 200,
      configurable: true,
    })

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'a much longer free-text answer' } })

    expect(textarea.style.height).toBe('200px')

    if (originalScrollHeight) {
      Object.defineProperty(window.HTMLTextAreaElement.prototype, 'scrollHeight', originalScrollHeight)
    }
  })

  test('a non-empty draft ignores clicks on other question lines and on itself; only × discards it', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const line1 = container.querySelector('[data-line="1"]') as HTMLElement
    const line2 = container.querySelector('[data-line="3"]') as HTMLElement

    fireEvent.click(line1)
    const textarea = container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'in progress' } })

    fireEvent.click(line1)
    expect(container.querySelector('.line-comment-form')).not.toBeNull()
    expect((container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement).value).toBe('in progress')

    fireEvent.click(line2)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()
    expect(container.querySelector('[data-line="3"] .line-comment-form')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))
    expect(container.querySelector('.line-comment-form')).toBeNull()
  })

  test('▸ SEND is disabled while a draft comment is open but unsaved, and re-enables once the form is closed', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    const sendBtn = screen.getByRole('button', { name: '▸ SEND' })
    expect(sendBtn).not.toBeDisabled()

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    fireEvent.change(container.querySelector('.line-comment-form textarea') as HTMLTextAreaElement, {
      target: { value: 'not saved yet' },
    })

    expect(sendBtn).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 1' }))

    expect(sendBtn).not.toBeDisabled()
  })

  test('an empty draft still lets a row click switch to a different question line', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="1"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).not.toBeNull()

    fireEvent.click(container.querySelector('[data-line="3"]') as HTMLElement)
    expect(container.querySelector('[data-line="1"] .line-comment-form')).toBeNull()
    expect(container.querySelector('[data-line="3"] .line-comment-form')).not.toBeNull()
  })

  test('adding a comment hides options+textarea and shows Send feedback; deleting the only comment restores them', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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
      question: 'First line\n\nSecond line',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
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
      question: 'First line\n\nSecond line',
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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    await waitFor(() => expect(container.querySelectorAll('.plan-line').length).toBe(2))

    fireEvent.click(container.querySelector('[data-line="3"]') as HTMLElement)
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
    expect(body.answer as string).toContain('Line 3:')
    expect(body.answer as string).toContain('Second line')
  })

  test('Ctrl+Enter in a comment textarea saves the comment instead of sending feedback', async () => {
    const calls: FetchCall[] = []
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'First line\n\nSecond line',
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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    renderDialogChannel(<DialogChannel stage={makeStage()} />)

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

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('loadDialog: response.ok=false leaves entries empty and does not crash', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false } as Response)

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('loadDialog: a non-array JSON payload leaves entries empty and does not crash', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ not: 'an array' }))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  test('maximizing the panel scrolls the feed to the bottom via jumpToBottom', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([]))

    renderDialogChannel(
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

    renderDialogChannel(
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

  test('answered entry with auto_answered:true is marked distinctly from a real user answer', async () => {
    const history: RawDialogEntry & { auto_answered?: boolean } = {
      id: 'q1',
      phase: 'implementation',
      question: 'which option?',
      answer: 'Вариант B',
      auto_answered: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([history]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(container.querySelector('.qa')).not.toBeNull())
    expect(container.querySelector('.qa')).toHaveClass('qa-auto')
    expect(container.querySelector('.auto-answered-badge')).not.toBeNull()
    expect(container.querySelector('.auto-answered-badge')?.textContent).toBe('⚙')
    expect(container.querySelector('.qa')?.textContent).toContain('Вариант B')
  })

  test('answered entry without auto_answered (user answer) has no badge and no qa-auto class', async () => {
    const history: RawDialogEntry = {
      id: 'q1',
      phase: 'implementation',
      question: 'which option?',
      answer: 'User chose Alpha',
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([history]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage({ status: 'running' })} />)

    await waitFor(() => expect(container.querySelector('.qa')).not.toBeNull())
    expect(container.querySelector('.qa')).not.toHaveClass('qa-auto')
    expect(container.querySelector('.auto-answered-badge')).toBeNull()
    expect(container.querySelector('.qa')?.textContent).toContain('User chose Alpha')
  })

  test('stage=null: renders nothing, fetches no dialog, and does not crash', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { container } = renderDialogChannel(<DialogChannel stage={null} />)

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(container).toBeEmptyDOMElement()
  })

  // Finding #7: overlapping /dialog polls can resolve out of order. An older
  // (pre-answer) response resolving AFTER a newer (answered) one must not revert
  // the answered question back to pending.
  test('stale dialog response does not revert an answered question to pending', async () => {
    vi.useFakeTimers()

    let resolveFirst!: (r: Response) => void
    const firstPromise = new Promise<Response>((r) => {
      resolveFirst = r
    })
    let resolveSecond!: (r: Response) => void
    const secondPromise = new Promise<Response>((r) => {
      resolveSecond = r
    })

    const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
    const answered = { id: 'q1', phase: 'p1', question: 'Pick', answer: 'A', options: ['A'], allow_custom: true }

    vi.spyOn(globalThis, 'fetch')
      .mockReturnValueOnce(firstPromise as unknown as Promise<Response>)
      .mockReturnValueOnce(secondPromise as unknown as Promise<Response>)
      .mockResolvedValue(jsonResponse([answered]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    // Mount issued request #1 (firstPromise, still pending). Advance to the next
    // poll → request #2 (secondPromise).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })

    // Newer request resolves FIRST with the answered state.
    await act(async () => {
      resolveSecond(jsonResponse([answered]))
      await Promise.resolve()
    })
    expect(container.querySelector('#dialog-pending')).toBeNull()

    // Older (stale) request resolves LATER with the pre-answer pending state —
    // the generation guard must drop it, keeping the answered view.
    await act(async () => {
      resolveFirst(jsonResponse([pending]))
      await Promise.resolve()
    })
    expect(container.querySelector('#dialog-pending')).toBeNull()
  })

  // Finding #7: a transient fetch failure must not wipe the last successful
  // dialog to an empty panel (loadDialog returns null, not []).
  test('transient fetch error keeps the last successful dialog', async () => {
    vi.useFakeTimers()

    const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(jsonResponse([pending]))
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValue(jsonResponse([pending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    // First poll succeeds → pending question shown.
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(container.querySelector('#dialog-pending')).not.toBeNull()

    // Second poll rejects → must NOT wipe the visible dialog.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(container.querySelector('#dialog-pending')).not.toBeNull()
  })

  // codex MAJOR#4: reload() (called after a successful answer) used to run its
  // own setEntries/reset WITHOUT bumping the shared generation counter — an
  // in-flight poll issued before the answer, still pending, could resolve
  // AFTER reload() applied the new question and stomp it back to the old one.
  test('a stale in-flight poll resolving after reload() does not revert to the previous question', async () => {
    vi.useFakeTimers()

    const q1 = { id: 'q1', phase: 'p1', question: 'Pick 1', answer: null, options: ['Alpha'], allow_custom: true }
    const q2 = { id: 'q2', phase: 'p1', question: 'Pick 2', answer: null, options: ['Beta'], allow_custom: true }

    let resolveStalePoll!: (r: Response) => void
    const stalePollPromise = new Promise<Response>((r) => {
      resolveStalePoll = r
    })

    let getCallCount = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) {
        getCallCount++
        if (getCallCount === 1) return jsonResponse([q1]) // mount poll
        if (getCallCount === 2) return stalePollPromise as unknown as Promise<Response> // in-flight poll, stays pending
        return jsonResponse([q2]) // reload() after the answer is sent
      }
      return jsonResponse([])
    })

    renderDialogChannel(<DialogChannel stage={makeStage()} />)

    // Fake timers are active — findByRole/waitFor's internal polling would
    // never fire; flush the mount fetch's microtasks manually instead.
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(screen.getByRole('button', { name: 'Alpha' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))

    // Next 2s poll fires WHILE the answer hasn't been sent yet — it becomes the
    // stale in-flight request (call #2), left unresolved.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })

    // Send the answer: answerDialog POST resolves, then reload() issues call #3
    // and applies q2. Flush the microtask chain (fetch → .json() → reload()).
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))
      for (let i = 0; i < 10; i++) {
        await Promise.resolve()
      }
    })
    expect(screen.getByRole('button', { name: 'Beta' })).toBeInTheDocument()

    // The stale poll (call #2, requestId assigned before reload's own bump)
    // resolves LATE with the OLD q1 data — the shared generation guard must
    // drop it instead of reverting the just-applied q2.
    await act(async () => {
      resolveStalePoll(jsonResponse([q1]))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(screen.queryByRole('button', { name: 'Alpha' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Beta' })).toBeInTheDocument()
  })

  // codex MAJOR#4: reload() used to reset the draft/selection WITHOUT updating
  // the poll effect's own "last pending key" — the very next 2s poll then saw
  // the (already-applied) new question as "changed" and wiped the draft a
  // second time, discarding anything the user had just started typing on it.
  test('a poll right after reload() does not re-clear a draft started on the new pending question', async () => {
    vi.useFakeTimers()

    const q1 = { id: 'q1', phase: 'p1', question: 'Pick 1', answer: null, options: ['Alpha'], allow_custom: true }
    const q2 = { id: 'q2', phase: 'p1', question: 'Pick 2', answer: null, options: ['Beta'], allow_custom: true }

    let getCallCount = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.endsWith('/dialog/answer')) return { ok: true } as Response
      if (url.endsWith('/dialog')) {
        getCallCount++
        return getCallCount === 1 ? jsonResponse([q1]) : jsonResponse([q2]) // mount sees q1, reload() and every poll after sees q2
      }
      return jsonResponse([])
    })

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    // Fake timers are active — findByRole/waitFor's internal polling would
    // never fire; flush the mount fetch's microtasks manually instead.
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(screen.getByRole('button', { name: 'Alpha' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))
      for (let i = 0; i < 10; i++) {
        await Promise.resolve()
      }
    })
    expect(screen.getByRole('button', { name: 'Beta' })).toBeInTheDocument()

    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'draft for q2' } })
    expect(textarea.value).toBe('draft for q2')

    // Next poll (2s) re-fetches q2 — unchanged since reload() already applied
    // it — and must NOT see it as a fresh pending question.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect((container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement).value).toBe('draft for q2')
  })

  // codex MAJOR: reload() (called from sendAnswer/sendFeedback after a
  // successful POST) used to be bound only to the `stage` closed over at the
  // moment it was invoked, not to whichever stage is CURRENTLY mounted in the
  // panel. If the answer POST for stage A is still in flight when the user
  // switches the panel to stage B, A's reload() ran anyway once the POST
  // resolved — bumping the SHARED requestGenRef (invalidating B's own
  // legitimate poll) and applying A's dialog data on top of B's panel. The
  // currentStageIdRef guard must make A's stale reload() a no-op once the
  // panel has moved on to B.
  test("an answer POST for stage A resolving after switching to stage B does not overwrite B's panel", async () => {
    const stageA = makeStage({ id: 'A' })
    const stageB = makeStage({ id: 'B' })
    const qA = { id: 'qa1', phase: 'planning', question: 'Pick A', answer: null, options: ['AlphaA'], allow_custom: true }
    const qB = { id: 'qb1', phase: 'planning', question: 'Pick B', answer: null, options: ['BetaB'], allow_custom: true }

    let resolveAnswerA!: (r: Response) => void
    const answerAPromise = new Promise<Response>((r) => {
      resolveAnswerA = r
    })

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.endsWith('/api/stages/A/dialog/answer')) return answerAPromise as unknown as Promise<Response>
      if (url.endsWith('/api/stages/A/dialog')) return jsonResponse([qA])
      if (url.endsWith('/api/stages/B/dialog')) return jsonResponse([qB])
      return jsonResponse([])
    })

    const { rerender } = renderDialogChannel(<DialogChannel stage={stageA} />)

    await screen.findByRole('button', { name: 'AlphaA' })
    fireEvent.click(screen.getByRole('button', { name: 'AlphaA' }))
    // Fires answerDialog('A', ...) → POST /api/stages/A/dialog/answer, awaiting
    // answerAPromise (not yet resolved) inside sendAnswer's still-pending await.
    fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))

    // The user switches the panel to stage B BEFORE A's POST resolves.
    rerender(
      <FileBrowserProvider flowName="flow1" startedAt="t1" enabled>
        <DialogChannel stage={stageB} />
      </FileBrowserProvider>,
    )
    await screen.findByRole('button', { name: 'BetaB' })

    // Now A's stale answer POST resolves — its reload() (closed over stage A)
    // must see that the panel has moved on to stage B and no-op instead of
    // applying A's data (or A's absence of data) onto B's panel.
    await act(async () => {
      resolveAnswerA({ ok: true } as Response)
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(screen.getByRole('button', { name: 'BetaB' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'AlphaA' })).toBeNull()
  })

  // Finding #8: the SEND button must disable while a submit is in flight so a
  // second click can't fire a duplicate POST (which the server 409s).
  test('SEND disables while a submit is in flight — no double-submit', async () => {
    let resolvePost!: (r: Response) => void
    const postPromise = new Promise<Response>((r) => {
      resolvePost = r
    })
    const pending = { id: 'q1', phase: 'planning', question: 'Pick', answer: null, options: ['Alpha'], allow_custom: true }
    const spy = vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      if (String(input).includes('/dialog/answer')) return postPromise as unknown as Promise<Response>
      return Promise.resolve(jsonResponse([pending]))
    })

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))
    })

    const send = () => container.querySelector('.btn-send') as HTMLButtonElement
    await act(async () => {
      fireEvent.click(send())
    })

    expect(send().disabled).toBe(true) // in-flight → disabled
    await act(async () => {
      fireEvent.click(send()) // second click on the disabled button is a no-op
    })
    const postCalls = spy.mock.calls.filter((c) => String(c[0]).includes('/dialog/answer'))
    expect(postCalls).toHaveLength(1)

    resolvePost(jsonResponse([{ ...pending, answer: 'Alpha' }]))
    await act(async () => {
      await Promise.resolve()
    })
  })

  // Finding #8: a failed mutating action must surface an error (not vanish as an
  // unhandled promise rejection) and re-enable the button for a retry.
  test('a failed answer submit shows an error and re-enables SEND', async () => {
    const pending = { id: 'q1', phase: 'planning', question: 'Pick', answer: null, options: ['Alpha'], allow_custom: true }
    vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      if (String(input).includes('/dialog/answer')) return Promise.resolve({ ok: false, status: 409 } as Response)
      return Promise.resolve(jsonResponse([pending]))
    })

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))
    })
    await act(async () => {
      fireEvent.click(container.querySelector('.btn-send') as HTMLButtonElement)
    })

    await waitFor(() => expect(container.querySelector('.dialog-error')).not.toBeNull())
    expect((container.querySelector('.btn-send') as HTMLButtonElement).disabled).toBe(false)
  })

  // Task 7: answerDialog теперь бросает типизированный FlowApiError — панель
  // резинхронизируется (discard draft) ТОЛЬКО для code==='answer_out_of_order',
  // любая другая ошибка сохраняет черновик пользователя.
  test('answer_out_of_order rejection triggers reload() and discards the stale selection', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    // reload()'s /dialog re-fetch (after the 409) reflects the real cause of an
    // answer_out_of_order rejection: the server-side question actually moved on
    // (here: a phase change) while the client still held a stale view — so the
    // (phase,id) key genuinely changes and applyDialog's reset fires. Same id
    // and options as before, so the "Alpha" button still renders — only its
    // "selected" state must be gone.
    const pendingAfterReload: RawDialogEntry = { ...pending, phase: 'p2' }
    let getCallCount = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      getCallCount++
      return jsonResponse([getCallCount === 1 ? pending : pendingAfterReload])
    })
    vi.mocked(answerDialog).mockRejectedValueOnce(
      new FlowApiError('/api/stages/s1/dialog/answer', 409, 'answer_out_of_order'),
    )

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    fireEvent.click(screen.getByRole('button', { name: 'Alpha' }))
    fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))

    await waitFor(() => expect(container.querySelector('.dialog-error')).not.toBeNull())
    // reload() re-fetched /dialog and reset selectedOption — the stale "Alpha"
    // selection from before the rejection is gone.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Alpha' })).not.toHaveClass('selected'))
  })

  test('a generic (non-FlowApiError) rejection shows the error but preserves the draft — no reload', async () => {
    const pending: RawDialogEntry = {
      id: 'q1',
      phase: 'p1',
      question: 'Pick one',
      answer: null,
      options: ['Alpha'],
      allow_custom: true,
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
    vi.mocked(answerDialog).mockRejectedValueOnce(new Error('network blip'))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)
    await screen.findByRole('button', { name: 'Alpha' })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'my draft answer' } })
    fireEvent.click(screen.getByRole('button', { name: '▸ SEND' }))

    await waitFor(() => expect(container.querySelector('.dialog-error')).not.toBeNull())
    // reload() was NOT called — it would have cleared customText to ''.
    expect(textarea.value).toBe('my draft answer')
  })

  // Composite (phase,id) key: the SAME id in a DIFFERENT phase is a different
  // question — the draft must reset, unlike the old id-only comparison.
  test('pending switching planning/q1 → implementation/q1 (same id, different phase) clears the draft', async () => {
    vi.useFakeTimers()
    const planningPending = { id: 'q1', phase: 'planning', question: 'Pick', answer: null, options: ['Alpha'], allow_custom: true }
    const implementationPending = {
      id: 'q1',
      phase: 'implementation',
      question: 'Pick again',
      answer: null,
      options: ['Beta'],
      allow_custom: true,
    }

    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(jsonResponse([planningPending]))
      .mockResolvedValue(jsonResponse([implementationPending]))

    const { container } = renderDialogChannel(<DialogChannel stage={makeStage()} />)

    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    const textarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'still about planning q1' } })
    expect(textarea.value).toBe('still about planning q1')

    // Next poll (2s) picks up implementation/q1 — same raw id, different phase.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })

    const newTextarea = container.querySelector('textarea.dialog-custom') as HTMLTextAreaElement
    expect(newTextarea.value).toBe('')
  })

  // Task 6: переход из ленты (FeedWorkspace.onOpenDialog) передаёт scrollTarget —
  // канал должен доскроллить до конкретного Q&A. /dialog грузится асинхронно,
  // поэтому якорь (#qa-<phase>-<id>) на маунте ещё не существует — цель нужно
  // удержать и повторить попытку после того, как история дорисуется.
  describe('scrollTarget', () => {
    test('delayed-fetch: scroll fires only after the matching qa-<phase>-<id> anchor renders', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      let resolveFetch!: (value: Response) => void
      const delayed = new Promise<Response>((resolve) => {
        resolveFetch = resolve
      })
      vi.spyOn(globalThis, 'fetch').mockReturnValue(delayed)

      const { container } = renderDialogChannel(
        <DialogChannel stage={makeStage({ status: 'done' })} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }} />,
      )

      // /dialog ещё не ответил — ни истории, ни якоря в DOM. hasContent=false
      // (status не awaiting_user_input, hasDialog=false, entries=[]) → панель
      // ещё вообще ничего не рендерит, и попытка скролла не может найти анкор.
      expect(container.querySelector(`#qa-planning-q1`)).toBeNull()
      expect(scrollSpy).not.toHaveBeenCalled()

      // /dialog отвечает с задержкой (следующий тик) отвеченным вопросом q1.
      await act(async () => {
        resolveFetch(jsonResponse([{ id: 'q1', phase: 'planning', question: 'Q', answer: 'A' }]))
        await Promise.resolve()
        await Promise.resolve()
      })

      // Якорь появился, и ИМЕННО тогда сработал скролл (не раньше).
      expect(container.querySelector('#qa-planning-q1')).not.toBeNull()
      expect(scrollSpy).toHaveBeenCalledTimes(1)
    })

    test('a scrollTarget whose anchor never appears does not throw and does not spam scrollIntoView', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entry = { id: 'q1', phase: 'planning', question: 'Q', answer: 'A' }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([entry]))
      vi.useFakeTimers()

      expect(() =>
        renderDialogChannel(
          <DialogChannel stage={makeStage({ status: 'done' })} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'missing' }} />,
        ),
      ).not.toThrow()

      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
      })
      expect(scrollSpy).not.toHaveBeenCalled()

      // Следующий опрос (2с) снова не находит анкор missing — не должно ни
      // упасть, ни начать спамить scrollIntoView.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2000)
      })
      expect(scrollSpy).not.toHaveBeenCalled()
    })

    test('pending live-вопрос: scrollTarget с id ожидающего вопроса скроллит к #dialog-pending', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const pending = { id: 'q1', phase: 'planning', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))

      const { container } = renderDialogChannel(
        <DialogChannel stage={makeStage()} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }} />,
      )

      await waitFor(() => expect(container.querySelector('#dialog-pending')).not.toBeNull())
      await waitFor(() => expect(scrollSpy).toHaveBeenCalled())
    })

    // F4: id уникален только В ПРЕДЕЛАХ фазы — совпадение по одному id не
    // должно уводить к pending-вопросу ДРУГОЙ фазы.
    test('F4: a pending question in a different phase does not steal a same-id targeted scroll', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entries = [
        { id: 'q1', phase: 'planning', question: 'Old Q', answer: 'Old A' },
        { id: 'q1', phase: 'implementation', question: 'Live Q', answer: null, options: ['A'], allow_custom: true },
      ]
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(entries))

      const { container } = renderDialogChannel(
        <DialogChannel stage={makeStage()} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }} />,
      )

      await waitFor(() => expect(container.querySelector('#qa-planning-q1')).not.toBeNull())
      // Отфильтровываем шум от use-auto-grow-textarea (тот же
      // HTMLElement.prototype.scrollIntoView, но вызывается с block:'nearest'
      // для растущего textarea pending-вопроса) — нас интересует только вызов
      // ИЗ прицельного эффекта (block:'center').
      await waitFor(() => expect(centeredScrollCalls(scrollSpy)).toHaveLength(1))

      // Скроллили именно к отвеченному planning/q1, а не к #dialog-pending
      // (implementation/q1, тот же id — другая фаза).
      expect(centeredScrollCalls(scrollSpy)[0]?.id).toBe('qa-planning-q1')
    })

    // F5: клик по старому отвеченному Q&A, пока ждёт ответа НОВЫЙ вопрос —
    // автопрыжок в конец (эффект на pending?.id) не должен перебивать
    // прицельный скролл к старому ответу.
    test('F5: targeting an older answered Q&A while a newer question is pending does not get overridden by the pending auto-jump', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entries = [
        { id: 'q1', phase: 'planning', question: 'Old Q', answer: 'Old A' },
        { id: 'q2', phase: 'planning', question: 'New Q', answer: null, options: ['A'], allow_custom: true },
      ]
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(entries))

      const { container } = renderDialogChannel(
        <DialogChannel stage={makeStage()} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }} />,
      )

      await waitFor(() => expect(container.querySelector('#qa-planning-q1')).not.toBeNull())
      await waitFor(() => expect(container.querySelector('#dialog-pending')).not.toBeNull())

      // Targeted scroll wins: exactly one targeted (block:'start') scroll, on
      // the OLD qa anchor — the pending auto-jump (mockJumpToBottom) never fired.
      await waitFor(() => expect(centeredScrollCalls(scrollSpy)).toHaveLength(1))
      expect(centeredScrollCalls(scrollSpy)[0]?.id).toBe('qa-planning-q1')
      expect(mockJumpToBottom).not.toHaveBeenCalled()
      // stick-to-bottom released for a HISTORY target, so the MutationObserver
      // doesn't re-pin to the bottom and undo the targeted scroll.
      expect(mockRelease).toHaveBeenCalled()
    })

    // F6a: цель, предназначенная ДРУГОЙ стадии, не должна применяться к этой —
    // без stageId-гейта случайное совпадение phase/id увело бы скролл не туда.
    test('F6: a scrollTarget for a different stageId is ignored (no scroll)', async () => {
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entries = [{ id: 'q1', phase: 'planning', question: 'Q', answer: 'A' }]
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(entries))

      const { container } = renderDialogChannel(
        <DialogChannel stage={makeStage({ status: 'done' })} scrollTarget={{ stageId: 'OTHER-STAGE', phase: 'planning', id: 'q1' }} />,
      )

      await waitFor(() => expect(container.querySelector('#qa-planning-q1')).not.toBeNull())
      // Даём эффектам ещё один цикл на случай ошибочного срабатывания.
      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
      })
      expect(scrollSpy).not.toHaveBeenCalled()
    })

    // F6b: после успешного применения цели родитель должен получить сигнал —
    // без него он никогда не сбросит scrollToDialogTarget, и она переживёт своё
    // назначение (реплей скролла при повторном открытии того же диалога).
    test('F6: onTargetConsumed fires exactly once after a successful scroll', async () => {
      vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entries = [{ id: 'q1', phase: 'planning', question: 'Q', answer: 'A' }]
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(entries))
      const onTargetConsumed = vi.fn()

      const { container } = renderDialogChannel(
        <DialogChannel
          stage={makeStage({ status: 'done' })}
          scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }}
          onTargetConsumed={onTargetConsumed}
        />,
      )

      await waitFor(() => expect(container.querySelector('#qa-planning-q1')).not.toBeNull())
      await waitFor(() => expect(onTargetConsumed).toHaveBeenCalledTimes(1))
    })

    // R1: clearing retainedTarget after a successful targeted scroll re-runs the
    // pending auto-jump effect (it lists retainedTarget in its deps), which now
    // sees retainedTarget===null and schedules requestAnimationFrame(jumpToBottom)
    // — undoing the targeted scroll one frame later. The old F5 test only checked
    // the immediate frame and missed this; this test advances a FAKED rAF past
    // that frame and asserts the undo never happens.
    test('R1: clearing the target after a successful scroll does not schedule a delayed jump back to the pending question', async () => {
      vi.useFakeTimers({
        toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'requestAnimationFrame', 'cancelAnimationFrame'],
      })
      const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const entries = [
        { id: 'q1', phase: 'planning', question: 'Old Q', answer: 'Old A' },
        { id: 'q2', phase: 'planning', question: 'New Q', answer: null, options: ['A'], allow_custom: true },
      ]
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(entries))

      renderDialogChannel(<DialogChannel stage={makeStage()} scrollTarget={{ stageId: 's1', phase: 'planning', id: 'q1' }} />)

      // Let the initial /dialog fetch resolve and the targeted-scroll effect fire.
      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
      })
      expect(centeredScrollCalls(scrollSpy)).toHaveLength(1)
      expect(centeredScrollCalls(scrollSpy)[0]?.id).toBe('qa-planning-q1')
      expect(mockJumpToBottom).not.toHaveBeenCalled()

      // Flush the requestAnimationFrame the retainedTarget clear may have
      // scheduled — the undo-jump bug fires one frame later, not immediately.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100)
      })

      expect(mockJumpToBottom).not.toHaveBeenCalled()
      expect(centeredScrollCalls(scrollSpy)).toHaveLength(1)
    })

    // R2: a same-stage target whose anchor never appears (the id doesn't exist
    // in this stage's dialog) used to retain the target FOREVER — with R1 fixed
    // via a ref, the pending auto-jump stays suppressed for every future pending
    // question, and the parent never gets onTargetConsumed to clear its own
    // target. A bounded give-up timer must release it.
    test('R2: a same-stage target whose anchor never appears is released after the give-up window, restoring normal auto-jump', async () => {
      vi.useFakeTimers({
        toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'requestAnimationFrame', 'cancelAnimationFrame'],
      })
      vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      const answered = { id: 'q1', phase: 'planning', question: 'Q', answer: 'A' }
      const pending = { id: 'q2', phase: 'planning', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
      let withPending = false
      vi.spyOn(globalThis, 'fetch').mockImplementation(async () => jsonResponse(withPending ? [answered, pending] : [answered]))
      const onTargetConsumed = vi.fn()

      renderDialogChannel(
        <DialogChannel
          stage={makeStage({ status: 'done' })}
          scrollTarget={{ stageId: 's1', phase: 'planning', id: 'missing' }}
          onTargetConsumed={onTargetConsumed}
        />,
      )

      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
      })
      expect(onTargetConsumed).not.toHaveBeenCalled()

      // Past the give-up window (~4s): the target is released even though its
      // anchor never rendered.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(4001)
      })
      expect(onTargetConsumed).toHaveBeenCalledTimes(1)

      // A subsequent pending question now auto-jumps again — normal behaviour
      // is restored, not permanently suppressed.
      withPending = true
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2000) // next /dialog poll picks up the pending question
      })
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100) // flush the scheduled requestAnimationFrame
      })
      expect(mockJumpToBottom).toHaveBeenCalled()
    })
  })
})
