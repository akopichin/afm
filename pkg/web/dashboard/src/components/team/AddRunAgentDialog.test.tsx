import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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

  test('a hidden dialog ignores Escape meant for the view that is shown, and listens again once visible', () => {
    const onAttached = vi.fn()
    const onCancel = vi.fn()
    const view = render(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} active={false} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).not.toHaveBeenCalled()
    view.rerender(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} active />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  test('a dialog shown again takes the keyboard focus back', () => {
    const onAttached = vi.fn()
    const onCancel = vi.fn()
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    const view = render(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} active />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    view.rerender(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} active={false} />)
    act(() => outside.focus())
    view.rerender(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={onCancel} active />)
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    outside.remove()
  })

  test('Cancel creates nothing', () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    const { onCancel } = renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
    expect(attachSpy).not.toHaveBeenCalled()
  })

  test("hands over the server's roster entry of the new agent, alias included", async () => {
    const participant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true }
    const result = { task_id: 't-1', participant_id: 'run:t-1', thread_id: 'mt-1', participant }
    vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue(result)
    const { onAttached } = renderDialog()
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(onAttached).toHaveBeenCalledWith(result, participant))
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
    // Without the server's roster entry the new agent is shown under its title until the roster poll has it.
    await waitFor(() =>
      expect(onAttached).toHaveBeenCalledWith(
        { task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1' },
        { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true },
      ),
    )

    expect(attachSpy).toHaveBeenCalledTimes(2)
    const [firstCall, secondCall] = attachSpy.mock.calls
    expect(firstCall?.[0]).toMatchObject({ profileId: 'qa', scope: 'run', instruction: 'review everything' })
    expect(firstCall?.[0].stageId).toBeUndefined()
    expect(secondCall?.[0].requestId).toBe(firstCall?.[0].requestId)
  })

  test('an optional first message is sent so the agent starts right away', async () => {
    const attachSpy = vi
      .spyOn(meshClient, 'attachAndSendMesh')
      .mockResolvedValue({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1', message_id: 'm1' })
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review everything' } })
    fireEvent.change(screen.getByLabelText('First message (optional)'), { target: { value: '  start with the API  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(attachSpy).toHaveBeenCalled())
    expect(attachSpy.mock.calls[0]?.[0]).toMatchObject({ firstMessage: 'start with the API' })
  })

  test('without a first message none is sent', async () => {
    const attachSpy = vi
      .spyOn(meshClient, 'attachAndSendMesh')
      .mockResolvedValue({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1' })
    renderDialog()
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review everything' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(attachSpy).toHaveBeenCalled())
    expect(attachSpy.mock.calls[0]?.[0]).not.toHaveProperty('firstMessage')
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

  test('editing the form after a failure makes the retry a new request with a new request_id', async () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockRejectedValue(new Error('conflict'))
    renderDialog()
    const instruction = screen.getByPlaceholderText('What should this run agent do?')
    fireEvent.change(instruction, { target: { value: 'review everything' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('conflict'))

    fireEvent.change(instruction, { target: { value: 'review the API only' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(2))
    expect(attachSpy.mock.calls[1]?.[0].requestId).not.toBe(attachSpy.mock.calls[0]?.[0].requestId)
  })

  test('a response that arrives after the dialog was closed is ignored', async () => {
    let resolveAttach!: (v: meshClient.MeshAttachAndSendResult) => void
    vi.spyOn(meshClient, 'attachAndSendMesh').mockReturnValue(new Promise((res) => (resolveAttach = res)))
    const onAttached = vi.fn()
    const { unmount } = render(<AddRunAgentDialog profiles={profiles} onAttached={onAttached} onCancel={vi.fn()} />)
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review everything' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    unmount()
    await act(async () => resolveAttach({ task_id: 't1', participant_id: 'run:qa', thread_id: 'mt-1' }))
    expect(onAttached).not.toHaveBeenCalled()
  })
})
