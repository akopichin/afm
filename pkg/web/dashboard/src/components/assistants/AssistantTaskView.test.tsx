import { act, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView, useTaskResult } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'
import { getAssistantTaskEvents } from '../../api/run-client'

vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return { ...actual, getAssistantTaskEvents: vi.fn(actual.getAssistantTaskEvents) }
})
const mockGetEvents = getAssistantTaskEvents as unknown as ReturnType<typeof vi.fn>

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: 'Reviewer', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

function eventsResponse(events: Array<{ seq: number; type: string; data?: unknown }>): Response {
  return { ok: true, json: async () => ({ events, next_cursor: events.length, has_more: false }) } as Response
}

describe('AssistantTaskView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('empty state: no task selected', () => {
    render(<AssistantTaskView task={null} />)
    expect(screen.getByText(/select a helper task/i)).toBeInTheDocument()
  })

  test('empty state: task still running, no result reported yet', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'running' })} />)

    expect(screen.getByText('Reviewer')).toBeInTheDocument()
    expect(screen.getByText('Running')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/waiting for the first result/i)).toBeInTheDocument())
  })

  test('error state: a failed task shows its status reason', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'failed', statusReason: 'protocol_error: invalid JSON' })} />)

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('protocol_error: invalid JSON'))
  })

  test('error state: a cancelled task with no reason falls back to a generic message', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'cancelled', statusReason: '' })} />)

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/was cancelled/i))
  })

  test('completed, no structured result available: falls back to a calm message instead of an empty screen', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([{ seq: 1, type: 'task_status_changed', data: { status: 'completed', reason: '' } }]))
    render(<AssistantTaskView task={makeTask({ status: 'completed', statusReason: '' })} />)

    await waitFor(() => expect(screen.getByText(/no detailed result/i)).toBeInTheDocument())
  })

  test('completed result renders through the shared safe markdown renderer, not a second dangerouslySetInnerHTML', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([
        {
          seq: 1,
          // The real wire shape (pkg/server/assistants.go's AssistantEventView.Type) is literally the
          // string "task_result" — a different literal here would silently defeat this test's purpose: it
          // would still pass even if the Go side's event Kind were renamed out from under it, since
          // parseAssistantResult does not look at `type` at all (see latestResult's doc comment).
          type: 'task_result',
          data: {
            verdict: 'needs_changes',
            summary: '# Found an issue\n\nThe **retry loop** never backs off.',
            findings: [{ id: 'f1', severity: 'major', path: 'pkg/foo.go', line_start: 42, line_end: null, detail: 'missing backoff' }],
          },
        },
      ]),
    )
    const { container } = render(<AssistantTaskView task={makeTask({ status: 'completed' })} />)

    await waitFor(() => expect(container.querySelector('.md')).not.toBeNull())
    // Rendered via markdown-it (real heading/bold tags), not raw-escaped text.
    expect(container.querySelector('.md h1')).toHaveTextContent('Found an issue')
    expect(container.querySelector('.md strong')).toHaveTextContent('retry loop')
    // Exactly one dangerouslySetInnerHTML surface in the whole view — the shared MarkdownRenderer's.
    expect(container.querySelectorAll('.md')).toHaveLength(1)

    // Verdict is distinguishable by text/icon, not color alone.
    expect(screen.getByText('Needs changes')).toBeInTheDocument()
    expect(container.querySelector('[data-verdict="needs_changes"]')).not.toBeNull()

    // Finding renders too.
    expect(screen.getByText('missing backoff')).toBeInTheDocument()
    expect(screen.getByText('pkg/foo.go:42')).toBeInTheDocument()
  })

  test('required vs advisory mode is shown as text, not only as a data attribute', () => {
    render(<AssistantTaskView task={makeTask({ mode: 'required' })} />)
    expect(screen.getByText('Required')).toBeInTheDocument()
  })

  // T5.6: a checkpoints/on_completion watcher task surfaces trigger kind + pass count + last processed
  // checkpoint — a plain one_shot task (the default makeTask()) shows none of this.
  test('watcher task shows trigger kind, pass count, and last processed checkpoint', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(
      <AssistantTaskView
        task={makeTask({ status: 'watching', trigger: 'checkpoints', watchPasses: 3, watchLastCheckpointSeq: 42 })}
      />,
    )
    expect(screen.getByText('Watches: checkpoints')).toBeInTheDocument()
    expect(screen.getByText('Passes: 3')).toBeInTheDocument()
    expect(screen.getByText('Last checkpoint: #42')).toBeInTheDocument()
  })

  test('a plain one_shot task shows no watcher status line', () => {
    render(<AssistantTaskView task={makeTask({ trigger: 'one_shot' })} />)
    expect(screen.queryByText(/^Watches:/)).not.toBeInTheDocument()
  })

  // TestHistory_LargeHistoryNotFullyLoadedOnEveryPoll (T11.2, §19.4): a long-lived watcher task accumulates a
  // large event history over a run. Before this fix, useTaskResult always re-read from cursor=0 on every 3s
  // poll tick ("Этап 3 scope" comment, now removed) — cost grew with the task's full history size on EVERY
  // tick, not just once. This pins that a later poll continues from the cursor it already consumed, never
  // re-requesting the already-seen page.
  test('TestHistory_LargeHistoryNotFullyLoadedOnEveryPoll', async () => {
    vi.useFakeTimers()
    mockGetEvents.mockClear()
    const largePage = Array.from({ length: 150 }, (_, i) => ({ seq: i + 1, type: 'agent_action', data: null }))
    mockGetEvents.mockImplementation((_taskId: string, cursor: number) => {
      if (cursor === 0) {
        return Promise.resolve({ events: largePage, nextCursor: largePage.length, hasMore: false })
      }
      // Any later poll must be asking for what's NEW since its own last cursor, never cursor 0 again.
      return Promise.resolve({ events: [], nextCursor: cursor, hasMore: false })
    })

    renderHook(() => useTaskResult('t-watcher'))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) }) // first load()

    expect(mockGetEvents).toHaveBeenCalledTimes(1)
    expect(mockGetEvents).toHaveBeenNthCalledWith(1, 't-watcher', 0)

    await act(async () => { await vi.advanceTimersByTimeAsync(3000) }) // next poll tick

    expect(mockGetEvents).toHaveBeenCalledTimes(2)
    // The large 150-event page must never be re-requested from scratch — the second call continues from the
    // cursor the first call already advanced past.
    expect(mockGetEvents).toHaveBeenNthCalledWith(2, 't-watcher', largePage.length)
  })
})
