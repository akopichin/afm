import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildTree, useStageAssistants } from './use-stage-assistants'
import type { AssistantTaskWire } from '../../api/run-client'
import type { AssistantTask } from '../../types/assistant-task'

function wire(overrides: Partial<AssistantTaskWire> = {}): AssistantTaskWire {
  return { id: 't1', profile_id: 'reviewer', mode: 'advisory', trigger: 'one_shot', status: 'running', ...overrides }
}

function task(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: '', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', ...overrides,
  }
}

function jsonResponse(body: unknown): Response {
  return { ok: true, json: async () => body } as Response
}

describe('buildTree', () => {
  it('links parent_task_id references into a forest of root nodes with children', () => {
    const tasks = [task({ id: 'a' }), task({ id: 'b', parentTaskId: 'a' }), task({ id: 'c' })]
    const roots = buildTree(tasks)
    expect(roots.map((r) => r.id)).toEqual(['a', 'c'])
    expect(roots[0]?.children.map((c) => c.id)).toEqual(['b'])
    expect(roots[1]?.children).toEqual([])
  })

  it('Этап 3 scope: every task is a root (Attach never sets parent_task_id)', () => {
    const tasks = [task({ id: 'a' }), task({ id: 'b' })]
    const roots = buildTree(tasks)
    expect(roots).toHaveLength(2)
    expect(roots.every((r) => r.children.length === 0)).toBe(true)
  })
})

describe('useStageAssistants', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('makes no request and returns an empty map when disabled', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderHook(() => useStageAssistants(['s1'], false))
    await Promise.resolve()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('makes no request when there are no stage ids, even if enabled', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderHook(() => useStageAssistants([], true))
    await Promise.resolve()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('polls each stage id and summarizes count + hasAttention', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      if (url.includes('/stages/s1/assistants')) {
        return jsonResponse({ tasks: [wire({ id: 't1', status: 'running' }), wire({ id: 't2', status: 'paused' })] })
      }
      return jsonResponse({ tasks: [] })
    })

    const { result } = renderHook(() => useStageAssistants(['s1', 's2'], true))

    await waitFor(() => expect(result.current.s1).toBeDefined())
    expect(result.current.s1?.count).toBe(2)
    expect(result.current.s1?.hasAttention).toBe(true)
    expect(result.current.s2?.count).toBe(0)
    expect(result.current.s2?.hasAttention).toBe(false)
  })

  it('keeps the previous summary on a transient fetch error instead of clearing it', async () => {
    let fail = false
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      if (fail) throw new Error('network down')
      return jsonResponse({ tasks: [wire({ id: 't1' })] })
    })

    const { result } = renderHook(() => useStageAssistants(['s1'], true))
    await waitFor(() => expect(result.current.s1?.count).toBe(1))

    fail = true
    vi.useFakeTimers({ shouldAdvanceTime: true })
    await act(async () => {
      vi.advanceTimersByTime(3000)
      await Promise.resolve()
    })
    expect(result.current.s1?.count).toBe(1)
  })
})
