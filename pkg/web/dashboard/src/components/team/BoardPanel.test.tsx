import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { BoardPanel, type BoardPanelProps } from './BoardPanel'
import { useMeshBoard } from '../../hooks/use-mesh'

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

  test("agents' reports (<task key>/<alias>) are listed under their task, by name", async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { author_id: 'user', key: 'task/csv-export', version: 1, status: 'CSV export — Add GET /todos.csv', seq: 1, ts: '2026-10-18T00:00:00Z' },
        { author_id: 'stage:api', key: 'task/csv-export/api', version: 1, status: 'done: GET /todos.csv added', seq: 2, ts: '2026-10-18T00:01:00Z' },
      ],
    })
    render(<BoardWithPoll participants={participants} stages={stages} />)
    const task = await screen.findByRole('article', { name: /csv-export/ })
    expect(within(task).getByText('CSV export — Add GET /todos.csv')).toBeInTheDocument()
    expect(within(task).getByText('API')).toBeInTheDocument()
    expect(within(task).getByText('done: GET /todos.csv added')).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: /task\/csv-export\/api/ })).toBeNull()
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
})
