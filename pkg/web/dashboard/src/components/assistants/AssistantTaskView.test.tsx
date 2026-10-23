import { act, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView, useTaskResult } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'
import { parseStageQuota } from '../../types/stage'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import {
  cancelAssistantTask,
  continueAssistantTask,
  getAssistantTaskEvents,
  getAssistantTaskRaw,
  pauseAssistantTask,
  waiveAssistantTask,
} from '../../api/run-client'

vi.mock('../../api/run-client', async () => {
  const actual = await vi.importActual<typeof import('../../api/run-client')>('../../api/run-client')
  return {
    ...actual,
    getAssistantTaskEvents: vi.fn(actual.getAssistantTaskEvents),
    getAssistantTaskRaw: vi.fn(() => Promise.resolve('')),
    pauseAssistantTask: vi.fn(() => Promise.resolve()),
    continueAssistantTask: vi.fn(() => Promise.resolve()),
    cancelAssistantTask: vi.fn(() => Promise.resolve()),
    waiveAssistantTask: vi.fn(() => Promise.resolve()),
  }
})
const mockGetEvents = getAssistantTaskEvents as unknown as ReturnType<typeof vi.fn>
const mockGetRaw = getAssistantTaskRaw as unknown as ReturnType<typeof vi.fn>
const mockPause = pauseAssistantTask as unknown as ReturnType<typeof vi.fn>
const mockContinue = continueAssistantTask as unknown as ReturnType<typeof vi.fn>
const mockCancel = cancelAssistantTask as unknown as ReturnType<typeof vi.fn>
const mockWaive = waiveAssistantTask as unknown as ReturnType<typeof vi.fn>

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
  // afterEach(restoreAllMocks) resets the factory vi.fns' implementations; re-establish them each test so the
  // mutation mocks return a resolved promise (not undefined) regardless of test order.
  beforeEach(() => {
    vi.useRealTimers() // some sibling tests opt into fake timers; make sure waitFor here uses real ones.
    mockPause.mockImplementation(() => Promise.resolve())
    mockCancel.mockImplementation(() => Promise.resolve())
    mockContinue.mockImplementation(() => Promise.resolve())
    mockWaive.mockImplementation(() => Promise.resolve())
    mockGetRaw.mockClear()
    mockGetRaw.mockImplementation(() => Promise.resolve(''))
  })
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

  test('offers the raw answer on a protocol_error failure and fetches it lazily on expand', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    mockGetRaw.mockImplementation(() => Promise.resolve('Проверил. Всё плохо, вот пять замечаний...'))
    render(<AssistantTaskView task={makeTask({ status: 'failed', statusReason: 'protocol_error: invalid JSON' })} />)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(mockGetRaw).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText(/show the helper's raw answer/i))
    await waitFor(() => expect(mockGetRaw).toHaveBeenCalledTimes(1))
    expect(mockGetRaw).toHaveBeenCalledWith('t1')
    await waitFor(() => expect(screen.getByText(/Проверил\. Всё плохо/)).toBeInTheDocument())
  })

  test('switching to a different failed task does not keep showing the previous task\'s raw answer', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    mockGetRaw.mockImplementation((taskId: string) => Promise.resolve(`RAW-FOR-${taskId}`))
    const { rerender } = render(
      <AssistantTaskView task={makeTask({ id: 't1', status: 'failed', statusReason: 'protocol_error: invalid JSON' })} />,
    )

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    fireEvent.click(screen.getByText(/show the helper's raw answer/i))
    await waitFor(() => expect(screen.getByText('RAW-FOR-t1')).toBeInTheDocument())

    rerender(
      <AssistantTaskView task={makeTask({ id: 't2', status: 'failed', statusReason: 'protocol_error: invalid JSON' })} />,
    )

    await waitFor(() => expect(screen.queryByText('RAW-FOR-t1')).not.toBeInTheDocument())
    expect(screen.queryByText('RAW-FOR-t2')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/show the helper's raw answer/i))
    await waitFor(() => expect(mockGetRaw).toHaveBeenLastCalledWith('t2'))
    await waitFor(() => expect(screen.getByText('RAW-FOR-t2')).toBeInTheDocument())
  })

  test('keeps showing the plain error when the raw answer is unavailable', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    mockGetRaw.mockImplementation(() => Promise.reject(new Error('404')))
    render(<AssistantTaskView task={makeTask({ status: 'failed', statusReason: 'protocol_error: invalid JSON' })} />)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    fireEvent.click(screen.getByText(/show the helper's raw answer/i))
    await waitFor(() => expect(mockGetRaw).toHaveBeenCalledTimes(1))
    // No crash, and the plain error text is still there.
    expect(screen.getByRole('alert')).toHaveTextContent('protocol_error: invalid JSON')
  })

  test('completed, no structured result available: falls back to a calm message instead of an empty screen', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([{ seq: 1, type: 'task_status_changed', data: { status: 'completed', reason: '' } }]))
    render(<AssistantTaskView task={makeTask({ status: 'completed', statusReason: '' })} />)

    await waitFor(() => expect(screen.getByText(/no detailed result/i)).toBeInTheDocument())
  })

  test('shows a repaired badge when the result was mechanically repaired', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([{ seq: 1, type: 'task_result', data: { verdict: 'pass', summary: 'ok', repaired: true } }]),
    )
    render(<AssistantTaskView task={makeTask({ status: 'completed' })} />)

    await waitFor(() => expect(screen.getByText('repaired')).toBeInTheDocument())
  })

  test('does not show the badge for an unrepaired result', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      eventsResponse([{ seq: 1, type: 'task_result', data: { verdict: 'pass', summary: 'ok' } }]),
    )
    render(<AssistantTaskView task={makeTask({ status: 'completed' })} />)

    await waitFor(() => expect(screen.getByText('ok')).toBeInTheDocument())
    expect(screen.queryByText('repaired')).toBeNull()
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

  test('actions: a running task offers Pause and Cancel and fires them', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ status: 'running' })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(mockPause).toHaveBeenCalledWith('t1'))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Cancel' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(mockCancel).toHaveBeenCalledWith('t1'))
  })

  test('actions: a required needs_changes task can be waived with a reason', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    // Required + completed (needs_changes is a terminal 'completed' status with an open gate) + not passed.
    render(<AssistantTaskView task={makeTask({ mode: 'required', status: 'completed', blocksRootCompletion: true })} />)

    fireEvent.click(screen.getByRole('button', { name: 'Waive…' }))
    const confirm = screen.getByRole('button', { name: 'Confirm waive' })
    // Reason is required: the confirm button is disabled until one is typed.
    expect((confirm as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByPlaceholderText(/why are you accepting/i), { target: { value: 'known risk, shipping anyway' } })
    expect((confirm as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(confirm)
    await waitFor(() => expect(mockWaive).toHaveBeenCalledWith('t1', 'known risk, shipping anyway'))
  })

  test('actions: a waived required task offers no actions', () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    render(<AssistantTaskView task={makeTask({ mode: 'required', status: 'completed', waived: true })} />)
    expect(screen.queryByRole('button', { name: 'Waive…' })).toBeNull()
    // Terminal + waived + advisory-less → the action bar renders nothing at all.
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  // Helper, припаркованный на неожидаемом лимите: статус queued + ручной hold на сервере, until пуст. Сам он не
  // проснётся — счётчика нет, а человеку нужен Continue (Pause бессмысленен).
  describe('helper parked by an unwaitable usage limit', () => {
    const heldQuota = parseStageQuota({ provider: 'claude', window: 'five_hour', source: 'structured', reset_at: '2026-10-24T22:00:00Z', pause_reason: 'exceeds_max_wait' })
    const heldTask = () => makeTask({ status: 'queued', quota: heldQuota })

    test('offers Continue (wired to continueAssistantTask), no Pause', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
      render(<AssistantTaskView task={heldTask()} />)
      expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
      await waitFor(() => expect(mockContinue).toHaveBeenCalledWith('t1'))
      expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    })

    test('shows the reason instead of a countdown or "Resumes at"', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
      const { container } = render(
        <MessageTimeZoneProvider timeZone="UTC" utcOffsetMinutes={0}><AssistantTaskView task={heldTask()} /></MessageTimeZoneProvider>,
      )
      expect(container.querySelector('.quota-countdown')).toBeNull()
      expect(container.textContent).not.toContain('Resumes')
      expect(container.querySelector('.quota-wait')).toHaveTextContent(/later than the maximum wait, so the task was paused/)
    })

    test('no_reset and max_waits also offer Continue', () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
      for (const pauseReason of ['no_reset', 'max_waits']) {
        const { unmount } = render(<AssistantTaskView task={makeTask({ status: 'queued', quota: parseStageQuota({ provider: 'codex', source: 'text', pause_reason: pauseReason }) })} />)
        expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
        expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
        unmount()
      }
    })
  })

  test('a helper that simply waits for a limit (until set, no pause_reason) keeps countdown and Pause, no Continue', () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(eventsResponse([]))
    const quota = parseStageQuota({ provider: 'claude', source: 'structured', until: '2099-01-01T00:00:00Z', reset_at: '2099-01-01T00:00:00Z' })
    const { container } = render(<AssistantTaskView task={makeTask({ status: 'queued', quota })} />)
    expect(container.querySelector('.quota-countdown')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
  })
})
