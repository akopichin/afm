import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { DialogChannel } from './DialogChannel'

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
  return { id: 's1', name: 'Stage', status: 'awaiting_user_input', updatedAt: '', interactive: true, autonomous: false, ...overrides }
}

describe('DialogChannel', () => {
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
})
