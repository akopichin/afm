import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { BoardPanel } from './BoardPanel'
import * as meshClient from '../../api/mesh-client'
import type { MeshParticipant } from '../../api/mesh-client'

const participants: MeshParticipant[] = [
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
]

describe('BoardPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('empty state invites posting a task', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    render(<BoardPanel participants={participants} />)
    await waitFor(() => expect(screen.getByText(/Nothing on the board yet/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Post task' })).toBeInTheDocument()
  })

  test('an agent fact shows its key, status, author name and version', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [{ author_id: 'run:t-1', key: 'qa:report', version: 2, status: 'ready', seq: 5, ts: '2026-10-18T00:00:00Z' }],
    })
    render(<BoardPanel participants={participants} />)
    await waitFor(() => expect(screen.getByText('qa:report')).toBeInTheDocument())
    expect(screen.getByText('ready')).toBeInTheDocument()
    expect(screen.getByText('QA')).toBeInTheDocument()
    expect(screen.getByText('v2')).toBeInTheDocument()
  })

  test('posting a task publishes task/<slug> with the description', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue({
      author_id: 'user', key: 'task/csv-export', version: 1, status: 'Add GET /todos.csv', seq: 1, ts: '2026-10-18T00:00:00Z',
    })
    render(<BoardPanel participants={participants} />)
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
    render(<BoardPanel participants={participants} />)
    const task = await screen.findByRole('article', { name: /csv-export/ })
    expect(within(task).getByText('CSV export — Add GET /todos.csv')).toBeInTheDocument()
    expect(within(task).getByText('API')).toBeInTheDocument()
    expect(within(task).getByText('done: GET /todos.csv added')).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: /task\/csv-export\/api/ })).toBeNull()
  })
})
