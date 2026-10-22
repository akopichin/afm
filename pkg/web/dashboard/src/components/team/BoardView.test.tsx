import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import type { MeshBoard } from '../../hooks/use-mesh'
import { BoardLoadState } from './BoardLoadState'
import { BoardView } from './BoardView'
import { typedAssignment, typedTaskView } from '../../test/board-tasks'

const participants: MeshParticipant[] = [
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
]
const stages = [{ id: 'api', title: 'API', status: 'running' }]

function entry(author_id: string, key: string, status: string, seq: number): MeshBoardEntry {
  return { author_id, key, version: 1, status, seq, ts: '2026-10-18T00:00:00Z' }
}

// A board with one task per situation: todo, in progress, done and one with two claimants.
const entries: MeshBoardEntry[] = [
  entry('user', 'task/csv-export', 'CSV export — Add GET /todos.csv', 1),
  entry('user', 'task/login-fix', 'Login fix — Session cookie expires early', 2),
  entry('stage:api', 'task/login-fix/api', 'in progress: reproducing', 3),
  entry('user', 'task/docs', 'Docs', 4),
  entry('stage:api', 'task/docs/api', 'done: written', 5),
  entry('user', 'task/release', 'Release', 6),
  entry('stage:api', 'task/release/api', 'done: tagged', 7),
  entry('run:t-1', 'task/release/qa', 'in progress: smoke test', 8),
]

function boardOf(list: MeshBoardEntry[], overrides: Partial<MeshBoard> = {}): MeshBoard {
  return { entries: list, tasks: [], typedTasks: false, status: 'ready', reload: vi.fn(() => Promise.resolve()), ...overrides }
}

function renderView(board: MeshBoard) {
  return render(<BoardView board={board} participants={participants} stages={stages} />)
}

function lane(name: string): HTMLElement {
  return screen.getByRole('region', { name: `${name} tasks` })
}

function cardTitles(laneName: string): string[] {
  return within(lane(laneName))
    .queryAllByRole('button')
    .map((b) => b.getAttribute('aria-label') ?? '')
}

