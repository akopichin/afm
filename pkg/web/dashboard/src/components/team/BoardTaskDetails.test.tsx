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

    test('History keeps the progress of an assignment released or reopened before it was done', () => {
      const ended = taskFromView(
        typedTaskView({
          key: 'task/csv',
          title: 'CSV export',
          history: [
            typedAssignment({ id: 'a0', participant_id: 'run:t-1', state: 'in_progress', progress: 'half the columns', ended: 'released', ended_by: 'user' }),
            typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'in_progress', progress: 'parser drafted', ended: 'reopened', ended_by: 'user' }),
          ],
        }),
      )
      const { dialog } = renderTyped(ended)
      const history = within(dialog).getByRole('list', { name: 'History' })
      expect(within(history).getByText('QA').closest('li')).toHaveTextContent('half the columns')
      expect(within(history).getByText('API').closest('li')).toHaveTextContent('parser drafted')
    })

    test('an agent picked to assign that stops being a candidate is neither shown nor sent', () => {
      const unassigned = taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export', version: 2 }))
      const { rerender } = render(<BoardTaskDetails task={unassigned} participants={participants} stages={stages} onClose={() => {}} />)
      fireEvent.change(screen.getByRole('combobox', { name: 'Assign to' }), { target: { value: 'run:t-1' } })
      const gone = participants.map((p) => (p.id === 'run:t-1' ? { ...p, reachable: false } : p))
      rerender(<BoardTaskDetails task={unassigned} participants={gone} stages={stages} onClose={() => {}} />)
      expect(screen.getByRole('combobox', { name: 'Assign to' })).toHaveValue('')
      expect(screen.getByRole('button', { name: 'Assign' })).toBeDisabled()
    })

    test('each assignment shows its artifact refs, active and in History', () => {
      const withRefs = taskFromView(
        typedTaskView({
          key: 'task/csv',
          title: 'CSV export',
          assignments: [typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'done', result: 'shipped', artifact_refs: ['artifacts/report.md'] })],
          history: [typedAssignment({ id: 'a0', participant_id: 'run:t-1', state: 'done', result: 'first draft', artifact_refs: ['artifacts/draft.md', 'plan.md'], ended: 'reopened', ended_by: 'user' })],
        }),
      )
      const { dialog } = renderTyped(withRefs)
      const active = within(within(dialog).getByRole('list', { name: 'Assignees' })).getByRole('list', { name: 'API artifacts' })
      expect(within(active).getByText('artifacts/report.md')).toBeInTheDocument()
      const archived = within(within(dialog).getByRole('list', { name: 'History' })).getByRole('list', { name: 'QA artifacts' })
      expect(within(archived).getByText('artifacts/draft.md')).toBeInTheDocument()
      expect(within(archived).getByText('plan.md')).toBeInTheDocument()
    })

    test('an assignment without artifact refs shows no artifacts list', () => {
      const { dialog } = renderTyped()
      expect(within(dialog).queryByRole('list', { name: /artifacts$/ })).toBeNull()
    })

    test('Assign never offers an Assistant (side agent) or a helper', () => {
      const withAssistant: MeshParticipant[] = [
        ...participants,
        { id: 'side:assistant', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'Assistant', reachable: true },
        { id: 'helper:api:rev', kind: 'helper', scope: 'stage', alias: 'rev', display_name: 'Reviewer', stage_id: 'api', reachable: true },
      ]
      const unassigned = taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export', version: 2 }))
      render(<BoardTaskDetails task={unassigned} participants={withAssistant} stages={stages} onClose={() => {}} />)
      const select = screen.getByRole('combobox', { name: 'Assign to' })
      expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['Choose an agent', 'API', 'QA'])
    })

    test('Release without a reason puts only the fields it changes on the wire', async () => {
      const receipt = { key: 'task/csv', version: 5, status: 'in_progress', seq: 9 }
      const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 200, json: async () => receipt } as Response)
      const { dialog, reload } = renderTyped()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
      await waitFor(() => expect(reload).toHaveBeenCalled())
      const body = JSON.parse((fetchSpy.mock.calls[0]?.[1] as RequestInit).body as string)
      expect(Object.keys(body).sort()).toEqual(['action', 'assignment_id', 'expected_version', 'key', 'request_id'])
    })

    describe('retry after a lost response', () => {
      // A newer poll of the same task: the lost change may have committed, and the task moved on.
      const newer = taskFromView(
        typedTaskView({
          key: 'task/csv',
          title: 'CSV export',
          version: 7,
          status: 'in_progress',
          assignments: [
            typedAssignment({ id: 'a1', participant_id: 'stage:api', state: 'in_progress', assigned_seq: 3, started_seq: 4 }),
            typedAssignment({ id: 'a9', participant_id: 'run:t-1', state: 'assigned', assigned_seq: 12 }),
          ],
          history: [typedAssignment({ id: 'a2', participant_id: 'run:t-1', ended: 'reopened', ended_by: 'user' })],
        }),
      )

      function renderLive(first: BoardTask) {
        const reload = vi.fn()
        const view = render(<BoardTaskDetails task={first} participants={participants} stages={stages} onChanged={reload} onClose={() => {}} />)
        const poll = (next: BoardTask) => view.rerender(<BoardTaskDetails task={next} participants={participants} stages={stages} onChanged={reload} onClose={() => {}} />)
        return { dialog: screen.getByRole('dialog', { name: 'Task details' }), reload, poll }
      }

      function lostThenOk() {
        return vi
          .spyOn(meshClient, 'postMeshTask')
          .mockRejectedValueOnce(new TypeError('Failed to fetch'))
          .mockResolvedValue({ key: 'task/csv', version: 8, status: 'todo', seq: 20 })
      }

      test('Reopen resends the original payload with the same request id after the task moved on', async () => {
        const post = lostThenOk()
        const { dialog, reload, poll } = renderLive(typed)
        fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'needs headers' } })
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('Failed to fetch'))
        poll(newer)
        fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'needs headers and quoting' } })
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(reload).toHaveBeenCalled())
        expect(post).toHaveBeenCalledTimes(2)
        expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'reopen', expectedVersion: 4, text: 'needs headers' })
        expect(post.mock.calls[1]?.[0]).toEqual(post.mock.calls[0]?.[0])
      })

      test('Release resends the original payload with the same request id after the task moved on', async () => {
        const post = lostThenOk()
        const { dialog, reload, poll } = renderLive(typed)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
        await waitFor(() => expect(within(dialog).getByRole('alert')).toBeInTheDocument())
        poll(newer)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
        await waitFor(() => expect(reload).toHaveBeenCalled())
        expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'release', assignmentId: 'a1', expectedVersion: 4 })
        expect(post.mock.calls[1]?.[0]).toEqual(post.mock.calls[0]?.[0])
      })

      test('Assign resends the original payload with the same request id after the task moved on', async () => {
        const post = lostThenOk()
        const unassigned = taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export', version: 2 }))
        const { dialog, reload, poll } = renderLive(unassigned)
        fireEvent.change(within(dialog).getByRole('combobox', { name: 'Assign to' }), { target: { value: 'stage:api' } })
        fireEvent.click(within(dialog).getByRole('button', { name: 'Assign' }))
        await waitFor(() => expect(within(dialog).getByRole('alert')).toBeInTheDocument())
        poll(taskFromView(typedTaskView({ key: 'task/csv', title: 'CSV export', version: 5 })))
        fireEvent.click(within(dialog).getByRole('button', { name: 'Assign' }))
        await waitFor(() => expect(reload).toHaveBeenCalled())
        expect(post.mock.calls[0]?.[0]).toMatchObject({ action: 'assign', to: 'stage:api', expectedVersion: 2 })
        expect(post.mock.calls[1]?.[0]).toEqual(post.mock.calls[0]?.[0])
      })

      test('after stale_revision the next click is a new change built from the fresh task', async () => {
        const post = vi
          .spyOn(meshClient, 'postMeshTask')
          .mockRejectedValueOnce(new meshClient.MeshApiError('/api/mesh/tasks', 409, 'stale_revision', 'version mismatch'))
          .mockResolvedValue({ key: 'task/csv', version: 8, status: 'todo', seq: 20 })
        const { dialog, poll } = renderLive(typed)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(within(dialog).getByRole('alert')).toHaveTextContent('Task changed, reloaded'))
        poll(newer)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
        expect(post.mock.calls[1]?.[0]).toMatchObject({ action: 'reopen', expectedVersion: 7 })
        expect(post.mock.calls[1]?.[0].requestId).not.toBe(post.mock.calls[0]?.[0].requestId)
      })

      test('after a success the next click is a new change built from the fresh task', async () => {
        const post = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv', version: 8, status: 'todo', seq: 20 })
        const { dialog, reload, poll } = renderLive(typed)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
        poll(newer)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(reload).toHaveBeenCalledTimes(2))
        expect(post.mock.calls[1]?.[0]).toMatchObject({ action: 'reopen', expectedVersion: 7 })
        expect(post.mock.calls[1]?.[0].requestId).not.toBe(post.mock.calls[0]?.[0].requestId)
      })

      test('a different action after a lost response is built from the fresh task', async () => {
        const post = lostThenOk()
        const { dialog, reload, poll } = renderLive(typed)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Release API' }))
        await waitFor(() => expect(within(dialog).getByRole('alert')).toBeInTheDocument())
        poll(newer)
        fireEvent.click(within(dialog).getByRole('button', { name: 'Reopen' }))
        await waitFor(() => expect(reload).toHaveBeenCalled())
        expect(post.mock.calls[1]?.[0]).toMatchObject({ action: 'reopen', expectedVersion: 7 })
      })
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
