import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { boardTasks, taskFromView, type BoardTask } from '../../lib/board-tasks'
import { typedAssignment, typedTaskView } from '../../test/board-tasks'
import tokensCss from '../../../skins/base/tokens.css?raw'
import meshCss from '../../../skins/base/mesh.css?raw'
import { BoardTaskCard } from './BoardTaskCard'
import { BoardTaskDetails } from './BoardTaskDetails'

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
    expect(within(card).getByText('@api')).toHaveAttribute('title', 'API')
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
    expect(card).toHaveAttribute('type', 'button')
    expect(card).toHaveAttribute('aria-haspopup', 'dialog')
    card.focus()
    expect(card).toHaveFocus()
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

// A long participant name (an unbreakable alias) must never push the filter or a card wider than a narrow screen.
describe('long participant names', () => {
  test('the assignee filter can shrink to the width it is given', () => {
    const rule = block(meshCss, '.board-view-assignee {')
    expect(rule).toMatch(/min-width:\s*0/)
    expect(rule).toMatch(/max-width:\s*100%/)
  })

  test("a card's assignee name is cut with an ellipsis", () => {
    expect(block(meshCss, '.board-task-assignee {')).toMatch(/max-width:\s*100%/)
    const name = block(meshCss, '.board-task-assignee-name {')
    expect(name).toMatch(/min-width:\s*0/)
    expect(name).toMatch(/overflow:\s*hidden/)
    expect(name).toMatch(/text-overflow:\s*ellipsis/)
    expect(name).toMatch(/white-space:\s*nowrap/)
  })

  test('the details and the Notify list wrap a long name instead of overflowing', () => {
    for (const selector of ['.board-task-details-assignee-name {', '.team-board-notify-option {']) {
      const rule = block(meshCss, selector)
      expect(rule).toMatch(/min-width:\s*0/)
      expect(rule).toMatch(/overflow-wrap:\s*anywhere/)
    }
  })

  test('the card and the details mark the name for those rules', () => {
    const task = boardTasks(
      [
        { author_id: 'user', key: 'task/a', version: 1, status: 'A', seq: 1, ts: '' },
        { author_id: 'run:t-1', key: 'task/a/qa', version: 1, status: 'in progress: x', seq: 2, ts: '' },
      ],
      participants,
    )[0]!
    render(<BoardTaskCard task={task} participants={participants} onOpen={() => {}} />)
    expect(document.querySelector('.board-task-assignee-name')).toHaveTextContent('@qa')
    render(<BoardTaskDetails task={task} participants={participants} onClose={() => {}} />)
    expect(document.querySelector('.board-task-details-assignee-name')).toHaveTextContent('QA')
  })
})

describe('typed task card', () => {
  test('a typed task shows its own title, description, latest note and each assignee with the assignment state', () => {
    const task = taskFromView(
      typedTaskView({
        key: 'task/csv',
        title: 'CSV — export',
        description: 'Add GET /todos.csv',
        status: 'in_progress',
        assignments: [
          typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'in_progress', progress: 'wiring', assigned_seq: 2, started_seq: 3 }),
          typedAssignment({ id: 'a2', participant_id: 'run:t-1' }),
        ],
      }),
    )
    render(<BoardTaskCard task={task} participants={participants} onOpen={() => {}} />)
    const card = screen.getByRole('button', { name: 'CSV — export, In progress' })
    expect(within(card).getByText('CSV — export')).toBeInTheDocument()
    expect(within(card).getByText('Add GET /todos.csv')).toBeInTheDocument()
    expect(within(card).getByText('wiring')).toBeInTheDocument()
    expect(within(card).getByText('@api', { selector: '.board-task-assignee-name' }).closest('.board-task-assignee')).toHaveTextContent('In progress')
    expect(within(card).getByText('@qa', { selector: '.board-task-assignee-name' }).closest('.board-task-assignee')).toHaveTextContent('Assigned')
    expect(within(card).queryByText(/report/)).toBeNull()
  })
})
