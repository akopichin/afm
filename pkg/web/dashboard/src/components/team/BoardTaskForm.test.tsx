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

  describe('task key', () => {
    async function keyPostedFor(title: string): Promise<string> {
      const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted('task/x', 'x'))
      const onPosted = vi.fn()
      render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={onPosted} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: title } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(onPosted).toHaveBeenCalled())
      return publish.mock.calls[0]?.[0].key ?? ''
    }

    test('a long Cyrillic title is cut by code point so the key stays within 200 bytes', async () => {
      const key = await keyPostedFor('Экспорт данных в формат таблицы '.repeat(10))
      expect(new TextEncoder().encode(key).length).toBeLessThanOrEqual(200)
      expect(key).toMatch(/^task\/[\p{L}\p{N}._-]+$/u)
      expect(key.endsWith('-')).toBe(false)
      expect(key.startsWith('task/экспорт-данных-в-формат')).toBe(true)
    })

    test('punctuation and emoji become separators; the key only has letters, digits and . _ -', async () => {
      const key = await keyPostedFor('Fix: v1.2_beta 🚀 (urgent)!')
      expect(key).toBe('task/fix-v1.2_beta-urgent')
    })

    test('a title of only symbols falls back to task/untitled', async () => {
      expect(await keyPostedFor('🚀🚀')).toBe('task/untitled')
    })
  })

  test('posting publishes task/<slug> with title and details, and reports that nothing is left', async () => {
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted('task/csv-export', 'x'))
    const onPosted = vi.fn()
    render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={onPosted} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Add GET /todos.csv' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    await waitFor(() => expect(onPosted).toHaveBeenCalledWith(false))
    expect(publish.mock.calls[0]?.[0]).toMatchObject({ key: 'task/csv-export', status: 'CSV export — Add GET /todos.csv' })
    expect(screen.getByLabelText('Task')).toHaveValue('')
    expect(screen.getByLabelText('Details')).toHaveValue('')
  })

  test('has no status control: the operator only names the task and picks who to notify', () => {
    render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={() => {}} />)
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByText(/in progress|done/i)).toBeNull()
  })

  test('notifies the checked agents only; working agents all start checked', async () => {
    const publish = vi.spyOn(meshClient, 'publishMeshBoard').mockResolvedValue(posted('task/csv-export', 'CSV export'))
    render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={() => {}} />)
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
    render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={onPosted} />)
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
    render(<BoardTaskForm participants={participants} stages={stages} mode="legacy" onPosted={() => {}} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'next: PDF export' } })
    await act(async () => resolvePublish(posted('task/csv-export', 'CSV export')))
    expect(screen.getByLabelText('Task')).toHaveValue('')
    expect(screen.getByLabelText('Details')).toHaveValue('next: PDF export')
  })

  test('more ticked agents than a task may notify blocks posting and says how many to untick', () => {
    const many = Array.from({ length: 18 }, (_, i) => ({ id: `run:a${i}`, kind: 'run_agent', scope: 'run', display_name: `Agent ${i}`, reachable: true }))
    render(<BoardTaskForm participants={many} stages={[]} mode="legacy" onPosted={() => {}} />)
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'Big task' } })
    expect(screen.getByText(/untick 2/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Post task' })).toBeDisabled()
  })

  describe('typed tasks', () => {
    test('has an optional Assignee select instead of notify checkboxes', () => {
      render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={() => {}} />)
      expect(screen.queryByRole('group', { name: 'Notify' })).toBeNull()
      expect(screen.queryByRole('checkbox')).toBeNull()
      const assignee = screen.getByRole('combobox', { name: 'Assignee' })
      expect(assignee).toHaveValue('')
      expect(within(assignee).getAllByRole('option').map((o) => o.textContent)).toEqual(['Nobody yet', 'QA', 'API'])
    })

    test('the Assignee select never offers an Assistant (side agent) or a helper', () => {
      const withAssistant: MeshParticipant[] = [
        ...participants,
        { id: 'side:assistant', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'Assistant', reachable: true },
        { id: 'helper:api:rev', kind: 'helper', scope: 'stage', alias: 'rev', display_name: 'Reviewer', stage_id: 'api', reachable: true },
      ]
      render(<BoardTaskForm participants={withAssistant} stages={stages} mode="typed" onPosted={() => {}} />)
      const assignee = screen.getByRole('combobox', { name: 'Assignee' })
      expect(within(assignee).getAllByRole('option').map((o) => o.textContent)).toEqual(['Nobody yet', 'QA', 'API'])
    })

    test('creates the task with title, description and the picked assignee', async () => {
      const create = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv-export', version: 1, status: 'todo', seq: 3 })
      const publish = vi.spyOn(meshClient, 'publishMeshBoard')
      const onPosted = vi.fn()
      render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={onPosted} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
      fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Add GET /todos.csv' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), { target: { value: 'stage:api' } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(onPosted).toHaveBeenCalledWith(false))
      expect(create.mock.calls[0]?.[0]).toMatchObject({ action: 'create', key: 'task/csv-export', title: 'CSV export', description: 'Add GET /todos.csv', to: 'stage:api' })
      expect(publish).not.toHaveBeenCalled()
      expect(screen.getByRole('combobox', { name: 'Assignee' })).toHaveValue('')
    })

    test('without an assignee the create sends no "to"', async () => {
      const create = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv-export', version: 1, status: 'todo', seq: 3 })
      render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={() => {}} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(create).toHaveBeenCalled())
      expect(create.mock.calls[0]?.[0].to).toBeUndefined()
      expect(create.mock.calls[0]?.[0].description).toBeUndefined()
    })

    test('an assignee that stops being a candidate is neither shown nor sent', async () => {
      const create = vi.spyOn(meshClient, 'postMeshTask').mockResolvedValue({ key: 'task/csv-export', version: 1, status: 'todo', seq: 3 })
      const { rerender } = render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={() => {}} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), { target: { value: 'stage:api' } })
      rerender(<BoardTaskForm participants={participants} stages={[{ id: 'api', title: 'API', status: 'paused' }]} mode="typed" onPosted={() => {}} />)
      expect(screen.getByRole('combobox', { name: 'Assignee' })).toHaveValue('')
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(create).toHaveBeenCalled())
      expect(create.mock.calls[0]?.[0].to).toBeUndefined()
    })

    test('a create whose response was lost is resent unchanged even after its assignee left the candidates', async () => {
      const create = vi
        .spyOn(meshClient, 'postMeshTask')
        .mockRejectedValueOnce(new TypeError('network down'))
        .mockResolvedValue({ key: 'task/csv-export', version: 1, status: 'todo', seq: 3 })
      const onPosted = vi.fn()
      const { rerender } = render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={onPosted} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), { target: { value: 'stage:api' } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network down'))
      rerender(<BoardTaskForm participants={participants} stages={[{ id: 'api', title: 'API', status: 'paused' }]} mode="typed" onPosted={onPosted} />)
      expect(screen.getByRole('combobox', { name: 'Assignee' })).toHaveValue('')
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(onPosted).toHaveBeenCalledWith(false))
      expect(create).toHaveBeenCalledTimes(2)
      expect(create.mock.calls[1]?.[0]).toEqual(create.mock.calls[0]?.[0])
      expect(create.mock.calls[1]?.[0].to).toBe('stage:api')
      expect(screen.getByLabelText('Task')).toHaveValue('')
    })

    describe('a retried create removes only the text that was sent', () => {
      const lost = new TypeError('network down')
      const saved = { key: 'task/csv-export', version: 1, status: 'todo', seq: 3 } as const

      function setup() {
        const create = vi.spyOn(meshClient, 'postMeshTask').mockRejectedValueOnce(lost).mockResolvedValue(saved)
        const onPosted = vi.fn()
        render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={onPosted} />)
        fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
        fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Add GET /todos.csv' } })
        return { create, onPosted }
      }
      const post = (): boolean => fireEvent.click(screen.getByRole('button', { name: 'Post task' }))

      test('Details edited after the lost response stay in the form', async () => {
        const { create, onPosted } = setup()
        post()
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network down'))
        fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Something else entirely' } })
        post()
        await waitFor(() => expect(onPosted).toHaveBeenCalledWith(true))
        expect(create.mock.calls[1]?.[0]).toEqual(create.mock.calls[0]?.[0])
        expect(create.mock.calls[1]?.[0].description).toBe('Add GET /todos.csv')
        expect(screen.getByLabelText('Details')).toHaveValue('Something else entirely')
        expect(screen.getByLabelText('Task')).toHaveValue('')
      })

      test('a title edited to the same slug stays in the form', async () => {
        const { create, onPosted } = setup()
        post()
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network down'))
        fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV Export' } })
        post()
        await waitFor(() => expect(onPosted).toHaveBeenCalledWith(true))
        expect(create.mock.calls[1]?.[0]).toEqual(create.mock.calls[0]?.[0])
        expect(create.mock.calls[1]?.[0].title).toBe('CSV export')
        expect(screen.getByLabelText('Task')).toHaveValue('CSV Export')
        expect(screen.getByLabelText('Details')).toHaveValue('')
      })

      test('Details edited while the first request was in flight stay in the form', async () => {
        const { create, onPosted } = setup()
        let fail: (err: unknown) => void = () => {}
        create.mockReset()
        create
          .mockImplementationOnce(() => new Promise((_, reject) => { fail = reject }))
          .mockResolvedValue(saved)
        post()
        fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'Something else entirely' } })
        await act(async () => fail(lost))
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network down'))
        post()
        await waitFor(() => expect(onPosted).toHaveBeenCalledWith(true))
        expect(create.mock.calls[1]?.[0].description).toBe('Add GET /todos.csv')
        expect(screen.getByLabelText('Details')).toHaveValue('Something else entirely')
      })

      test('without edits the whole draft is cleared', async () => {
        const { onPosted } = setup()
        post()
        await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network down'))
        post()
        await waitFor(() => expect(onPosted).toHaveBeenCalledWith(false))
        expect(screen.getByLabelText('Task')).toHaveValue('')
        expect(screen.getByLabelText('Details')).toHaveValue('')
      })
    })

    test('a failed create keeps the draft and shows the error', async () => {
      vi.spyOn(meshClient, 'postMeshTask').mockRejectedValue(new Error('agent unreachable'))
      render(<BoardTaskForm participants={participants} stages={stages} mode="typed" onPosted={() => {}} />)
      fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
      fireEvent.click(screen.getByRole('button', { name: 'Post task' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('agent unreachable'))
      expect(screen.getByLabelText('Task')).toHaveValue('CSV export')
    })
  })
})