describe('BoardView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('renders Todo, In progress and Done lanes in that order, each task in exactly one', () => {
    renderView(boardOf(entries))
    const lanes = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(lanes).toEqual(['Todo tasks', 'In progress tasks', 'Done tasks'])
    expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
    expect(cardTitles('In progress')).toEqual(['Login fix, In progress', 'Release, In progress'])
    expect(cardTitles('Done')).toEqual(['Docs, Done'])
  })

  test('shows a count per lane and keeps an empty lane visible with 0', () => {
    renderView(boardOf([entry('user', 'task/csv-export', 'CSV export', 1)]))
    expect(within(lane('Todo')).getByText('1')).toBeInTheDocument()
    expect(within(lane('In progress')).getByText('0')).toBeInTheDocument()
    expect(within(lane('Done')).getByText('0')).toBeInTheDocument()
    expect(within(lane('Done')).getByText('No tasks')).toBeInTheDocument()
  })

  test('an empty board still shows the three lanes and the New task button', () => {
    renderView(boardOf([]))
    expect(screen.getAllByRole('region')).toHaveLength(3)
    expect(screen.getByRole('button', { name: 'New task' })).toBeInTheDocument()
  })

  test('shows how many tasks are completed', () => {
    renderView(boardOf(entries))
    expect(screen.getByText('1 of 4 completed')).toBeInTheDocument()
  })

  test('a changed board moves the card between lanes', () => {
    const { rerender } = renderView(boardOf(entries))
    expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
    rerender(
      <BoardView
        board={boardOf([...entries, entry('run:t-1', 'task/csv-export/qa', 'done: shipped', 9)])}
        participants={participants}
        stages={stages}
      />,
    )
    expect(cardTitles('Todo')).toEqual([])
    expect(cardTitles('Done')).toEqual(['CSV export, Done', 'Docs, Done'])
    expect(screen.getByText('2 of 4 completed')).toBeInTheDocument()
  })

  test('search matches the key, the title and the details', () => {
    renderView(boardOf(entries))
    const search = screen.getByRole('searchbox', { name: 'Search tasks' })
    fireEvent.change(search, { target: { value: 'COOKIE' } })
    expect(cardTitles('In progress')).toEqual(['Login fix, In progress'])
    expect(cardTitles('Todo')).toEqual([])
    fireEvent.change(search, { target: { value: 'csv' } })
    expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
    fireEvent.change(search, { target: { value: 'task/docs' } })
    expect(cardTitles('Done')).toEqual(['Docs, Done'])
    expect(within(lane('Todo')).getByText('No matching tasks')).toBeInTheDocument()
  })

  test('the assignee filter lists the live roster, Unassigned, and matches any claimant', () => {
    renderView(boardOf(entries))
    const filter = screen.getByRole('combobox', { name: 'Assignee' })
    expect(within(filter).getAllByRole('option').map((o) => o.textContent)).toEqual(['All assignees', 'Unassigned', 'API', 'QA'])

    fireEvent.change(filter, { target: { value: 'run:t-1' } })
    expect(cardTitles('In progress')).toEqual(['Release, In progress'])
    expect(cardTitles('Done')).toEqual([])

    fireEvent.change(filter, { target: { value: 'stage:api' } })
    expect(cardTitles('In progress')).toEqual(['Login fix, In progress', 'Release, In progress'])
    expect(cardTitles('Done')).toEqual(['Docs, Done'])

    fireEvent.change(filter, { target: { value: 'unassigned' } })
    expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
    expect(cardTitles('In progress')).toEqual([])
  })

  test('a filter says how many tasks it shows', () => {
    renderView(boardOf(entries))
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search tasks' }), { target: { value: 'docs' } })
    expect(screen.getByText('1 shown')).toBeInTheDocument()
    expect(screen.getByText('1 of 4 completed')).toBeInTheDocument()
  })

  test('a card opens the task details, Escape closes them and focus returns to the card', () => {
    renderView(boardOf(entries))
    const card = screen.getByRole('button', { name: 'Login fix, In progress' })
    card.focus()
    fireEvent.click(card)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByText('in progress: reproducing')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(card).toHaveFocus()
  })

  test('the open details follow the board poll', () => {
    const { rerender } = renderView(boardOf(entries))
    fireEvent.click(screen.getByRole('button', { name: 'Login fix, In progress' }))
    rerender(
      <BoardView
        board={boardOf([...entries, entry('stage:api', 'task/login-fix/api', 'done: fixed', 9)])}
        participants={participants}
        stages={stages}
      />,
    )
    expect(within(screen.getByRole('dialog', { name: 'Task details' })).getByText('done: fixed')).toBeInTheDocument()
  })

  test('the New task form does not post until the board has loaded', () => {
    renderView(boardOf([], { status: 'loading', typedTasks: false }))
    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    expect(screen.getByRole('button', { name: 'Post task' })).toBeDisabled()
  })

  test('New task opens a dialog with the task form; Escape closes it', () => {
    renderView(boardOf(entries))
    const opener = screen.getByRole('button', { name: 'New task' })
    opener.focus()
    fireEvent.click(opener)
    const dialog = screen.getByRole('dialog', { name: 'New task' })
    expect(within(dialog).getByLabelText('Task')).toHaveFocus()
    expect(within(dialog).getByRole('button', { name: 'Post task' })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(opener).toHaveFocus()
  })

  test('a posted task is revealed in Todo even when the filters hid it: filters clear, announced, focus back on New task', async () => {
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(entry('user', 'task/new-thing', 'New thing', 10))
    const posted = entry('user', 'task/new-thing', 'New thing', 10)
    const reload = vi.fn(() => Promise.resolve())
    const { rerender } = renderView(boardOf(entries, { reload }))

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search tasks' }), { target: { value: 'docs' } })
    fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), { target: { value: 'stage:api' } })

    const opener = screen.getByRole('button', { name: 'New task' })
    opener.focus()
    fireEvent.click(opener)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))

    await waitFor(() => expect(reload).toHaveBeenCalled())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('searchbox', { name: 'Search tasks' })).toHaveValue('')
    expect(screen.getByRole('combobox', { name: 'Assignee' })).toHaveValue('all')
    expect(screen.getByRole('status', { name: 'Board updates' })).toHaveTextContent('Task added to Todo')
    expect(opener).toHaveFocus()

    // The poll delivers the new task: it shows in Todo, focus stays where it was.
    rerender(<BoardView board={boardOf([...entries, posted], { reload })} participants={participants} stages={stages} />)
    expect(within(lane('Todo')).getByRole('button', { name: 'New thing, Todo' })).toBeInTheDocument()
    expect(opener).toHaveFocus()
  })

  test('a posted task that arrives while the operator types in search does not take focus', async () => {
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(entry('user', 'task/new-thing', 'New thing', 10))
    const reload = vi.fn(() => Promise.resolve())
    const { rerender } = renderView(boardOf(entries, { reload }))

    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    const search = screen.getByRole('searchbox', { name: 'Search tasks' })
    search.focus()
    fireEvent.change(search, { target: { value: 'new' } })
    rerender(<BoardView board={boardOf([...entries, entry('user', 'task/new-thing', 'New thing', 10)], { reload })} participants={participants} stages={stages} />)
    expect(within(lane('Todo')).getByRole('button', { name: 'New thing, Todo' })).toBeInTheDocument()
    expect(search).toHaveFocus()
  })

  test('a posted task that arrives while a dialog the Board does not own is open leaves focus in that dialog', async () => {
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(entry('user', 'task/new-thing', 'New thing', 10))
    const reload = vi.fn(() => Promise.resolve())
    const view = (list: MeshBoardEntry[]) => (
      <>
        <BoardView board={boardOf(list, { reload })} participants={participants} stages={stages} />
        <div role="dialog" aria-modal="true" aria-label="Other">
          <button type="button">Inside other dialog</button>
        </div>
      </>
    )
    const { rerender } = render(view(entries))
    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New task' })).toBeNull())

    const inside = screen.getByRole('button', { name: 'Inside other dialog' })
    inside.focus()
    rerender(view([...entries, entry('user', 'task/new-thing', 'New thing', 10)]))
    expect(inside).toHaveFocus()
  })

  test('the board shows no status control: agents own the status', () => {
    renderView(boardOf(entries))
    expect(screen.queryByRole('radio')).toBeNull()
    // The only select is the assignee filter.
    expect(screen.getAllByRole('combobox')).toHaveLength(1)
  })

  // App wraps the Board tab in BoardLoadState: a failed refresh and its recovery must keep the open form, its
  // text and the filters.
  test('ready -> stale -> ready keeps the filters and the open New task form with its text', () => {
    const view = (status: MeshBoard['status']) => {
      const board = boardOf(entries, { status })
      return (
        <BoardLoadState board={board}>
          <BoardView board={board} participants={participants} stages={stages} />
        </BoardLoadState>
      )
    }
    const { rerender } = render(view('ready'))
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search tasks' }), { target: { value: 'docs' } })
    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'Half typed' } })

    rerender(view('stale'))
    expect(screen.getByRole('alert')).toHaveTextContent('out of date')
    expect(screen.getByLabelText('Task')).toHaveValue('Half typed')
    rerender(view('ready'))
    expect(screen.getByRole('dialog', { name: 'New task' })).toBeInTheDocument()
    expect(screen.getByLabelText('Task')).toHaveValue('Half typed')
    expect(screen.getByRole('searchbox', { name: 'Search tasks' })).toHaveValue('docs')
  })

  test('text typed while a task is being posted keeps the New task dialog open with that text', async () => {
    let resolvePublish!: (v: MeshBoardEntry) => void
    vi.spyOn(meshClient, 'publishMeshBoard').mockReturnValue(new Promise((res) => (resolvePublish = res)))
    const reload = vi.fn(() => Promise.resolve())
    renderView(boardOf(entries, { reload }))

    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV import' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'next: XLSX import' } })
    await act(async () => resolvePublish(entry('user', 'task/csv-import', 'CSV import', 10)))

    expect(reload).toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: 'New task' })
    expect(within(dialog).getByLabelText('Task')).toHaveValue('')
    expect(within(dialog).getByLabelText('Details')).toHaveValue('next: XLSX import')
    expect(screen.getByRole('status', { name: 'Board updates' })).toHaveTextContent('Task added to Todo')
  })

  test('closing the details of a task that moved to another lane returns focus to its card there', () => {
    const { rerender } = renderView(boardOf(entries))
    const card = screen.getByRole('button', { name: 'CSV export, Todo' })
    card.focus()
    fireEvent.click(card)
    rerender(
      <BoardView
        board={boardOf([...entries, entry('run:t-1', 'task/csv-export/qa', 'done: shipped', 9)])}
        participants={participants}
        stages={stages}
      />,
    )
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(within(lane('Done')).getByRole('button', { name: 'CSV export, Done' })).toHaveFocus()
  })

  test('a posted task that arrives while another dialog is open does not take focus behind it', async () => {
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(entry('user', 'task/new-thing', 'New thing', 10))
    const reload = vi.fn(() => Promise.resolve())
    const { rerender } = renderView(boardOf(entries, { reload }))

    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    // Before the poll brings the new card, the operator opens another task.
    fireEvent.click(screen.getByRole('button', { name: 'Docs, Done' }))
    const details = screen.getByRole('dialog', { name: 'Task details' })
    const close = within(details).getByRole('button', { name: 'Close task details' })
    expect(close).toHaveFocus()

    rerender(<BoardView board={boardOf([...entries, entry('user', 'task/new-thing', 'New thing', 10)], { reload })} participants={participants} stages={stages} />)
    expect(close).toHaveFocus()
  })

  describe('typed tasks', () => {
    const typedTasks = [
      typedTaskView({ key: 'task/csv', title: 'CSV export', description: 'Add GET /todos.csv', assignments: [typedAssignment({ id: 'a1', participant_id: 'run:t-1' })] }),
      typedTaskView({ key: 'task/docs', title: 'Docs', status: 'done', assignments: [typedAssignment({ id: 'a2', participant_id: 'stage:api', state: 'done', result: 'written' })] }),
      typedTaskView({ key: 'task/gone', title: 'Old agent work', status: 'in_progress', assignments: [typedAssignment({ id: 'a3', participant_id: 'run:gone', state: 'in_progress' })] }),
    ]
    const typedBoard = () => boardOf([], { tasks: typedTasks, typedTasks: true })

    test('an assigned-only task stays in Todo with its assignee named, not Unassigned', () => {
      renderView(typedBoard())
      expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
      const card = within(lane('Todo')).getByRole('button', { name: 'CSV export, Todo' })
      expect(within(card).getByText('QA', { selector: '.board-task-assignee-name' })).toBeInTheDocument()
      expect(within(card).getByText('Assigned')).toBeInTheDocument()
      expect(within(card).queryByText('Unassigned')).toBeNull()
    })

    test('a done task is in Done and counts as completed', () => {
      renderView(typedBoard())
      expect(cardTitles('Done')).toEqual(['Docs, Done'])
      expect(cardTitles('In progress')).toEqual(['Old agent work, In progress'])
      expect(screen.getByText('1 of 3 completed')).toBeInTheDocument()
    })

    test('the assignee filter offers and matches a participant missing from the roster', () => {
      renderView(typedBoard())
      const filter = screen.getByRole('combobox', { name: 'Assignee' })
      expect(within(filter).getAllByRole('option').map((o) => o.getAttribute('value'))).toContain('run:gone')
      fireEvent.change(filter, { target: { value: 'run:gone' } })
      expect(cardTitles('In progress')).toEqual(['Old agent work, In progress'])
      expect(cardTitles('Todo')).toEqual([])
    })

    test('search matches the title and the description', () => {
      renderView(typedBoard())
      fireEvent.change(screen.getByRole('searchbox', { name: 'Search tasks' }), { target: { value: 'todos.csv' } })
      expect(cardTitles('Todo')).toEqual(['CSV export, Todo'])
      expect(cardTitles('Done')).toEqual([])
    })

    test('New task creates a typed task through /api/mesh/tasks, never the board publish', async () => {
      const create = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/new-thing', version: 1, status: 'todo', seq: 9 })
      const publish = vi.spyOn(meshClient, 'publishMeshBoard')
      const reload = vi.fn(() => Promise.resolve())
      renderView(boardOf([], { tasks: typedTasks, typedTasks: true, reload }))
      fireEvent.click(screen.getByRole('button', { name: 'New task' }))
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(reload).toHaveBeenCalled())
      expect(create.mock.calls[0]?.[0]).toMatchObject({ action: 'create', key: 'task/new-thing', title: 'New thing' })
      expect(publish).not.toHaveBeenCalled()
    })
  })

  test('legacy mode never calls /api/mesh/tasks', async () => {
    const typed = vi.spyOn(meshClient, 'postMeshTask')
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(entry('user', 'task/new-thing', 'New thing', 10))
    const reload = vi.fn(() => Promise.resolve())
    renderView(boardOf(entries, { reload }))
    fireEvent.click(screen.getByRole('button', { name: 'New task' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'New thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(reload).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: 'Release, In progress' }))
    expect(within(screen.getByRole('dialog', { name: 'Task details' })).queryByRole('button', { name: /Release|Reopen|Assign/ })).toBeNull()
    expect(typed).not.toHaveBeenCalled()
  })
})
