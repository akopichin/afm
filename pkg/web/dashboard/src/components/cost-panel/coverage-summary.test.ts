import { describe, it, expect } from 'vitest'
import { summarizeCoverageGroups } from './coverage-summary'
import type { CoverageIssue } from '../../types/cost'

// FIX 2 (codex#2/opus#2, cost-ui increment 2 final review): the coverage line
// used to drop issue.attribution and issue.reason entirely — a reader could
// see "unpriced implementation/claude claude-x (×3)" but never learn WHOSE
// invocations these were (a specific stage? run overhead? unresolvable) or WHY
// they weren't priced. These tests pin the new format down.
function makeIssue(overrides: Partial<CoverageIssue> = {}): CoverageIssue {
  return {
    kind: 'unpriced',
    attribution: { kind: 'unknown' },
    phase: 'implementation',
    channel: 'claude',
    model: 'claude-x',
    reason: '',
    count: 1,
    ...overrides,
  }
}

describe('summarizeCoverageGroups — attribution + reason', () => {
  it('names run overhead as the owner', () => {
    const text = summarizeCoverageGroups([makeIssue({ attribution: { kind: 'run_overhead' } })])
    expect(text).toContain('run overhead')
  })

  it('names the stage id as the owner', () => {
    const text = summarizeCoverageGroups([makeIssue({ attribution: { kind: 'stage', stageId: 'implement-api' } })])
    expect(text).toContain('implement-api')
  })

  it('falls back to "unknown" when attribution cannot be resolved', () => {
    const text = summarizeCoverageGroups([makeIssue({ attribution: { kind: 'unknown' } })])
    expect(text).toContain('unknown')
  })

  it('includes a non-empty reason', () => {
    const text = summarizeCoverageGroups([makeIssue({ reason: 'model not in the price table' })])
    expect(text).toContain('model not in the price table')
  })

  it('omits the reason segment entirely when reason is empty', () => {
    const text = summarizeCoverageGroups([makeIssue({ reason: '' })])
    // No dangling separator (e.g. trailing " — (×1)") when there is no reason.
    expect(text).not.toMatch(/—\s*\(×1\)/)
  })

  it('renders a run_overhead issue and a stage issue distinguishably, with reasons', () => {
    const issues = [
      makeIssue({ attribution: { kind: 'run_overhead' }, reason: 'overhead phase is not priced', count: 2 }),
      makeIssue({ attribution: { kind: 'stage', stageId: 'review' }, reason: 'unknown model', count: 5 }),
    ]
    const text = summarizeCoverageGroups(issues)
    const segments = text.split('; ')
    expect(segments).toHaveLength(2)
    expect(segments[0]).toContain('run overhead')
    expect(segments[0]).toContain('overhead phase is not priced')
    expect(segments[0]).toContain('×2')
    expect(segments[1]).toContain('review')
    expect(segments[1]).toContain('unknown model')
    expect(segments[1]).toContain('×5')
    // The two segments must not be identical strings — they describe different owners.
    expect(segments[0]).not.toBe(segments[1])
  })
})
