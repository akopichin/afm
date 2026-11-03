import { describe, expect, it } from 'vitest'
import { assistantBlockingExplanation, parseAssistantResult, type AssistantTask } from './assistant-task'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: '', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

// T7.9/§4.3: a required task's mode badge must say WHAT it blocks — the root stage itself, or just its own
// immediate parent helper — never leave "Required" unqualified.
describe('assistantBlockingExplanation', () => {
  it('TestBadge_RequiredExplainsWhoIsBlocked', () => {
    const blocksStage = makeTask({ id: 'qa', mode: 'required', blocksRootCompletion: true })
    expect(assistantBlockingExplanation(blocksStage, [blocksStage])).toBe('Blocks stage completion')

    const parent = makeTask({ id: 'mobile', title: 'Mobile lead', mode: 'required', blocksRootCompletion: true })
    const child = makeTask({
      id: 'android', parentTaskId: 'mobile', mode: 'required', blocksRootCompletion: false,
    })
    expect(assistantBlockingExplanation(child, [parent, child])).toBe('Blocks Mobile lead')

    // Parent has no title — falls back to its profileId (same convention as the rail row's own title display).
    const parentNoTitle = makeTask({ id: 'mobile2', profileId: 'mobile-lead', mode: 'required', blocksRootCompletion: true })
    const child2 = makeTask({
      id: 'android2', parentTaskId: 'mobile2', mode: 'required', blocksRootCompletion: false,
    })
    expect(assistantBlockingExplanation(child2, [parentNoTitle, child2])).toBe('Blocks mobile-lead')
  })

  it('returns null for an advisory task — it never blocks anything', () => {
    const task = makeTask({ mode: 'advisory', blocksRootCompletion: true })
    expect(assistantBlockingExplanation(task, [task])).toBeNull()
  })

  it('returns null for a waived required task — a human already accepted the risk', () => {
    const task = makeTask({ mode: 'required', blocksRootCompletion: true, waived: true, waiverReason: 'flaky profile, accepted' })
    expect(assistantBlockingExplanation(task, [task])).toBeNull()
  })

  it('falls back gracefully when the parent id cannot be found in the given list', () => {
    const child = makeTask({ id: 'android', parentTaskId: 'missing-parent', mode: 'required', blocksRootCompletion: false })
    expect(assistantBlockingExplanation(child, [child])).toBe('Blocks its parent task')
  })
})

describe('parseAssistantResult', () => {
  it('parses repaired as a boolean, defaulting to false', () => {
    const base = { verdict: 'pass', summary: 'ok' }
    expect(parseAssistantResult({ ...base, repaired: true })?.repaired).toBe(true)
    expect(parseAssistantResult(base)?.repaired).toBe(false)
  })

  it('accepts a result without a verdict and leaves it undefined', () => {
    const parsed = parseAssistantResult({ summary: 'Collected the logs.', evidence: ['a.log'], limitations: 'one pod missing' })
    expect(parsed).not.toBeNull()
    expect(parsed?.verdict).toBeUndefined()
    expect(parsed?.summary).toBe('Collected the logs.')
    expect(parsed?.evidence).toEqual(['a.log'])
    expect(parsed?.limitations).toBe('one pod missing')
  })

  it.each([['an unknown string', 'great'], ['an empty string', ''], ['null', null], ['a number', 1]])('rejects %s as a verdict', (_, verdict) => {
    expect(parseAssistantResult({ verdict, summary: 'ok' })).toBeNull()
  })
})
