import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { boardTasks, type BoardTask } from '../../lib/board-tasks'
import tokensCss from '../../../skins/base/tokens.css?raw'
import meshCss from '../../../skins/base/mesh.css?raw'
import { BoardTaskCard } from './BoardTaskCard'

const participants: MeshParticipant[] = [
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
]

function entry(author_id: string, key: string, status: string, seq: number): MeshBoardEntry {
  return { author_id, key, version: 1, status, seq, ts: '2026-10-18T00:00:00Z' }
}

function taskOf(...reports: [string, string][]): BoardTask {
  const entries = [
    entry('user', 'task/csv-export', 'CSV export — Add GET /todos.csv', 1),
    ...reports.map(([author, text], i) => entry(author, `task/csv-export/${author === 'stage:api' ? 'api' : 'qa'}`, text, i + 2)),
  ]
  const task = boardTasks(entries, participants)[0]
  if (task === undefined) throw new Error('no task')
  return task
}

describe('BoardTaskCard', () => {
  test('shows key, title, description and "Unassigned" for a task nobody claimed', () => {
    render(<BoardTaskCard task={taskOf()} participants={participants} onOpen={() => {}} />)
    const card = screen.getByRole('button', { name: 'CSV export, Todo' })
    expect(within(card).getByText('task/csv-export')).toBeInTheDocument()
    expect(within(card).getByText('CSV export')).toBeInTheDocument()
    expect(within(card).getByText('Add GET /todos.csv')).toBeInTheDocument()
    expect(within(card).getByText('Unassigned')).toBeInTheDocument()
  })

  test('a task text without a description is just a title', () => {
    const entries = [entry('stage:api', 'task/x', 'Fix the flaky test', 1)]
    const task = boardTasks(entries, participants)[0]!
    render(<BoardTaskCard task={task} participants={participants} onOpen={() => {}} />)
    expect(screen.getByRole('button', { name: 'Fix the flaky test, Todo' })).toBeInTheDocument()
  })

  test.each([
    ['todo', [], 'Todo'],
    ['in_progress', [['stage:api', 'in progress: wiring']], 'In progress'],
    ['done', [['stage:api', 'done: shipped']], 'Done'],
  ] as const)('%s task carries a text label, an icon and the status hook', (status, reports, label) => {
    render(<BoardTaskCard task={taskOf(...reports.map(([a, t]) => [a, t] as [string, string]))} participants={participants} onOpen={() => {}} />)
    const card = screen.getByRole('button', { name: `CSV export, ${label}` })
    expect(card).toHaveAttribute('data-status', status)
    const badge = within(card).getByText(label).closest('.board-task-status')
    expect(badge).toHaveAttribute('data-status', status)
    expect(badge?.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
  })

  test('lists every claimant by name, the report count and the latest report', () => {
    const task = taskOf(['stage:api', 'in progress: wiring'], ['run:t-1', 'done: checked'])
    render(<BoardTaskCard task={task} participants={participants} onOpen={() => {}} />)
    const card = screen.getByRole('button', { name: 'CSV export, In progress' })
    expect(within(card).getByText('API')).toBeInTheDocument()
    expect(within(card).getAllByText('QA').length).toBeGreaterThan(0)
    expect(within(card).queryByText('Unassigned')).toBeNull()
    expect(within(card).getByText('2 reports')).toBeInTheDocument()
    expect(within(card).getByText(/done: checked/)).toBeInTheDocument()
  })

  test('activating the card opens the task and is keyboard operable (a real button)', () => {
    const task = taskOf()
    const onOpen = vi.fn()
    render(<BoardTaskCard task={task} participants={participants} onOpen={onOpen} />)
    const card = screen.getByRole('button', { name: 'CSV export, Todo' })
    expect(card.tagName).toBe('BUTTON')
    fireEvent.click(card)
    expect(onOpen).toHaveBeenCalledWith(task)
  })
})

// block — the declarations of the first rule whose selector starts with `selector`.
function block(css: string, selector: string): string {
  const start = css.indexOf(selector)
  if (start < 0) return ''
  return css.slice(css.indexOf('{', start), css.indexOf('}', start))
}

describe('board status colors', () => {
  const statuses = [
    ['todo', 'todo'],
    ['in_progress', 'in-progress'],
    ['done', 'done'],
  ] as const

  test.each(statuses)('%s has foreground, background and line tokens in the dark default and the light theme', (_status, token) => {
    const dark = block(tokensCss, ':root {')
    const light = block(tokensCss, ':root[data-theme="light"]')
    for (const part of ['', '-bg', '-line']) {
      expect(dark).toMatch(new RegExp(`--task-${token}${part}:`))
      expect(light).toMatch(new RegExp(`--task-${token}${part}:`))
    }
  })

  test.each(statuses)('mesh.css maps data-status="%s" to its tokens', (status, token) => {
    const rule = block(meshCss, `[data-status="${status}"]`)
    expect(rule).toContain(`var(--task-${token})`)
    expect(rule).toContain(`var(--task-${token}-bg)`)
    expect(rule).toContain(`var(--task-${token}-line)`)
  })

  test('gray, blue and green are different hues in the dark default', () => {
    const dark = block(tokensCss, ':root {')
    const fg = (t: string) => dark.match(new RegExp(`--task-${t}:\\s*([^;]+);`))?.[1]?.trim()
    expect(new Set([fg('todo'), fg('in-progress'), fg('done')]).size).toBe(3)
  })
})
