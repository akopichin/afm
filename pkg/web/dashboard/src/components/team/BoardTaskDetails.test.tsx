import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { boardTasks, type BoardTask } from '../../lib/board-tasks'
import { BoardTaskDetails } from './BoardTaskDetails'

const participants: MeshParticipant[] = [
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
]

function entry(author_id: string, key: string, status: string, seq: number): MeshBoardEntry {
  return { author_id, key, version: 1, status, seq, ts: '2026-10-18T00:00:00Z' }
}

function task(entries: MeshBoardEntry[]): BoardTask {
  const t = boardTasks(entries, participants)[0]
  if (t === undefined) throw new Error('no task')
  return t
}

const base = entry('user', 'task/csv-export', 'CSV export — Add GET /todos.csv', 1)
const twoClaimants = task([
  base,
  entry('stage:api', 'task/csv-export/api', 'done: endpoint added', 2),
  entry('run:t-1', 'task/csv-export/qa', 'in progress: checking edge cases', 3),
])

describe('BoardTaskDetails', () => {
  test('shows the task version and each artifact ref', () => {
    const withRefs = task([{ ...base, version: 2, artifact_refs: ['plan.md', 'artifacts/report.txt'] }])
    render(<BoardTaskDetails task={withRefs} participants={participants} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByText('v2')).toBeInTheDocument()
    const refs = within(dialog).getByRole('list', { name: 'Artifacts' })
    expect(within(refs).getByText('plan.md')).toBeInTheDocument()
    expect(within(refs).getByText('artifacts/report.txt')).toBeInTheDocument()
  })

  test('a task without artifact refs shows the version but no artifacts list', () => {
    render(<BoardTaskDetails task={task([base])} participants={participants} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByText('v1')).toBeInTheDocument()
    expect(within(dialog).queryByRole('list', { name: 'Artifacts' })).not.toBeInTheDocument()
  })

  test('shows status, key, text, author, every claimant and each agent\'s current report', () => {
    render(<BoardTaskDetails task={twoClaimants} participants={participants} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByRole('heading', { name: 'CSV export' })).toBeInTheDocument()
    expect(within(dialog).getByText('task/csv-export')).toBeInTheDocument()
    expect(within(dialog).getByText('Add GET /todos.csv')).toBeInTheDocument()
    expect(within(dialog).getByText('In progress', { selector: '.board-task-status *' })).toBeInTheDocument()
    expect(within(dialog).getByText('You')).toBeInTheDocument()

    const assignees = within(dialog).getByRole('list', { name: 'Assignees' })
    expect(within(assignees).getByText('API').closest('li')).toHaveTextContent('Done')
    expect(within(assignees).getByText('QA').closest('li')).toHaveTextContent('In progress')

    const reports = within(dialog).getByRole('list', { name: 'Reports' })
    expect(within(reports).getByText('done: endpoint added')).toBeInTheDocument()
    expect(within(reports).getByText('in progress: checking edge cases')).toBeInTheDocument()
  })

  test('a task nobody claimed says so and has no reports section content', () => {
    render(<BoardTaskDetails task={task([base])} participants={participants} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByText('Unassigned')).toBeInTheDocument()
    expect(within(dialog).getByText('No reports yet.')).toBeInTheDocument()
  })

  test('offers no way to change the status — Close is the only action', () => {
    render(<BoardTaskDetails task={twoClaimants} participants={participants} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getAllByRole('button').map((b) => b.getAttribute('aria-label') ?? b.textContent)).toEqual(['Close task details'])
    expect(within(dialog).queryByRole('combobox')).toBeNull()
    expect(within(dialog).queryByRole('textbox')).toBeNull()
    expect(within(dialog).queryByRole('checkbox')).toBeNull()
    expect(within(dialog).queryByRole('radio')).toBeNull()
    expect(within(dialog).getByText(/updated by agents/i)).toBeInTheDocument()
  })

  test('Close button and Escape both close; focus starts inside and returns to the opener', () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const onClose = vi.fn()
    const { unmount } = render(<BoardTaskDetails task={twoClaimants} participants={participants} onClose={onClose} />)
    const close = screen.getByRole('button', { name: 'Close task details' })
    expect(close).toHaveFocus()
    fireEvent.click(close)
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(2)
    unmount()
    expect(opener).toHaveFocus()
    opener.remove()
  })

  test('a report that arrives while the dialog is open shows without reopening', () => {
    const { rerender } = render(<BoardTaskDetails task={task([base])} participants={participants} onClose={() => {}} />)
    expect(screen.getByText('No reports yet.')).toBeInTheDocument()
    rerender(<BoardTaskDetails task={twoClaimants} participants={participants} onClose={() => {}} />)
    expect(screen.getByText('done: endpoint added')).toBeInTheDocument()
  })
})
