import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { BoardTaskForm } from './BoardTaskForm'

const participants: MeshParticipant[] = [
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
  { id: 'stage:api', kind: 'stage_agent', scope: 'stage', alias: 'api', display_name: 'API', stage_id: 'api', reachable: true },
]
const stages = [{ id: 'api', title: 'API', status: 'running' }]

function posted(key: string, status: string): MeshBoardEntry {
  return { author_id: 'user', key, version: 1, status, seq: 1, ts: '2026-10-18T00:00:00Z' }
}

describe('BoardTaskForm', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('posting publishes task/<slug> with title and details, and reports the key', async () => {
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted('task/csv-export', 'x'))
    const onPosted = vi.fn()
    render(<BoardTaskForm participants={participants} stages={stages} onPosted={onPosted} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Add GET /todos.csv' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(onPosted).toHaveBeenCalledWith('task/csv-export', false))
    expect(publish.mock.calls[0]?.[0]).toMatchObject({ key: 'task/csv-export', status: 'CSV export — Add GET /todos.csv' })
    expect(screen.getByLabelText('Task')).toHaveValue('')
    expect(screen.getByLabelText('Details')).toHaveValue('')
  })

  test('has no status control: the operator only names the task and picks who to notify', () => {
    render(<BoardTaskForm participants={participants} stages={stages} onPosted={() => {}} />)
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByText(/in progress|done/i)).toBeNull()
  })

  test('notifies the checked agents only; working agents all start checked', async () => {
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted('task/csv-export', 'CSV export'))
    render(<BoardTaskForm participants={participants} stages={stages} onPosted={() => {}} />)
    const notify = screen.getByRole('group', { name: 'Notify' })
    expect(within(notify).getByRole('checkbox', { name: 'API' })).toBeChecked()
    fireEvent.click(within(notify).getByRole('checkbox', { name: 'QA' }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalled())
    expect(publish.mock.calls[0]?.[0].notify).toEqual(['stage:api'])
  })

  test('a failed post shows the error, keeps the draft and a retry reuses the request id', async () => {
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockRejectedValue(new Error('network'))
    const onPosted = vi.fn()
    render(<BoardTaskForm participants={participants} stages={stages} onPosted={onPosted} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    expect(screen.getByLabelText('Task')).toHaveValue('CSV export')
    expect(onPosted).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(publish).toHaveBeenCalledTimes(2))
    expect(publish.mock.calls[1]?.[0].requestId).toBe(publish.mock.calls[0]?.[0].requestId)
  })

  test('text typed while a task is being posted is kept after it succeeds', async () => {
    let resolvePublish!: (v: MeshBoardEntry) => void
    vi.spyOn(meshClient, 'publishMeshBoard').mockReturnValue(new Promise((res) => (resolvePublish = res)))
    render(<BoardTaskForm participants={participants} stages={stages} onPosted={() => {}} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'next: PDF export' } })
    await act(async () => resolvePublish(posted('task/csv-export', 'CSV export')))
    expect(screen.getByLabelText('Task')).toHaveValue('')
    expect(screen.getByLabelText('Details')).toHaveValue('next: PDF export')
  })

  test('more ticked agents than a task may notify blocks posting and says how many to untick', () => {
    const many = Array.from({ length: 18 }, (_, i) => ({ id: `run:a${i}`, kind: 'run_agent', scope: 'run', display_name: `Agent ${i}`, reachable: true }))
    render(<BoardTaskForm participants={many} stages={[]} onPosted={() => {}} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'Big task' } })
    expect(screen.getByText(/untick 2/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Post task' })).toBeDisabled()
  })
})
