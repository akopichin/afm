import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { BoardPanel, type BoardPanelProps } from './BoardPanel'
import { useMeshBoard } from '../../hooks/use-mesh'
import { boardTaskCount } from '../../lib/board-tasks'

// BoardWithPoll — BoardPanel with the real board poll its parent (TeamView) owns.
function BoardWithPoll(props: Omit<BoardPanelProps, 'board'>) {
  const board = useMeshBoard()
  return <BoardPanel board={board} {...props} />
}
import * as meshClient from '../../api/mesh-client'
import type { MeshParticipant } from '../../api/mesh-client'

const participants: MeshParticipant[] = [
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
]

const stages = [{ id: 'api', title: 'API', status: 'running' }]

describe('BoardPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('empty state invites posting a task', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    await waitFor(() => expect(screen.getByText(/Nothing on the board yet/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Post task' })).toBeInTheDocument()
  })

  test('an agent fact shows its key, status, author name and version', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [{ author_id: 'run:t-1', key: 'qa:report', version: 2, status: 'ready', seq: 5, ts: '2026-10-18T00:00:00Z' }],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const fact = await screen.findByRole('article', { name: 'qa:report' })
    expect(within(fact).getByText('ready')).toBeInTheDocument()
    expect(within(fact).getByText('QA')).toBeInTheDocument()
    expect(within(fact).getByText('v2')).toBeInTheDocument()
  })

  test('posting a task publishes task/<slug> with the description', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue({
      author_id: 'user', key: 'task/csv-export', version: 1, status: 'Add GET /todos.csv', seq: 1, ts: '2026-10-18T00:00:00Z',
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Add GET /todos.csv' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalled())
    expect(publish.mock.calls[0]?.[0]).toMatchObject({ key: 'task/csv-export', status: 'CSV export — Add GET /todos.csv' })
  })

  test('a posted task shows on the board after the reload', async () => {
    const posted = { author_id: 'user', key: 'task/csv-export', version: 1, status: 'CSV export', seq: 1, ts: '2026-10-18T00:00:00Z' }
    const get = vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValueOnce({ entries: [] }).mockResolvedValue({ entries: [posted] })
    vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted)
    render(<BoardWithPoll participants={participants} stages={stages} />)
    await waitFor(() => expect(screen.getByText(/Nothing on the board yet/)).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    expect(await screen.findByRole('button', { name: 'CSV export, Todo' })).toBeInTheDocument()
    expect(get.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  test("agents' reports (<task key>/<alias>) stay nested in their task: the card shows the latest, details list them", async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { author_id: 'user', key: 'task/csv-export', version: 1, status: 'CSV export — Add GET /todos.csv', seq: 1, ts: '2026-10-18T00:00:00Z' },
        { author_id: 'stage:api', key: 'task/csv-export/api', version: 1, status: 'done: GET /todos.csv added', seq: 2, ts: '2026-10-18T00:01:00Z' },
      ],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const card = await screen.findByRole('button', { name: 'CSV export, Done' })
    expect(within(card).getAllByText('API').length).toBeGreaterThan(0)
    expect(within(card).getByText(/done: GET \/todos.csv added/)).toBeInTheDocument()
    expect(document.querySelectorAll('.board-task-card')).toHaveLength(1)
    expect(screen.queryByRole('button', { name: /task\/csv-export\/api/ })).toBeNull()
    expect(screen.queryByRole('article')).toBeNull()
    fireEvent.click(card)
    const reports = within(screen.getByRole('dialog', { name: 'Task details' })).getByRole('list', { name: 'Reports' })
    expect(within(reports).getByText('done: GET /todos.csv added')).toBeInTheDocument()
  })

  test('tasks sit together in one ungrouped grid and each takes the colour of its status', async () => {
    const at = { version: 1, ts: '2026-10-18T00:00:00Z' }
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { ...at, author_id: 'user', key: 'task/a', status: 'Alpha', seq: 1 },
        { ...at, author_id: 'user', key: 'task/b', status: 'Beta', seq: 2 },
        { ...at, author_id: 'stage:api', key: 'task/b/api', status: 'in progress: wiring', seq: 3 },
        { ...at, author_id: 'user', key: 'task/c', status: 'Gamma', seq: 4 },
        { ...at, author_id: 'stage:api', key: 'task/c/api', status: 'done: shipped', seq: 5 },
      ],
    })
    const { container } = render(<BoardWithPoll participants={participants} stages={stages} />)
    expect(await screen.findByRole('button', { name: 'Alpha, Todo' })).toHaveAttribute('data-status', 'todo')
    expect(screen.getByRole('button', { name: 'Beta, In progress' })).toHaveAttribute('data-status', 'in_progress')
    expect(screen.getByRole('button', { name: 'Gamma, Done' })).toHaveAttribute('data-status', 'done')
    const grids = container.querySelectorAll('.team-board-list')
    expect(grids).toHaveLength(1)
    expect(grids[0]!.querySelectorAll('.board-task-card')).toHaveLength(3)
    expect(container.querySelector('[aria-label$=" tasks"]')).toBeNull()
  })

  test('the legend names the three statuses with their task counts', async () => {
    const at = { version: 1, ts: '2026-10-18T00:00:00Z' }
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { ...at, author_id: 'user', key: 'task/a', status: 'Alpha', seq: 1 },
        { ...at, author_id: 'user', key: 'task/b', status: 'Beta', seq: 2 },
        { ...at, author_id: 'stage:api', key: 'task/b/api', status: 'in progress: wiring', seq: 3 },
      ],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const legend = await screen.findByRole('list', { name: 'Statuses' })
    expect(within(legend).getByText('Todo').closest('li')).toHaveTextContent('1')
    expect(within(legend).getByText('In progress').closest('li')).toHaveTextContent('1')
    expect(within(legend).getByText('Done').closest('li')).toHaveTextContent('0')
  })

  test('cards follow the board poll: a new report recolours the card', async () => {
    vi.useFakeTimers()
    try {
      const at = { version: 1, ts: '2026-10-18T00:00:00Z' }
      const task = { ...at, author_id: 'user', key: 'task/a', status: 'Alpha', seq: 1 }
      vi.spyOn(meshClient, 'getMeshBoard')
        .mockResolvedValueOnce({ entries: [task] })
        .mockResolvedValue({ entries: [task, { ...at, author_id: 'stage:api', key: 'task/a/api', status: 'done: shipped', seq: 2 }] })
      render(<BoardWithPoll participants={participants} stages={stages} />)
      await act(async () => {})
      expect(screen.getByRole('button', { name: 'Alpha, Todo' })).toHaveAttribute('data-status', 'todo')
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20000)
      })
      expect(screen.getByRole('button', { name: 'Alpha, Done' })).toHaveAttribute('data-status', 'done')
    } finally {
      vi.useRealTimers()
    }
  })

  test('facts keep their article next to the task cards, and facts are not cards', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { author_id: 'user', key: 'task/a', version: 1, status: 'Alpha', seq: 1, ts: '2026-10-18T00:00:00Z' },
        { author_id: 'run:t-1', key: 'qa:report', version: 3, status: 'ready', seq: 2, ts: '2026-10-18T00:00:00Z', artifact_refs: ['qa/out.txt'] },
      ],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const fact = await screen.findByRole('article', { name: 'qa:report' })
    expect(within(fact).getByText('v3')).toBeInTheDocument()
    expect(within(fact).getByText('qa/out.txt')).toBeInTheDocument()
    expect(screen.getAllByRole('article')).toHaveLength(1)
    expect(document.querySelectorAll('.board-task-card')).toHaveLength(1)
  })

  test('a card opens the task details and Close returns focus to the card', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [{ author_id: 'user', key: 'task/a', version: 1, status: 'Alpha — Do the thing', seq: 1, ts: '2026-10-18T00:00:00Z' }],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const card = await screen.findByRole('button', { name: 'Alpha, Todo' })
    card.focus()
    fireEvent.click(card)
    const dialog = screen.getByRole('dialog', { name: 'Task details' })
    expect(within(dialog).getByText('Do the thing')).toBeInTheDocument()
    expect(within(dialog).queryByRole('combobox')).toBeNull()
    fireEvent.click(within(dialog).getByRole('button', { name: /close/i }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(card).toHaveFocus()
  })

  test('an open dialog shows the report that arrives on the next poll', async () => {
    vi.useFakeTimers()
    try {
      const at = { version: 1, ts: '2026-10-18T00:00:00Z' }
      const task = { ...at, author_id: 'user', key: 'task/a', status: 'Alpha', seq: 1 }
      vi.spyOn(meshClient, 'getMeshBoard')
        .mockResolvedValueOnce({ entries: [task] })
        .mockResolvedValue({ entries: [task, { ...at, author_id: 'stage:api', key: 'task/a/api', status: 'done: shipped', seq: 2 }] })
      render(<BoardWithPoll participants={participants} stages={stages} />)
      await act(async () => {})
      fireEvent.click(screen.getByRole('button', { name: 'Alpha, Todo' }))
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20000)
      })
      const dialog = screen.getByRole('dialog', { name: 'Task details' })
      expect(within(dialog).getByText('done: shipped')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  test('working agents are listed under Notify and all start checked', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const notify = await screen.findByRole('group', { name: 'Notify' })
    expect(within(notify).getByRole('checkbox', { name: 'QA' })).toBeChecked()
    expect(within(notify).getByRole('checkbox', { name: 'API' })).toBeChecked()
  })

  test('posting notifies the checked agents only', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue({
      author_id: 'user', key: 'task/csv-export', version: 1, status: 'CSV export', seq: 1, ts: '2026-10-18T00:00:00Z',
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    fireEvent.click(screen.getByRole('checkbox', { name: 'QA' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalled())
    expect(publish.mock.calls[0]?.[0].notify).toEqual(['stage:api'])
  })

  test('unreachable run agents and agents of idle stages are not offered', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const offline = participants.map((p) => (p.kind === 'run_agent' ? { ...p, reachable: false } : p))
    render(<BoardWithPoll participants={offline} stages={[{ id: 'api', title: 'API', status: 'done' }]} />)
    await waitFor(() => expect(screen.getByText(/Nothing on the board yet/)).toBeInTheDocument())
    expect(screen.queryByRole('group', { name: 'Notify' })).toBeNull()
    expect(screen.queryByRole('checkbox')).toBeNull()
  })

  test('a retry of the same task after a lost response reuses its request id; an edited task gets a new one', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockRejectedValue(new Error('network'))
    render(<BoardWithPoll participants={participants} stages={stages} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalledTimes(2))
    expect(publish.mock.calls[1]?.[0].requestId).toBe(publish.mock.calls[0]?.[0].requestId)

    await waitFor(() => expect(screen.getByRole('button', { name: 'Post task' })).toBeEnabled())
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'only the API' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalledTimes(3))
    expect(publish.mock.calls[2]?.[0].requestId).not.toBe(publish.mock.calls[0]?.[0].requestId)
  })

  test('details typed while a task is being posted are kept after it succeeds', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    let resolvePublish!: (v: meshClient.MeshBoardEntry) => void
    vi.spyOn(meshClient, 'publishMeshBoard').mockReturnValue(new Promise((res) => (resolvePublish = res)))
    render(<BoardWithPoll participants={participants} stages={stages} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'next: PDF export' } })
    await act(async () => resolvePublish({ author_id: 'user', key: 'task/csv-export', version: 1, status: 'CSV export', seq: 1, ts: '2026-10-18T00:00:00Z' }))
    expect(screen.getByLabelText('Task')).toHaveValue('')
    expect(screen.getByLabelText('Details')).toHaveValue('next: PDF export')
  })
})

describe('boardTaskCount', () => {
  test('counts top-level task/ keys only, not facts or reports', () => {
    const at = { version: 1, status: 's', seq: 1, ts: '2026-10-18T00:00:00Z', author_id: 'user' }
    expect(
      boardTaskCount([
        { ...at, key: 'task/a' },
        { ...at, key: 'task/a/qa' },
        { ...at, key: 'api/ready' },
      ]),
    ).toBe(1)
  })

  test('more ticked agents than a task may notify blocks posting and says how many to untick', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const many = Array.from({ length: 18 }, (_, i) => ({ id: `run:a${i}`, kind: 'run_agent', scope: 'run', display_name: `Agent ${i}`, reachable: true, activity: 'idle' }))
    render(<BoardWithPoll participants={many} stages={[]} />)
    fireEvent.change(screen.getByPlaceholderText('e.g. CSV export'), { target: { value: 'Big task' } })
    expect(screen.getByText(/untick 2/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Post task' })).toBeDisabled()
  })
})
