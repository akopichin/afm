import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { AfmEvent, StageStatus } from '../../types'
import { useStatusDuration } from './use-status-duration'

function statusEvent(stageId: string, status: StageStatus, timestamp: string): AfmEvent {
  return { type: 'stage_status_changed', payload: { status }, stageId, timestamp }
}

const TRACKED: ReadonlySet<StageStatus> = new Set(['awaiting_user_input', 'awaiting_approval', 'failed'])

describe('useStatusDuration', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns 0 when no stage ever entered a tracked status', () => {
    const events: AfmEvent[] = [statusEvent('s1', 'running', '2026-07-29T10:00:00.000Z')]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    expect(result.current).toBe(0)
  })

  test('accumulates a closed episode (entered tracked status, then left it)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:20.000Z') })
    const events: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s1', 'running', '2026-07-29T10:00:05.000Z'),
    ]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    // Эпизод закрыт (running не в TRACKED) — 5000мс, без живой добавки.
    expect(result.current).toBe(5000)
  })

  test('adds a live-ticking delta while an episode is still open', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const events: AfmEvent[] = [statusEvent('s1', 'failed', '2026-07-29T10:00:00.000Z')]
    const { result, rerender } = renderHook(() => useStatusDuration(events, TRACKED))

    expect(result.current).toBe(0)

    act(() => {
      vi.advanceTimersByTime(3000)
    })
    rerender()
    expect(result.current).toBe(3000)

    act(() => {
      vi.advanceTimersByTime(2000)
    })
    rerender()
    expect(result.current).toBe(5000)
  })

  test('sums concurrently-open episodes across different stages (no merge/dedup)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:10.000Z') })
    const events: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s2', 'awaiting_approval', '2026-07-29T10:00:00.000Z'),
    ]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    // Оба открыты 10с → сумма 20000, не merge до 10000.
    expect(result.current).toBe(20000)
  })

  test('ignores non-stage_status_changed events and unparseable statuses without throwing', () => {
    const events: AfmEvent[] = [
      { type: 'agent_action', payload: { tool: 'Bash' }, stageId: 's1', timestamp: '2026-07-29T10:00:00.000Z' },
      { type: 'stage_status_changed', payload: {}, stageId: 's1', timestamp: '2026-07-29T10:00:01.000Z' },
    ]
    expect(() => renderHook(() => useStatusDuration(events, TRACKED))).not.toThrow()
  })

  test('keeps a completed episode counted even after it is trimmed out of the events array', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const closedEpisode: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s1', 'running', '2026-07-29T10:00:07.000Z'),
    ]
    const { result, rerender } = renderHook(({ events }) => useStatusDuration(events, TRACKED), {
      initialProps: { events: closedEpisode },
    })
    expect(result.current).toBe(7000)

    // Симулируем вытеснение старых событий из капа MAX_EVENTS=200: массив
    // больше не содержит closedEpisode вовсе, только новые события.
    const flushedAway: AfmEvent[] = [statusEvent('s2', 'running', '2026-07-29T10:05:00.000Z')]
    rerender({ events: flushedAway })

    // Уже учтённые 7000мс не теряются, хотя события о них исчезли из массива.
    expect(result.current).toBe(7000)
  })
})
