import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AttachAssistantDialog } from './AttachAssistantDialog'
import type { AssistantProfile } from '../../types/assistant-profile'

const profiles: AssistantProfile[] = [
  { id: 'qaEngineer', title: 'QA Engineer', command: 'claude', available: true, unavailableReason: '' },
]
const stages = [{ id: 'impl', title: 'Frontend' }]

function renderDialog(overrides: Partial<Parameters<typeof AttachAssistantDialog>[0]> = {}) {
  const onConfirmed = vi.fn()
  const onCancel = vi.fn()
  render(
    <AttachAssistantDialog
      turnId="turn-1"
      index={0}
      profileId="qaEngineer"
      stageId="impl"
      title="Look at this"
      instruction="Please review"
      profiles={profiles}
      stages={stages}
      maxDepth={3}
      onConfirmed={onConfirmed}
      onCancel={onCancel}
      {...overrides}
    />,
  )
  return { onConfirmed, onCancel }
}

function okResponse(taskId: string): Response {
  return { ok: true, json: async () => ({ task_id: taskId }) } as Response
}

describe('AttachAssistantDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // TestCard_DoubleSubmitCreatesOneTask — a client-side guard (disable the button immediately on first
  // click) in ADDITION to the server's own idempotency: two rapid clicks must issue at most one request.
  test('TestCard_DoubleSubmitCreatesOneTask', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(okResponse('t-1'))
    const { onConfirmed } = renderDialog()

    const button = screen.getByRole('button', { name: /attach assistant/i })
    fireEvent.click(button)
    fireEvent.click(button)
    fireEvent.click(button)

    await waitFor(() => expect(onConfirmed).toHaveBeenCalledTimes(1))
    expect(onConfirmed).toHaveBeenCalledWith('t-1')
    expect(fetchSpy).toHaveBeenCalledTimes(1)
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

  test('shows an error and re-enables the button when the server rejects the draft', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'stale_revision' }),
    } as Response)
    renderDialog()

    fireEvent.click(screen.getByRole('button', { name: /attach assistant/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/attach failed/i))
    expect(screen.getByRole('button', { name: /attach assistant/i })).not.toBeDisabled()
  })

  test('disables the confirm button and explains why at the depth limit', () => {
    renderDialog({ maxDepth: 0 })
    expect(screen.getByRole('button', { name: /attach assistant/i })).toBeDisabled()
  })
})
