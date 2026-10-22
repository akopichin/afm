import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { boardTasks, taskFromView, type BoardTask } from '../../lib/board-tasks'
import { typedAssignment, typedTaskView } from '../../test/board-tasks'
import { BoardTaskDetails } from './BoardTaskDetails'
import { DialogOpenContext } from '../../lib/focus-trap'

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
  test('details in a hidden Team do not count as open and ignore Escape, until shown again', () => {
    const reports: boolean[] = []
    const report = (open: boolean) => { reports.push(open) }
    const onClose = vi.fn()
    const view = render(<DialogOpenContext.Provider value={report}><BoardTaskDetails task={twoClaimants} participants={participants} onClose={onClose} active={false} /></DialogOpenContext.Provider>)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    expect(reports).toEqual([])
    view.rerender(<DialogOpenContext.Provider value={report}><BoardTaskDetails task={twoClaimants} participants={participants} onClose={onClose} active /></DialogOpenContext.Provider>)
    expect(reports).toEqual([true])
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

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

  describe('typed task', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    const stages = [{ id: 'api', title: 'API', status: 'running' }]
    const typed = taskFromView(
      typedTaskView({
        key: 'task/csv',
        title: 'CSV export',
        description: 'Add GET /todos.csv',
        version: 4,
        status: 'in_progress',
        assignments: [
          typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'in_progress', progress: 'wiring the handler', assigned_seq: 3, started_seq: 4 }),
          typedAssignment({ id: 'a2', participant_id: 'run:t-1', state: 'assigned', assigned_seq: 5 }),
          typedAssignment({ id: 'a3', participant_id: 'run:gone', state: 'done', result: 'reviewed', assigned_seq: 2, completed_seq: 3 }),
        ],
        history: [typedAssignment({ id: 'a0', participant_id: 'run:t-1', ended: 'released', ended_by: 'user', end_reason: 'wrong agent' })],
      }),
    )

    function renderTyped(task: BoardTask = typed, reload = vi.fn()) {
      render(<BoardTaskDetails task={task} participants={participants} stages={stages} onChanged={reload} onClose={() => {}} />)
      return { dialog: screen.getByRole('dialog', { name: 'Task details' }), reload }
    }

    test('shows title, description, the latest note and each assignment with its own state', () => {
      const { dialog } = renderTyped()
      expect(within(dialog).getByRole('heading', { name: 'CSV export' })).toBeInTheDocument()
      expect(within(dialog).getByText('Add GET /todos.csv')).toBeInTheDocument()
      expect(within(dialog).getByText('wiring the handler', { selector: '.board-task-details-progress' })).toBeInTheDocument()
      expect(within(dialog).getByText('v4')).toBeInTheDocument()
      const assignees = within(dialog).getByRole('list', { name: 'Assignees' })
      expect(within(assignees).getByText('API').closest('li')).toHaveTextContent('In progress')
      expect(within(assignees).getByText('QA').closest('li')).toHaveTextContent('Assigned')
      expect(within(assignees).getByText('gone').closest('li')).toHaveTextContent('Done')
    })

    test('history lists ended assignments with who ended them and why', () => {
      const { dialog } = renderTyped()
      const history = within(dialog).getByRole('list', { name: 'History' })
      expect(history).toHaveTextContent('QA')
      expect(history).toHaveTextContent('Released by You')
      expect(history).toHaveTextContent('wrong agent')
    })

    test('Release is offered only on active assignments that are not done', () => {
      const { dialog } = renderTyped()
      expect(within(dialog).getByRole('button', { name: 'Release API' })).toBeInTheDocument()
      expect(within(dialog).getByRole('button', { name: 'Release QA' })).toBeInTheDocument()
      expect(within(dialog).queryByRole('button', { name: 'Release gone' })).toBeNull()
    })

    test('Release posts the assignment with the expected version and reloads the board', async () => {
      const post = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv', version: 5, status: 'in_progress', seq: 9 })
      const { dialog, reload } = renderTyped()
      fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'blocked' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
      await waitFor(() => expect(reload).toHaveBeenCalled())
      expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'release', key: 'task/csv', assignmentId: 'a1', expectedVersion: 4, text: 'blocked' })
      expect(within(dialog).getByLabelText('Reason')).toHaveValue('')
    })

    test('a stale version says the task changed, reloads and keeps the typed reason', async () => {
      vi.spyOn(meshClient, 'postMeshTask').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/tasks', 409, 'stale_revision', 'version mismatch'))
      const { dialog, reload } = renderTyped()
      fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'blocked' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
      await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('Task changed, reloaded'))
      expect(reload).toHaveBeenCalled()
      expect(within(dialog).getByLabelText('Reason')).toHaveValue('blocked')
    })

    test('Assign hands the task to a picked agent', async () => {
      const post = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv', version: 5, status: 'in_progress', seq: 9 })
      const unassigned = taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export', version: 2 }))
      const { dialog, reload } = renderTyped(unassigned)
      const select = within(dialog).getByRole('combobox', { name: 'Assign to' })
      expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['Choose an agent', 'API', 'QA'])
      fireEvent.change(select, { target: { value: 'run:t-1' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Assign' }))
      await waitFor(() => expect(reload).toHaveBeenCalled())
      expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'assign', key: 'task/csv', to: 'run:t-1', expectedVersion: 2 })
    })

    test('Reopen is offered once the task had assignments and posts the reason', async () => {
      const post = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv', version: 5, status: 'todo', seq: 9 })
      const { dialog } = renderTyped()
      fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'needs CSV headers' } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
      await waitFor(() => expect(post).toHaveBeenCalled())
      expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'reopen', key: 'task/csv', expectedVersion: 4, text: 'needs CSV headers' })
      expect(post.mock.calls[0]?.[0].assignmentId).toBeUndefined()
    })

    test('a fresh task has no Reopen and no Release', () => {
      const { dialog } = renderTyped(taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export' })))
      expect(within(dialog).queryByRole('button', { name: 'Reopen' })).toBeNull()
      expect(within(dialog).queryByRole('button', { name: /^Release/ })).toBeNull()
      expect(within(dialog).getByText('Unassigned')).toBeInTheDocument()
    })

    test('a done task offers no Assign — Reopen is the way forward', () => {
      const done = taskFromView(
        typedTaskView({ key: 'task/csv', title: 'CSV export', status: 'done', assignments: [typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'done', result: 'shipped' })] }),
      )
      const { dialog } = renderTyped(done)
      expect(within(dialog).queryByRole('combobox', { name: 'Assign to' })).toBeNull()
      expect(within(dialog).queryByRole('button', { name: 'Assign' })).toBeNull()
      expect(within(dialog).getByRole('button', { name: 'Reopen' })).toBeInTheDocument()
    })

    test('a todo or in-progress task offers Assign', () => {
      const { dialog } = renderTyped()
      expect(within(dialog).getByRole('combobox', { name: 'Assign to' })).toBeInTheDocument()
      expect(within(dialog).getByRole('button', { name: 'Assign' })).toBeInTheDocument()
    })

    test("each assignment shows its own result or progress, not only the latest note", () => {
      const { dialog } = renderTyped()
      const assignees = within(dialog).getByRole('list', { name: 'Assignees' })
      expect(within(assignees).getByText('API').closest('li')).toHaveTextContent('wiring the handler')
      // run:gone's result is older than API's progress, so it is not the task's note — it still shows.
      expect(within(assignees).getByText('gone').closest('li')).toHaveTextContent('reviewed')
    })

    test('History shows an ended assignment\'s result when it has one', () => {
      const withResult = taskFromView(
        typedTaskView({
          key: 'task/csv',
          title: 'CSV export',
          history: [typedAssignment({ id: 'a0', participant_id: 'run:t-1', state: 'done', result: 'first draft', ended: 'reopened', ended_by: 'user' })],
        }),
      )
      const { dialog } = renderTyped(withResult)
      expect(within(dialog).getByRole('list', { name: 'History' })).toHaveTextContent('first draft')
    })

    test('focus starts on Close and Escape closes, as for a legacy task', () => {
      const onClose = vi.fn()
      render(<BoardTaskDetails task={typed} participants={participants} stages={stages} onChanged={() => {}} onClose={onClose} />)
      expect(screen.getByRole('button', { name: 'Close task details' })).toHaveFocus()
      fireEvent.keyDown(document, { key: 'Escape' })
      expect(onClose).toHaveBeenCalledTimes(1)
    })
  })
})
