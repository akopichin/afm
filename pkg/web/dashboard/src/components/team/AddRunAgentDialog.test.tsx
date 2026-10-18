import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AddRunAgentDialog } from './AddRunAgentDialog'
import type { AssistantProfile } from '../../types/assistant-profile'
import * as meshClient from '../../api/mesh-client'

const profiles: AssistantProfile[] = [
  { id: 'qa', title: 'QA reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'codex', available: false, unavailableReason: 'no policy' },
]

function renderDialog(overrides: Partial<Parameters<typeof AddRunAgentDialog>[0]> = {}) {
  const onAttached = vi.fn()
  const onCancel = vi.fn()
  render(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} {...overrides} />)
  return { onAttached, onCancel }
}

describe('AddRunAgentDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('shows a read-only "Configured executor" with no model selector', () => {
    renderDialog()
    expect(screen.getByText('Configured executor')).toBeInTheDocument()
    expect(screen.getByText('claude')).toBeInTheDocument()
    // No model <select> anywhere in the dialog — only the Role select.
    expect(screen.getAllByRole('combobox')).toHaveLength(1)
    expect(screen.queryByLabelText(/model/i)).toBeNull()
  })

  test('Cancel creates nothing', () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    const { onCancel } = renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
    expect(attachSpy).not.toHaveBeenCalled()
  })

  test('submits scope "run" with no stage id, sharing one request_id across retries', async () => {
    const attachSpy = vi
      .spyOn(meshClient, 'attachAndSendMesh')
      .mockRejectedValueOnce(new Error('transient'))
      .mockResolvedValueOnce({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1' })
    const { onAttached } = renderDialog()

    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review everything' } })

    const submit = screen.getByRole('button', { name: 'Add run agent' })
    fireEvent.click(submit)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('transient'))

    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(onAttached).toHaveBeenCalledWith({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1' }))

    expect(attachSpy).toHaveBeenCalledTimes(2)
    const [firstCall, secondCall] = attachSpy.mock.calls
    expect(firstCall?.[0]).toMatchObject({ profileId: 'qa', scope: 'run', instruction: 'review everything' })
    expect(firstCall?.[0].stageId).toBeUndefined()
    expect(secondCall?.[0].requestId).toBe(firstCall?.[0].requestId)
  })

  test('only available profiles are offered', () => {
    renderDialog()
    expect(screen.queryByText('Ghost')).toBeNull()
    expect(screen.getByText('QA reviewer')).toBeInTheDocument()
  })

  test('empty state when no profile is available', () => {
    renderDialog({ profiles: [profiles[1]!] })
    expect(screen.getByText('No agent profiles are available for this run.')).toBeInTheDocument()
  })
})
