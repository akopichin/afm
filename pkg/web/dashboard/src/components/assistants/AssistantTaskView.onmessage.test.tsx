import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 'h1', parentTaskId: null, profileId: 'reviewer', title: 'Reviewer', mode: 'advisory',
    trigger: 'on_message', status: 'watching', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

function eventsResponse(events: Array<{ seq: number; type: string; data?: unknown }>): Response {
  return { ok: true, json: async () => ({ events, next_cursor: events.length, has_more: false }) } as Response
}

const resultEvent = (data: Record<string, unknown>) => ({ seq: 1, type: 'task_result', data })

describe('AssistantTaskView on_message helper', () => {
  beforeEach(() => {
    vi.useRealTimers()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('idle: shows Waiting for message, an empty queue and no checkpoint/passes fields', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask()} />)

    expect(screen.getByText('Waiting for message', { selector: '.assistant-task-view-status' })).toBeInTheDocument()
    expect(screen.getByText('On message')).toBeInTheDocument()
    expect(screen.getByText('Queued requests: 0')).toBeInTheDocument()
    expect(screen.queryByText(/Passes:/)).toBeNull()
    expect(screen.queryByText(/Last checkpoint/)).toBeNull()
    expect(screen.queryByText(/Active request/)).toBeNull()
    await act(async () => { await Promise.resolve() })
  })

  test('working: shows the active request, its turn and the queue length', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'running', activeRequestId: 'mm-2', turn: 2, queuedRequests: 3 })} />)

    expect(screen.getByText('Active request: mm-2 · turn 2')).toBeInTheDocument()
    expect(screen.getByText('Queued requests: 3')).toBeInTheDocument()
    await act(async () => { await Promise.resolve() })
  })

  test('shows the last request and a failed outcome in text', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ lastRequestId: 'mm-1', lastOutcome: 'failed' })} />)

    expect(screen.getByText('Last request: mm-1 · failed')).toBeInTheDocument()
    await act(async () => { await Promise.resolve() })
  })

  test('a result without a verdict is labelled with its request and turn', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([resultEvent({ summary: 'The retry path is safe.', request_id: 'mm-1', turn: 1 })]),
    )
    render(<AssistantTaskView task={makeTask({ lastRequestId: 'mm-1', lastOutcome: 'result', turn: 1 })} />)

    await waitFor(() => expect(screen.getByText('Request mm-1 · turn 1')).toBeInTheDocument())
    expect(screen.getByText('The retry path is safe.')).toBeInTheDocument()
    expect(screen.queryByText('Pass')).toBeNull()
    expect(screen.queryByText(/previous request/i)).toBeNull()
  })

  test('an older result stays marked with its own request while a new request is active', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([resultEvent({ summary: 'First answer.', request_id: 'mm-1', turn: 1 })]),
    )
    render(<AssistantTaskView task={makeTask({ status: 'running', activeRequestId: 'mm-2', turn: 2, lastRequestId: 'mm-1', lastOutcome: 'result' })} />)

    await waitFor(() => expect(screen.getByText('Request mm-1 · turn 1')).toBeInTheDocument())
    expect(screen.getByText('Result of a previous request')).toBeInTheDocument()
  })

  test('a required on_message helper keeps its Required badge', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ mode: 'required', lastRequestId: 'mm-1', lastOutcome: 'failed' })} />)

    expect(screen.getByText('Required')).toBeInTheDocument()
    expect(screen.getByText('Last request: mm-1 · failed')).toBeInTheDocument()
    await act(async () => { await Promise.resolve() })
  })

  test('required: an old pass followed by a failed request still offers Waive', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([resultEvent({ summary: 'ok', verdict: 'pass', request_id: 'mm-1', turn: 1 })]),
    )
    render(<AssistantTaskView task={makeTask({ mode: 'required', lastRequestId: 'mm-2', lastOutcome: 'failed', turn: 2 })} />)

    await waitFor(() => expect(screen.getByText('Request mm-1 · turn 1')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /waive/i })).toBeInTheDocument()
  })

  test('required: an old pass hides nothing while a newer request is active', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([resultEvent({ summary: 'ok', verdict: 'pass', request_id: 'mm-1', turn: 1 })]),
    )
    render(<AssistantTaskView task={makeTask({ mode: 'required', status: 'running', activeRequestId: 'mm-2', turn: 2, lastRequestId: 'mm-1', lastOutcome: 'result' })} />)

    await waitFor(() => expect(screen.getByText('Request mm-1 · turn 1')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /waive/i })).toBeInTheDocument()
  })

  test('required: a pass of the current request hides Waive', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([resultEvent({ summary: 'ok', verdict: 'pass', request_id: 'mm-1', turn: 1 })]),
    )
    render(<AssistantTaskView task={makeTask({ mode: 'required', lastRequestId: 'mm-1', lastOutcome: 'result', turn: 1 })} />)

    await waitFor(() => expect(screen.getByText('Request mm-1 · turn 1')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /waive/i })).toBeNull()
  })

  test('paused: shows Paused and no failure alert', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'paused', statusReason: 'paused by user' })} />)

    expect(screen.getByText('Paused', { selector: '.assistant-task-view-status' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    await act(async () => { await Promise.resolve() })
  })

  test('a turn finishing does not move focus out of the view', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    const view = render(
      <div>
        <input aria-label="Draft" />
        <AssistantTaskView task={makeTask({ status: 'running', activeRequestId: 'mm-1', turn: 1 })} />
      </div>,
    )
    const draft = screen.getByLabelText('Draft')
    draft.focus()

    view.rerender(
      <div>
        <input aria-label="Draft" />
        <AssistantTaskView task={makeTask({ lastRequestId: 'mm-1', lastOutcome: 'result', turn: 1 })} />
      </div>,
    )
    expect(document.activeElement).toBe(draft)
    await act(async () => { await Promise.resolve() })
  })

  test('other triggers keep their own block', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ trigger: 'checkpoints', watchPasses: 2, watchLastCheckpointSeq: 9 })} />)

    expect(screen.getByText('Passes: 2')).toBeInTheDocument()
    expect(screen.getByText('Watching', { selector: '.assistant-task-view-status' })).toBeInTheDocument()
    expect(screen.queryByText('On message')).toBeNull()
    await act(async () => { await Promise.resolve() })
  })
})
