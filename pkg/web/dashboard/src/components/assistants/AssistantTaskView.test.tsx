import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: 'Reviewer', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', ...overrides,
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
          type: 'task_result_recorded',
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
})
