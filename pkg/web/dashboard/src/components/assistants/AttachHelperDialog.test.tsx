import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AttachHelperDialog } from './AttachHelperDialog'
import type { AssistantProfile } from '../../types/assistant-profile'
import * as runClient from '../../api/run-client'

const profiles: AssistantProfile[] = [
  { id: 'reviewer', title: 'Code reviewer', command: 'claude', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'claude', available: false, unavailableReason: 'no policy' },
]

function renderDialog(overrides: Partial<Parameters<typeof AttachHelperDialog>[0]> = {}) {
  const onAttached = vi.fn()
  const onCancel = vi.fn()
  render(
    <AttachHelperDialog
      stageId="impl"
      stageTitle="Implement feature"
      profiles={profiles}
      onAttached={onAttached}
      onCancel={onCancel}
      {...overrides}
    />,
  )
  return { onAttached, onCancel }
}

describe('AttachHelperDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('lists only available profiles and submits an attach', async () => {
    const attachSpy = vi.spyOn(runClient, 'attachAssistant').mockResolvedValue({ taskId: 't-9' })
    const { onAttached } = renderDialog()

    // Unavailable profile is not offered.
    expect(screen.queryByText('Ghost')).toBeNull()
    expect(screen.getByText('Code reviewer')).toBeTruthy()

    // Instruction is required to enable submit.
    const submit = screen.getByRole('button', { name: 'Attach helper' })
    expect((submit as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByPlaceholderText('What should this helper do?'), {
      target: { value: 'Audit the retry path' },
    })
    expect((submit as HTMLButtonElement).disabled).toBe(false)

    fireEvent.click(submit)
    await waitFor(() => expect(onAttached).toHaveBeenCalledWith('t-9'))
    expect(attachSpy).toHaveBeenCalledWith(
      'impl',
      expect.objectContaining({ profileId: 'reviewer', instruction: 'Audit the retry path', mode: 'advisory', trigger: 'one_shot' }),
    )
  })

  test('surfaces a server error without closing', async () => {
    vi.spyOn(runClient, 'attachAssistant').mockRejectedValue(new Error('invalid_status'))
    const { onAttached } = renderDialog()
    fireEvent.change(screen.getByPlaceholderText('What should this helper do?'), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Attach helper' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('invalid_status'))
    expect(onAttached).not.toHaveBeenCalled()
  })

  test('Escape cancels', () => {
    const { onCancel } = renderDialog()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalled()
  })

  test('shows an empty state when no profile is available', () => {
    renderDialog({ profiles: [profiles[1]!] })
    expect(screen.getByText('No helper profiles are available for this run.')).toBeTruthy()
  })
})
