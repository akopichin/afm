import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AttachAssistantDialog } from './AttachAssistantDialog'
import type { AssistantProfile } from '../../types/assistant-profile'
import * as meshClient from '../../api/mesh-client'

const profiles: AssistantProfile[] = [
  { id: 'qaEngineer', title: 'QA Engineer', command: 'claude', commandName: 'Claude Code', available: true, unavailableReason: '' },
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
    expect(screen.getByText('Claude Code')).toHaveAttribute('title', 'Claude Code\nCommand: claude')
    expect(screen.getByText('Frontend')).toBeInTheDocument()
    expect(screen.getByText('Please review')).toBeInTheDocument()
    expect(screen.getByText(/advisory/i)).toBeInTheDocument()
    expect(attachSpy).not.toHaveBeenCalled()
  })

  test('names where the executor comes from, as resolved in the stage', () => {
    renderDialog({ profiles: [{ ...profiles[0]!, command: 'codex-as-claude', commandName: 'Codex Reviewer', commandSource: 'stage' }] })
    expect(screen.getByText('Codex Reviewer (stage)')).toBeInTheDocument()
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

  test('the confirmation dialog is named Attach helper', () => {
    renderDialog()
    expect(screen.getByRole('dialog', { name: 'Attach helper' })).toBeInTheDocument()
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

  // The composer owns the request's lifecycle, so it hands the dialog its own sender; the dialog still owns the
  // request id and the error.
  describe('with a send prop from the composer', () => {
    const result = { task_id: 't-1', participant_id: 'helper:t-1', thread_id: 'mt-1', message_id: 'm-1' }

    test('the given send is used instead of attachAndSendMesh and success confirms', async () => {
      const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
      const send = vi.fn().mockResolvedValue(result)
      const { onConfirmed } = renderDialog({ send })

      fireEvent.click(screen.getByRole('button', { name: /attach and send/i }))

      await waitFor(() => expect(onConfirmed).toHaveBeenCalledWith(result))
      expect(send).toHaveBeenCalledTimes(1)
      expect(send).toHaveBeenCalledWith(expect.objectContaining({ profileId: 'qaEngineer', stageId: 'impl', scope: 'stage', instruction: 'Please review', firstMessage: 'Please review' }))
      expect(attachSpy).not.toHaveBeenCalled()
    })

    test('a refusal shows the error, allows a retry, and the retry carries the same request_id', async () => {
      const send = vi.fn().mockRejectedValueOnce(new Error('locked')).mockResolvedValueOnce(result)
      const { onConfirmed } = renderDialog({ send })

      const button = screen.getByRole('button', { name: /attach and send/i })
      fireEvent.click(button)
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/locked/i))
      expect(onConfirmed).not.toHaveBeenCalled()
      expect(button).not.toBeDisabled()

      fireEvent.click(button)
      await waitFor(() => expect(onConfirmed).toHaveBeenCalledWith(result))
      expect(send).toHaveBeenCalledTimes(2)
      expect(send.mock.calls[0]?.[0].requestId).toBe(send.mock.calls[1]?.[0].requestId)
    })
  })

  test('disables the confirm button and explains why at the depth limit', () => {
    renderDialog({ maxDepth: 0 })
    expect(screen.getByRole('button', { name: /attach and send/i })).toBeDisabled()
  })

  // Profiles are polled, so an open preview must follow the role: a stale enabled button would attach to a
  // helper the server now refuses, and the reason would never be shown.
  describe('profile availability while the preview is open', () => {
    function renderRerenderable(maxDepth?: number) {
      const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
      const props = {
        profileId: 'qaEngineer',
        stageId: 'impl',
        instruction: 'Please review',
        maxDepth,
        stages,
        onConfirmed: vi.fn(),
        onCancel: vi.fn(),
      }
      const view = render(<AttachAssistantDialog {...props} profiles={profiles} />)
      return { attachSpy, props, rerenderWith: (next: AssistantProfile[]) => view.rerender(<AttachAssistantDialog {...props} profiles={next} />) }
    }

    test('an available role becoming unavailable disables Attach and shows the reason', () => {
      const { attachSpy, rerenderWith } = renderRerenderable()
      expect(screen.getByRole('button', { name: /attach and send/i })).toBeEnabled()

      rerenderWith([{ ...profiles[0]!, available: false, unavailableReason: 'claude is not on PATH' }])

      const button = screen.getByRole('button', { name: /attach and send/i })
      expect(button).toBeDisabled()
      expect(screen.getByRole('alert')).toHaveTextContent('QA Engineer: claude is not on PATH')
      fireEvent.click(button)
      expect(attachSpy).not.toHaveBeenCalled()
      // The assignment stays visible so the draft is not lost.
      expect(screen.getByText('Please review')).toBeInTheDocument()
    })

    test('a role removed from the list disables Attach and says so', () => {
      const { attachSpy, rerenderWith } = renderRerenderable()

      rerenderWith([])

      const button = screen.getByRole('button', { name: /attach and send/i })
      expect(button).toBeDisabled()
      expect(screen.getByRole('alert')).toHaveTextContent('qaEngineer is no longer in the profile list.')
      fireEvent.click(button)
      expect(attachSpy).not.toHaveBeenCalled()
      expect(screen.getByText('Please review')).toBeInTheDocument()
    })

    // Every blocking reason is shown on its own: the depth warning must not hide why the role is unusable.
    test('at the depth limit, an unavailable role still shows both the depth warning and the reason', () => {
      const { attachSpy, rerenderWith } = renderRerenderable(0)
      expect(screen.getAllByRole('alert')).toHaveLength(1)

      rerenderWith([{ ...profiles[0]!, available: false, unavailableReason: 'claude is not on PATH' }])

      const button = screen.getByRole('button', { name: /attach and send/i })
      expect(button).toBeDisabled()
      const alerts = screen.getAllByRole('alert')
      expect(alerts).toHaveLength(2)
      expect(alerts[0]).toHaveTextContent('delegation depth limit')
      expect(alerts[1]).toHaveTextContent('QA Engineer: claude is not on PATH')
      fireEvent.click(button)
      expect(attachSpy).not.toHaveBeenCalled()
    })

    test('at the depth limit, a removed role still shows both the depth warning and the removal', () => {
      const { attachSpy, rerenderWith } = renderRerenderable(0)

      rerenderWith([])

      const button = screen.getByRole('button', { name: /attach and send/i })
      expect(button).toBeDisabled()
      const alerts = screen.getAllByRole('alert')
      expect(alerts).toHaveLength(2)
      expect(alerts[0]).toHaveTextContent('delegation depth limit')
      expect(alerts[1]).toHaveTextContent('qaEngineer is no longer in the profile list.')
      fireEvent.click(button)
      expect(attachSpy).not.toHaveBeenCalled()
    })

    test('the role coming back enables Attach again', () => {
      const { rerenderWith } = renderRerenderable()
      rerenderWith([])
      rerenderWith(profiles)

      expect(screen.getByRole('button', { name: /attach and send/i })).toBeEnabled()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    })
  })
})
