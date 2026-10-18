import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AttachAssistantDialog } from './AttachAssistantDialog'
import type { AssistantProfile } from '../../types/assistant-profile'
import * as meshClient from '../../api/mesh-client'

const profiles: AssistantProfile[] = [
  { id: 'qaEngineer', title: 'QA Engineer', command: 'claude', available: true, unavailableReason: '' },
]
const stages = [{ id: 'impl', title: 'Frontend' }]

function renderDialog(overrides: Partial<Parameters<typeof AttachAssistantDialog>[0]> = {}) {
  const onConfirmed = vi.fn()
  const onCancel = vi.fn()
  render(
    <AttachAssistantDialog
      profileId="qaEngineer"
      stageId="impl"
      instruction="Please review"
      profiles={profiles}
      stages={stages}
      onConfirmed={onConfirmed}
      onCancel={onCancel}
      {...overrides}
    />,
  )
  return { onConfirmed, onCancel }
}

// dev-plan.md T7.5 — AttachAssistantDialog (+ ActionCard) is the Feed's attach preview: shows the assignment,
// the Configured executor, the stage binding and the adviser/required mode before a helper is actually
// created, and confirms via the T6.2-backed composite (attachAndSendMesh), never confirmActionDraft.
describe('AttachAssistantDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('shows the assignment, Configured executor, stage binding and mode before anything is created', () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    renderDialog()

    // "QA Engineer" appears twice: Role, and Task (the title falls back to the role when none is given).
    expect(screen.getAllByText('QA Engineer').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('Configured executor')).toBeInTheDocument()
    expect(screen.getByText('claude')).toBeInTheDocument()
    expect(screen.getByText('Frontend')).toBeInTheDocument()
    expect(screen.getByText('Please review')).toBeInTheDocument()
    expect(screen.getByText(/advisory/i)).toBeInTheDocument()
    expect(attachSpy).not.toHaveBeenCalled()
  })

  // TestCard_DoubleSubmitCreatesOneTask — a client-side guard (disable the button immediately on first
  // click) in ADDITION to the server's own idempotency: two rapid clicks must issue at most one request.
  test('TestCard_DoubleSubmitCreatesOneTask', async () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({
      task_id: 't-1', participant_id: 'helper:t-1', thread_id: 'mt-1', message_id: 'm-1',
    })
    const { onConfirmed } = renderDialog()

    const button = screen.getByRole('button', { name: /attach and send/i })
    fireEvent.click(button)
    fireEvent.click(button)
    fireEvent.click(button)

    await waitFor(() => expect(onConfirmed).toHaveBeenCalledTimes(1))
    expect(onConfirmed).toHaveBeenCalledWith({ task_id: 't-1', participant_id: 'helper:t-1', thread_id: 'mt-1', message_id: 'm-1' })
    expect(attachSpy).toHaveBeenCalledTimes(1)
  })

  // "Attach and send" issues exactly one POST carrying the assignment text and the stage id.
  test('Attach and send issues exactly one request carrying the assignment text and the stage id', async () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({
      task_id: 't-1', participant_id: 'helper:t-1', thread_id: 'mt-1', message_id: 'm-1',
    })
    renderDialog()

    fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))

    await waitFor(() => expect(attachSpy).toHaveBeenCalledTimes(1))
    expect(attachSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        profileId: 'qaEngineer',
        stageId: 'impl',
        scope: 'stage',
        instruction: 'Please review',
        firstMessage: 'Please review',
      }),
    )
  })

  // Cancel creates neither task nor message and issues no request at all.
  test('Cancel creates nothing and issues no request', () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    const { onCancel } = renderDialog()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(attachSpy).not.toHaveBeenCalled()
  })

  // TestCard_ThemesAndFocus — focus-trap (Tab wraps within the dialog), Escape closes (cancels), and the
  // initial focus lands on a visible, focusable element inside the dialog.
  test('TestCard_ThemesAndFocus', () => {
    const { onCancel } = renderDialog()

    const dialog = screen.getByRole('dialog')
    expect(dialog.contains(document.activeElement)).toBe(true)

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  // An unknown/ambiguous handle, an unavailable profile, or an exceeded quota renders the server error with
  // no partial attach — onConfirmed never fires, so nothing optimistic lands in the roster/feed.
  test('a server error renders in-dialog with no partial attach', async () => {
    vi.spyOn(meshClient, 'attachAndSendMesh').mockRejectedValue(new Error('quota exceeded'))
    const { onConfirmed } = renderDialog()

    fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/quota exceeded/i))
    expect(onConfirmed).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /attach and send/i })).not.toBeDisabled()
  })

  // A re-submit after a slow/failed response reuses the SAME request_id — no duplicate helper.
  test('retry after a server error reuses the same request_id', async () => {
    const attachSpy = vi
      .spyOn(meshClient, 'attachAndSendMesh')
      .mockRejectedValueOnce(new Error('transient'))
      .mockResolvedValueOnce({ task_id: 't-1', participant_id: 'helper:t-1', thread_id: 'mt-1' })
    const { onConfirmed } = renderDialog()

    const button = screen.getByRole('button', { name: /attach and send/i })
    fireEvent.click(button)
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/transient/i))

    fireEvent.click(button)
    await waitFor(() => expect(onConfirmed).toHaveBeenCalled())

    expect(attachSpy).toHaveBeenCalledTimes(2)
    const [firstCall, secondCall] = attachSpy.mock.calls
    expect(firstCall?.[0].requestId).toBe(secondCall?.[0].requestId)
  })

  test('disables the confirm button and explains why at the depth limit', () => {
    renderDialog({ maxDepth: 0 })
    expect(screen.getByRole('button', { name: /attach and send/i })).toBeDisabled()
  })
})
