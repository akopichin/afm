import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { AfmEvent, StageStatus } from '../../types'
import { useIdleTime } from './use-idle-time'

function statusEvent(stageId: string, status: StageStatus, timestamp: string): AfmEvent {
  return { type: 'stage_status_changed', payload: { status }, stageId, timestamp }
}

describe('useIdleTime', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns 0 when no stage is ever failed or asking a question', () => {
    const events: AfmEvent[] = [statusEvent('s1', 'running', '2026-07-29T10:00:00.000Z')]
    const { result } = renderHook(() => useIdleTime(events))
    expect(result.current).toBe(0)
  })

  test('bug fix: a cascaded-failed stage does NOT count as idle while another stage is actively running', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:10.000Z') })
    const events: AfmEvent[] = [
      statusEvent('b', 'running', '2026-07-29T10:00:00.000Z'),
      statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z'),
    ]
    // b работает все 10с, a упала одновременно — idle не должен копиться вовсе.
    const { result } = renderHook(() => useIdleTime(events))
    expect(result.current).toBe(0)
  })

  test('idle resumes accruing once the active stage finishes, with the failed stage still present', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:15.000Z') })
    const events: AfmEvent[] = [
      statusEvent('b', 'running', '2026-07-29T10:00:00.000Z'),
      statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z'),
      statusEvent('b', 'done', '2026-07-29T10:00:05.000Z'),
    ]
    const { result } = renderHook(() => useIdleTime(events))
    // Первые 5с (b работает) — не idle. Следующие 10с (b done, a всё ещё
    // failed, никто не активен) — idle. Итог: 10000, не 15000.
    expect(result.current).toBe(10000)
  })

  test('a pending question always counts as idle, even while another stage is actively running', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:08.000Z') })
    const events: AfmEvent[] = [
      statusEvent('b', 'running', '2026-07-29T10:00:00.000Z'),
      statusEvent('a', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
    ]
    const { result } = renderHook(() => useIdleTime(events))
    expect(result.current).toBe(8000)
  })

  test('a failed stage counts as idle when nothing else is active', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:06.000Z') })
    const events: AfmEvent[] = [statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z')]
    const { result } = renderHook(() => useIdleTime(events))
    expect(result.current).toBe(6000)
  })

  test('retrying (backoff) alone does not count as idle and does not count as active', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:05.000Z') })
    const events: AfmEvent[] = [
      statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z'),
      statusEvent('b', 'retrying', '2026-07-29T10:00:00.000Z'),
    ]
    // retrying — не "активен", так что failed всё ещё копит idle все 5с.
    const { result } = renderHook(() => useIdleTime(events))
    expect(result.current).toBe(5000)
  })

  test('live-ticks while currently idle', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const events: AfmEvent[] = [statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z')]
    const { result, rerender } = renderHook(() => useIdleTime(events))

    expect(result.current).toBe(0)

    act(() => {
      vi.advanceTimersByTime(3000)
    })
    rerender()
    expect(result.current).toBe(3000)
  })

  test('ignores non-stage_status_changed events and unparseable statuses without throwing', () => {
    const events: AfmEvent[] = [
      { type: 'agent_action', payload: { tool: 'Bash' }, stageId: 's1', timestamp: '2026-07-29T10:00:00.000Z' },
      { type: 'stage_status_changed', payload: {}, stageId: 's1', timestamp: '2026-07-29T10:00:01.000Z' },
    ]
    expect(() => renderHook(() => useIdleTime(events))).not.toThrow()
  })

  test('keeps a completed idle episode counted even after it is trimmed out of the events array', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const closedEpisode: AfmEvent[] = [
      statusEvent('a', 'failed', '2026-07-29T10:00:00.000Z'),
      statusEvent('a', 'running', '2026-07-29T10:00:07.000Z'),
    ]
    const { result, rerender } = renderHook(({ events }) => useIdleTime(events), {
      initialProps: { events: closedEpisode },
    })
    expect(result.current).toBe(7000)

    const flushedAway: AfmEvent[] = [statusEvent('b', 'running', '2026-07-29T10:05:00.000Z')]
    rerender({ events: flushedAway })

    expect(result.current).toBe(7000)
  })
})
