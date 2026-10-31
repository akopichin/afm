import { describe, expect, test } from 'vitest'
import type { Stage } from '../types'
import type { StageTiming } from '../types/stage'
import { formatStageDuration, hasOpenTiming, stageTimes } from './stage-timing'

const SINCE = Date.parse('2026-10-30T20:00:00Z')

function stage(id: string, timing?: StageTiming): Stage {
  return {
    id, name: id, status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false,
    hasDialog: false, showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [],
    timing,
  }
}

function timing(ms: Partial<Record<string, number>>, open?: { kind: string; sinceMs: number }): StageTiming {
  return {
    started: true,
    complete: true,
    ms: { work: 0, approval: 0, helpers: 0, backoff: 0, scheduling: 0, paused: 0, ...ms },
    open: open as StageTiming['open'],
  }
}

describe('formatStageDuration', () => {
  test.each([
    [0, '0s'],
    [45, '0m 45s'],
    [60, '1m 00s'],
    [185, '3m 05s'],
    [684, '11m 24s'],
    [3723, '1h 02m 03s'],
    [123 * 3600 + 4 * 60 + 5, '123h 04m 05s'],
  ])('%i s → %s', (seconds, text) => {
    expect(formatStageDuration(seconds)).toBe(text)
  })

  test('null is a dash and differs from 0s', () => {
    expect(formatStageDuration(null)).toBe('—')
    expect(formatStageDuration(0)).not.toBe('—')
  })

  test('negative and fractional values are floored into range', () => {
    expect(formatStageDuration(-5)).toBe('0s')
    expect(formatStageDuration(45.9)).toBe('0m 45s')
  })
})

describe('stageTimes', () => {
  test('adds the live delta only to the open leaf and floors each leaf', () => {
    const stages = [stage('a', timing({ work: 1500, approval: 2900, paused: 999 }, { kind: 'approval', sinceMs: SINCE }))]
    const { byStage } = stageTimes(stages, SINCE + 1200, true)
    expect(byStage.get('a')).toEqual({
      workSec: 1, approvalSec: 4, helpersSec: 0, backoffSec: 0, schedulingSec: 0, pausedSec: 0, waitingSec: 4, timeSec: 5,
    })
  })

  test('without live the open interval adds nothing', () => {
    const stages = [stage('a', timing({ work: 5000 }, { kind: 'work', sinceMs: SINCE }))]
    expect(stageTimes(stages, SINCE + 60_000, false).byStage.get('a')?.workSec).toBe(5)
  })

  test('a future since does not subtract time', () => {
    const stages = [stage('a', timing({ work: 5000 }, { kind: 'work', sinceMs: SINCE }))]
    expect(stageTimes(stages, SINCE - 10_000, true).byStage.get('a')?.workSec).toBe(5)
  })

  test('waiting is the sum of the waiting reasons and time adds work and paused', () => {
    const stages = [stage('a', timing({ work: 10_000, approval: 1000, helpers: 2000, backoff: 3000, scheduling: 4000, paused: 5000 }))]
    const view = stageTimes(stages, SINCE, true).byStage.get('a')
    expect(view?.waitingSec).toBe(10)
    expect(view?.timeSec).toBe(25)
  })

  test('totals sum the rounded integers, not the raw milliseconds', () => {
    const stages = [
      stage('a', timing({ work: 900, approval: 900 })),
      stage('b', timing({ work: 900, approval: 900 })),
    ]
    const { byStage, totals } = stageTimes(stages, SINCE, true)
    expect(byStage.get('a')?.timeSec).toBe(0)
    expect(totals).toEqual({
      workSec: 0, approvalSec: 0, helpersSec: 0, backoffSec: 0, schedulingSec: 0, pausedSec: 0, waitingSec: 0, timeSec: 0,
    })

    const second = [stage('a', timing({ work: 1900 })), stage('b', timing({ work: 1900 }))]
    expect(stageTimes(second, SINCE, true).totals.workSec).toBe(2)
  })

  test('parallel stages are summed independently', () => {
    const stages = [
      stage('a', timing({ work: 3000 }, { kind: 'work', sinceMs: SINCE })),
      stage('b', timing({ approval: 2000 }, { kind: 'approval', sinceMs: SINCE })),
    ]
    const { totals } = stageTimes(stages, SINCE + 4000, true)
    expect(totals.workSec).toBe(7)
    expect(totals.approvalSec).toBe(6)
    expect(totals.timeSec).toBe(13)
  })

  test('a not started stage is null and not incomplete', () => {
    const notStarted = { started: false, complete: true }
    const stages = [
      { ...stage('pending', notStarted), status: 'pending' as const },
      { ...stage('queued', notStarted), status: 'ready' as const },
      { ...stage('blocked', notStarted), status: 'failed' as const },
      { ...stage('held', notStarted), status: 'paused' as const },
    ]
    const result = stageTimes(stages, SINCE, true)
    for (const id of ['pending', 'queued', 'blocked', 'held']) expect(result.byStage.get(id)).toBeNull()
    expect(result.incompleteIds.size).toBe(0)
    expect(result.partial).toBe(false)
    expect(result.totalsKnown).toBe(true)
    expect(result.totals.timeSec).toBe(0)
  })

  test('a retried stage keeps its known time while it is ready again', () => {
    const result = stageTimes([{ ...stage('retried', timing({ work: 7000 })), status: 'ready' as const }], SINCE, true)
    expect(result.byStage.get('retried')?.workSec).toBe(7)
    expect(result.incompleteIds.size).toBe(0)
    expect(result.totals.workSec).toBe(7)
  })

  test.each(['done', 'failed', 'paused', 'pending', 'ready'] as const)(
    'without timing (old server) time is unknown whatever its status: %s',
    (status) => {
      const stages = [stage('known', timing({ work: 4000 })), { ...stage('old'), status }]
      const result = stageTimes(stages, SINCE, true)
      expect(result.byStage.get('old')).toBeNull()
      expect([...result.incompleteIds]).toEqual(['old'])
      expect(result.partial).toBe(true)
      expect(result.totalsKnown).toBe(true)
      expect(result.totals.workSec).toBe(4)

      expect(stageTimes([{ ...stage('old'), status }], SINCE, true).totalsKnown).toBe(false)
    },
  )

  test('totals are unknown when no stage has known time but some is unknown', () => {
    const result = stageTimes([stage('old'), { ...stage('next'), status: 'pending' as const }], SINCE, true)
    expect([...result.incompleteIds]).toEqual(['old', 'next'])
    expect(result.partial).toBe(true)
    expect(result.totalsKnown).toBe(false)
  })

  test('incomplete data is null, excluded from the totals and flags partial', () => {
    const stages = [
      stage('done', timing({ work: 4000 })),
      stage('legacy', { started: true, complete: false }),
    ]
    const result = stageTimes(stages, SINCE, true)
    expect(result.byStage.get('legacy')).toBeNull()
    expect([...result.incompleteIds]).toEqual(['legacy'])
    expect(result.partial).toBe(true)
    expect(result.totals.workSec).toBe(4)
  })

  test('has an entry for every stage in the input order', () => {
    const result = stageTimes([stage('b'), stage('a')], SINCE, true)
    expect([...result.byStage.keys()]).toEqual(['b', 'a'])
  })
})

describe('hasOpenTiming', () => {
  test('is true only when some stage has an open interval', () => {
    expect(hasOpenTiming([stage('a'), stage('b', timing({ work: 1 }))])).toBe(false)
    expect(hasOpenTiming([stage('a', timing({}, { kind: 'work', sinceMs: SINCE }))])).toBe(true)
  })
})
