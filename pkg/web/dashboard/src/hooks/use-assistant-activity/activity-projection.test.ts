import { describe, expect, test } from 'vitest'
import type { AssistantActivityEntry } from '../../types/assistant-activity'
import { latestAction, projectActivity } from './activity-projection'

let seq = 0
function entry(over: Partial<AssistantActivityEntry>): AssistantActivityEntry {
  seq += 1
  return {
    eventId: `e${seq}`, taskId: 't1', participantId: '', turn: 1,
    occurredAt: new Date(Date.UTC(2026, 9, 27, 12, 0, seq)).toISOString(),
    tool: 'Bash', detail: `cmd ${seq}`, truncated: false, cursor: undefined, ...over,
  }
}

const start = (id: string, over: Partial<AssistantActivityEntry> = {}) =>
  entry({ kind: 'tool_started', invocationId: 'inv1', itemId: `tool:${id}`, revision: 1, ...over })
const finish = (id: string, over: Partial<AssistantActivityEntry> = {}) =>
  entry({ kind: 'tool_finished', invocationId: 'inv1', itemId: `tool:${id}`, revision: 2, outcome: 'completed', ...over })
const message = (id: string, revision: number, detail: string, over: Partial<AssistantActivityEntry> = {}) =>
  entry({ kind: 'message', tool: 'text', invocationId: 'inv1', itemId: `msg:${id}`, revision, detail, ...over })
const invocationFinished = (inv: string, outcome: AssistantActivityEntry['outcome']) =>
  entry({ kind: 'invocation_finished', invocationId: inv, outcome, tool: 'invocation', detail: '' })

describe('projectActivity', () => {
  test('legacy entries stay one static row each', () => {
    const rows = projectActivity([entry({}), entry({ tool: 'text' })], true)
    expect(rows.map((r) => r.state)).toEqual(['none', 'none'])
    expect(rows).toHaveLength(2)
  })

  test('start and finish of one tool are one row with the outcome', () => {
    const rows = projectActivity([start('a'), finish('a', { exitCode: 0 })], true)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ state: 'finished', key: '4:inv1:tool:a' })
  })

  test('finish before start, then a late start page, is not running', () => {
    const fin = finish('a')
    for (const entries of [[fin], [fin, start('a')], [start('a'), fin]]) {
      const rows = projectActivity(entries, true)
      expect(rows.map((r) => r.state)).toEqual(['finished'])
    }
  })

  test('the finish outcome picks the state', () => {
    const state = (outcome: AssistantActivityEntry['outcome']) => projectActivity([start('a'), finish('a', { outcome })], true)[0]?.state
    expect(state('failed')).toBe('failed')
    expect(state('timed_out')).toBe('timed_out')
    expect(state('usage_limit')).toBe('usage_limit')
    expect(state('interrupted')).toBe('interrupted')
    expect(state('unknown')).toBe('unknown_outcome')
    expect(state(undefined)).toBe('unknown_outcome')
  })

  test('duration only comes from a known start and finish time', () => {
    const timed = finish('a', { startedAt: '2026-10-27T12:00:00Z', finishedAt: '2026-10-27T12:00:12Z' })
    expect(projectActivity([start('a'), timed], true)[0]?.durationMs).toBe(12000)
    expect(projectActivity([start('a'), finish('a')], true)[0]?.durationMs).toBeNull()
    const startTime = start('b', { startedAt: '2026-10-27T12:00:00Z' })
    const finishTime = finish('b', { finishedAt: '2026-10-27T12:00:05Z' })
    expect(projectActivity([startTime, finishTime], true)[0]?.durationMs).toBe(5000)
  })

  test('an open tool runs only while the task runs and its invocation is the last one', () => {
    expect(projectActivity([start('a')], true)[0]?.state).toBe('running')
    expect(projectActivity([start('a')], false)[0]?.state).toBe('not_received')
  })

  test('an open tool of an earlier invocation is not running', () => {
    const rows = projectActivity([start('a'), start('b', { invocationId: 'inv2' })], true)
    expect(rows.map((r) => r.state)).toEqual(['not_received', 'running'])
  })

  test('invocation_finished closes open tools: interrupted only when confirmed', () => {
    const open = [start('a'), start('b', { itemId: 'tool:b' })]
    expect(projectActivity([...open, invocationFinished('inv1', 'interrupted')], true).map((r) => r.state)).toEqual(['interrupted', 'interrupted'])
    expect(projectActivity([...open, invocationFinished('inv1', 'completed')], true).map((r) => r.state)).toEqual(['not_received', 'not_received'])
    expect(projectActivity([...open, invocationFinished('inv1', 'unknown')], true).map((r) => r.state)).toEqual(['not_received', 'not_received'])
  })

  test('invocation_finished is not a row', () => {
    expect(projectActivity([invocationFinished('inv1', 'completed')], true)).toEqual([])
  })

  test('the same provider id in two invocations stays two rows', () => {
    const rows = projectActivity([start('a'), finish('a'), start('a', { invocationId: 'inv2' })], true)
    expect(rows.map((r) => r.state)).toEqual(['finished', 'running'])
  })

  test('message revisions update one row in place, at the first position, in any arrival order', () => {
    const first = message('m1', 1, 'he')
    const tool = start('a')
    const second = message('m1', 2, 'hello')
    const rows = projectActivity([first, tool, second], true)
    expect(rows).toHaveLength(2)
    expect(rows[0]?.entry.detail).toBe('hello')
    expect(rows[1]?.key).toBe('4:inv1:tool:a')
    expect(projectActivity([second, first], true)[0]?.entry.detail).toBe('hello')
  })

  test('two text messages with different ids are two rows', () => {
    expect(projectActivity([message('m1', 1, 'a'), message('m2', 1, 'b')], true)).toHaveLength(2)
  })

  test('a final message becomes the neutral submitted-answer row', () => {
    expect(projectActivity([message('m1', 2, 'done', { final: true })], false)[0]?.state).toBe('answer_submitted')
  })

  test('a tool left open by an earlier turn is not running once a newer turn is current', () => {
    const open = start('a', { turn: 1 })
    expect(projectActivity([open], true, 1)[0]?.state).toBe('running')
    expect(projectActivity([open], true, 2)[0]?.state).toBe('not_received')
    expect(projectActivity([open, start('b', { turn: 2 })], true, 2).map((r) => r.state)).toEqual(['not_received', 'running'])
  })

  test('the latest action follows arrival order, not the first position of a merged row', () => {
    const msg1 = message('m', 1, 'draft')
    const tool1 = start('a')
    const tool2 = finish('a')
    const msg2 = message('m', 2, 'final text')
    const closed = invocationFinished('inv1', 'completed')
    expect(latestAction([msg1, tool1, tool2, msg2, closed])).toBe(msg2)
    expect(latestAction([closed])).toBeNull()
  })
})
