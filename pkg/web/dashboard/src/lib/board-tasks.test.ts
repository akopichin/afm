import { describe, expect, test } from 'vitest'
import type { MeshBoardEntry, MeshParticipant } from '../api/mesh-client'
import { boardReportsOf, boardTaskCount, boardTasks, boardTopLevel } from './board-tasks'

const dev: MeshParticipant = { id: 'run:t-dev', kind: 'run_agent', scope: 'run', alias: 'dev', reachable: true }
const qa: MeshParticipant = { id: 'stage:qa', kind: 'stage_agent', scope: 'stage', alias: 'qa', stage_id: 'qa', reachable: true }
const human: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', reachable: true }
const roster = [dev, qa, human]

function only<T>(items: T[]): T {
  expect(items).toHaveLength(1)
  return items[0] as T
}

let seq = 0
function entry(key: string, status: string, author_id: string): MeshBoardEntry {
  seq += 1
  return { author_id, key, version: 1, status, seq, ts: '2026-10-21T00:00:00Z' }
}

const task = () => entry('task/csv', 'CSV export — dump rows', 'user')

describe('boardTasks', () => {
  test('a new task without reports is Todo and unassigned', () => {
    const t = only(boardTasks([task()], roster))
    expect(t.status).toBe('todo')
    expect(t.text).toBe('CSV export — dump rows')
    expect(t.assignees).toEqual([])
    expect(t.reports).toEqual([])
  })

  test("an agent's in progress report makes the task In progress for that agent, a later done makes it Done", () => {
    const start = entry('task/csv/dev', 'In Progress: wiring the exporter', dev.id)
    const working = only(boardTasks([task(), start], roster))
    expect(working.status).toBe('in_progress')
    expect(working.assignees).toEqual([{ id: dev.id, inProgress: true }])

    const finish = { ...entry('task/csv/dev', 'done: exporter shipped', dev.id), version: 2 }
    const done = only(boardTasks([task(), finish], roster))
    expect(done.status).toBe('done')
    expect(done.assignees).toEqual([{ id: dev.id, inProgress: false }])
    expect(done.reports.map((r) => r.entry.status)).toEqual(['done: exporter shipped'])
  })

  test('an unmarked legacy report stays visible but claims nothing', () => {
    const legacy = entry('task/csv/dev', 'looked at it, seems fine', dev.id)
    const t = only(boardTasks([task(), legacy], roster))
    expect(t.status).toBe('todo')
    expect(t.assignees).toEqual([])
    expect(t.reports.map((r) => r.entry.key)).toEqual(['task/csv/dev'])
  })

  test('markers must lead the text: a mention elsewhere is just a report', () => {
    const t = only(boardTasks([task(), entry('task/csv/dev', 'not done: blocked', dev.id)], roster))
    expect(t.status).toBe('todo')
  })

  test('a user-authored child report stays visible but changes neither state nor assignees', () => {
    const entries = [task(), entry('task/csv/dev', 'done: forged', 'user'), entry('task/csv/qa', 'in progress: forged', 'user')]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('todo')
    expect(t.assignees).toEqual([])
    expect(t.reports).toHaveLength(2)
  })

  test('an author missing from the roster cannot drive the status', () => {
    const t = only(boardTasks([task(), entry('task/csv/ghost', 'done: ok', 'run:t-ghost')], roster))
    expect(t.status).toBe('todo')
    expect(t.assignees).toEqual([])
  })

  test('the assignee is the entry author, not the key suffix', () => {
    const t = only(boardTasks([task(), entry('task/csv/qa', 'in progress: mine', dev.id)], roster))
    expect(t.assignees).toEqual([{ id: dev.id, inProgress: true }])
  })

  test.each([
    ['done first', ['done: a', 'in progress: b']],
    ['in progress first', ['in progress: b', 'done: a']],
  ])('one claimant Done and another In progress keeps the task In progress (%s)', (_, order) => {
    const reports = {
      'done: a': { key: 'task/csv/dev', author: dev.id },
      'in progress: b': { key: 'task/csv/qa', author: qa.id },
    }
    // The publish order is the seq order: the second published has the higher seq.
    const entries = order.map((text, i) => ({ ...entry(reports[text as keyof typeof reports].key, text, reports[text as keyof typeof reports].author), seq: 1000 + i }))
    const t = only(boardTasks([task(), ...entries], roster))
    expect(t.status).toBe('in_progress')
    expect(t.assignees.map((a) => [a.id, a.inProgress])).toEqual(
      order[0] === 'done: a' ? [[dev.id, false], [qa.id, true]] : [[qa.id, true], [dev.id, false]],
    )
  })

  test('all claimants Done makes the task Done', () => {
    const entries = [task(), entry('task/csv/dev', 'done: a', dev.id), entry('task/csv/qa', 'DONE: b', qa.id)]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('done')
    expect(t.assignees).toHaveLength(2)
  })

  test("an author's latest report (highest seq) wins when it has several keys", () => {
    const entries = [task(), entry('task/csv/dev', 'done: first', dev.id), entry('task/csv/dev-2', 'in progress: again', dev.id)]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('in_progress')
    expect(t.reports.filter((r) => r.entry.author_id === dev.id)).toHaveLength(1)
  })

  test('a nested key under a report is not a report: a later nested "in progress:" does not regress Done', () => {
    const entries = [task(), entry('task/csv/dev', 'done: delivered', dev.id), entry('task/csv/dev/note', 'in progress: unrelated note', dev.id)]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('done')
    expect(t.assignees).toEqual([{ id: dev.id, inProgress: false }])
    expect(t.reports.map((r) => r.entry.key)).toEqual(['task/csv/dev'])
  })

  test('a nested key from another agent cannot claim an otherwise unassigned task', () => {
    const entries = [task(), entry('task/csv/dev/extra', 'in progress: nested', qa.id)]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('todo')
    expect(t.assignees).toEqual([])
  })

  test('a key with an empty alias segment ("task/csv/") is not a report', () => {
    const entries = [task(), entry('task/csv/', 'done: empty alias', dev.id)]
    const t = only(boardTasks(entries, roster))
    expect(t.status).toBe('todo')
    expect(t.assignees).toEqual([])
    expect(t.reports).toEqual([])
  })

  test('facts, child reports and orphan keys never become task cards', () => {
    const entries = [
      entry('api/ready', 'yes', dev.id),
      entry('task/foo/bar', 'orphan report', dev.id),
      task(),
      entry('task/csv/dev', 'in progress: x', dev.id),
    ]
    expect(boardTasks(entries, roster).map((t) => t.key)).toEqual(['task/csv'])
    expect(boardTaskCount(entries)).toBe(1)
  })

  test('tasks keep board order and only their own reports', () => {
    const a = entry('task/a', 'A', 'user')
    const b = entry('task/b', 'B', 'user')
    const tasks = boardTasks([a, b, entry('task/b/dev', 'done: B', dev.id)], roster)
    expect(tasks.map((t) => [t.key, t.status])).toEqual([['task/a', 'todo'], ['task/b', 'done']])
  })
})

describe('boardTopLevel', () => {
  test('hides reports filed under an entry but keeps facts and orphan keys', () => {
    const entries = [entry('task/a', 'A', 'user'), entry('task/a/dev', 'done: A', dev.id), entry('api/ready', 'yes', dev.id), entry('task/x/y', 'orphan', dev.id)]
    expect(boardTopLevel(entries).map((e) => e.key)).toEqual(['task/a', 'api/ready', 'task/x/y'])
  })

  test('a task is never filed under a fact whose key happens to prefix it', () => {
    const fact = entry('task', 'a fact named task', dev.id)
    const csv = entry('task/csv', 'CSV export', 'user')
    const report = entry('task/csv/agent', 'done: exported', dev.id)
    const entries = [fact, csv, report]
    expect(boardTopLevel(entries).map((e) => e.key)).toEqual(['task', 'task/csv'])
    expect(boardReportsOf(fact, entries)).toEqual([])
    expect(boardReportsOf(csv, entries).map((e) => e.key)).toEqual(['task/csv/agent'])
  })
})
