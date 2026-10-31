import { describe, expect, test } from 'vitest'
import type { UsageMetrics, UsageTokenCounts } from '../../types/usage-analytics'
import {
  cacheWriteTokens,
  coverageLabel,
  formatCount,
  formatDurationMs,
  formatMoney,
  formatOffset,
  formatShare,
  formatTickMs,
  metricValue,
  reasonText,
  tickedDuration,
} from './usage-format'

const counts: UsageTokenCounts = {
  uncachedInput: 10,
  cacheRead: 20,
  cacheWrite5m: 3,
  cacheWrite1h: 4,
  cacheWriteOther: 5,
  output: 6,
  reasoningOutput: 2,
  total: 48,
}

function metrics(overrides: Partial<UsageMetrics>): UsageMetrics {
  return { jobs: 1, activeJobs: 0, agentTime: { durationMs: 90_000, measuredJobs: 1, unknownJobs: 0 }, ...overrides }
}

describe('formatting', () => {
  test('unknown values are an em dash, not zero', () => {
    expect(formatCount(null)).toBe('—')
    expect(formatDurationMs(null)).toBe('—')
    expect(formatMoney(null)).toBe('—')
    expect(formatOffset(null)).toBe('—')
    expect(formatShare(null, 10)).toBe('')
  })

  test('a measured zero stays zero', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatDurationMs(0)).toBe('0s')
    expect(formatMoney(0)).toBe('$0.00')
  })

  test('durations use the stage duration format', () => {
    expect(formatDurationMs(185_000)).toBe('3m 05s')
  })

  test('money keeps two decimals and marks sub-cent amounts', () => {
    expect(formatMoney(1.923)).toBe('$1.92')
    expect(formatMoney(0.004)).toBe('<$0.01')
  })

  test('the start offset is relative to the run start', () => {
    expect(formatOffset(130_000)).toBe('+2m 10s')
  })

  test('share is a whole percent of the total and empty without a total', () => {
    expect(formatShare(25, 100)).toBe('25%')
    expect(formatShare(1, 0)).toBe('')
    expect(formatShare(1, null)).toBe('')
  })

  test('timeline ticks of a short run are seconds and tell the ticks apart', () => {
    const span = 13_000
    const labels = [0, 0.25, 0.5, 0.75, 1].map((tick) => formatTickMs(tick * span, span))
    expect(labels).toEqual(['0s', '3s', '7s', '10s', '13s'])
  })

  test('timeline ticks of a few minutes are m:ss', () => {
    const span = 300_000
    const labels = [0, 0.25, 0.5, 0.75, 1].map((tick) => formatTickMs(tick * span, span))
    expect(labels).toEqual(['0:00', '1:15', '2:30', '3:45', '5:00'])
  })

  test('timeline ticks of a long run are minutes and the last one is the end of the range', () => {
    const span = 40 * 60_000
    const labels = [0, 0.25, 0.5, 0.75, 1].map((tick) => formatTickMs(tick * span, span))
    expect(labels).toEqual(['0m', '10m', '20m', '30m', '40m'])
    expect(formatTickMs(3_900_000, 3_900_000)).toBe('1h 5m')
  })

  test('cache writes of every lifetime add up', () => {
    expect(cacheWriteTokens(counts)).toBe(12)
  })
})

describe('metricValue', () => {
  test('reads the value of each metric and keeps unknown as null', () => {
    const row = metrics({
      tokens: { counts, meteredJobs: 1, unmeteredJobs: 0, pendingJobs: 0 },
      money: { estimatedCostUsd: 1.5, pricedJobs: 1, unpricedJobs: 0 },
    })
    expect(metricValue(row, 'agent_time')).toBe(90_000)
    expect(metricValue(row, 'tokens')).toBe(48)
    expect(metricValue(row, 'cost')).toBe(1.5)
    expect(metricValue(metrics({}), 'tokens')).toBeNull()
    expect(metricValue(metrics({}), 'cost')).toBeNull()
  })
})

describe('tickedDuration', () => {
  test('a job that is still running grows from its value at as_of', () => {
    expect(tickedDuration(10_000, true, 4_000)).toBe(14_000)
  })

  test('a finished job and an unknown duration never tick', () => {
    expect(tickedDuration(10_000, false, 4_000)).toBe(10_000)
    expect(tickedDuration(null, true, 4_000)).toBeNull()
  })
})

describe('coverage text', () => {
  test('names what is partial or unknown and says nothing for full coverage', () => {
    expect(coverageLabel('time', 'partial')).toBe('Partial time')
    expect(coverageLabel('tokens', 'unknown')).toBe('Tokens unknown')
    expect(coverageLabel('cost', 'partial')).toBe('Partial cost')
    expect(coverageLabel('time', 'full')).toBeNull()
  })

  test('reasons read as sentences and an unknown code is still readable', () => {
    expect(reasonText('interrupted_job', true)).toBe('a job was interrupted, its duration is unknown')
    expect(reasonText('some_new_reason', true)).toBe('some new reason')
  })

  test('price reasons disappear when money is hidden', () => {
    expect(reasonText('unpriced_model', true)).not.toBeNull()
    expect(reasonText('unpriced_model', false)).toBeNull()
  })
})
