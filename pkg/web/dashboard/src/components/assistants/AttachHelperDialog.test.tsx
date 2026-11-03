import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { AttachHelperDialog } from './AttachHelperDialog'
import { toAssistantProfile, type AssistantProfile } from '../../types/assistant-profile'
import * as runClient from '../../api/run-client'
import { resetUnknownCommandLabels } from '../../lib/executor-label'

const profiles: AssistantProfile[] = [
  { id: 'reviewer', title: 'Code reviewer', command: 'claude', commandName: 'Claude Code', commandSource: 'stage', available: true, unavailableReason: '' },
  { id: 'qa', title: 'QA', command: 'codex-as-claude', commandName: 'Codex Reviewer', commandSource: 'profile', available: true, unavailableReason: '' },
  { id: 'ghost', title: 'Ghost', command: 'claude', commandSource: 'client', available: false, unavailableReason: 'no policy' },
]
const ghost = profiles[2]!

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
  beforeEach(() => resetUnknownCommandLabels())

  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('an untitled profile is offered under its id', () => {
    const untitled = toAssistantProfile({ id: 'reviewer', title: '', command: 'claude', command_source: 'client', available: true })
    renderDialog({ profiles: [untitled] })
    expect(screen.getByRole('option', { name: 'reviewer' })).toBeTruthy()
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
    renderDialog({ profiles: [] })
    expect(screen.getByText('No helper profiles are available for this stage.')).toBeTruthy()
  })

  test('explains an empty list by the reason each profile is unavailable', () => {
    renderDialog({ profiles: [ghost] })
    expect(screen.getByText(/No helper profiles are available for this stage\./).textContent).toContain('Ghost: no policy')
    expect((screen.getByRole('button', { name: 'Attach helper' }) as HTMLButtonElement).disabled).toBe(true)
  })

  test('shows the executor and its source for the selected role and follows the selection', () => {
    renderDialog()
    expect(screen.getByText('Configured executor')).toBeTruthy()
    expect(screen.getByText('Claude Code (stage)')).toBeTruthy()

    fireEvent.change(screen.getByRole('combobox', { name: 'Role' }), { target: { value: 'qa' } })
    expect(screen.getByText('Codex Reviewer (profile)')).toHaveAttribute('title', 'Codex Reviewer\nCommand: codex-as-claude')
    expect(screen.queryByText('Claude Code (stage)')).toBeNull()
  })

  test('the executor tooltip loses the raw command when the skin hides it; a nameless command is numbered', () => {
    document.documentElement.style.setProperty('--afm-raw-command-tooltip', 'none')
    try {
      renderDialog({ profiles: [{ ...profiles[0]!, command: './x/agent.sh', commandName: undefined }] })
      expect(screen.getByText('Unknown-1 (stage)')).toHaveAttribute('title', 'Unknown-1')
    } finally {
      document.documentElement.style.removeProperty('--afm-raw-command-tooltip')
    }
  })

  test('attaches the role of the executor line when profiles arrive after the dialog opened', async () => {
    const attachSpy = vi.spyOn(runClient, 'attachAssistant').mockResolvedValue({ taskId: 't-1' })
    const props = { stageId: 'impl', stageTitle: 'Implement', onAttached: vi.fn(), onCancel: vi.fn() }
    const view = render(<AttachHelperDialog {...props} profiles={[]} />)
    view.rerender(<AttachHelperDialog {...props} profiles={profiles} />)

    expect(screen.getByText('Claude Code (stage)')).toBeTruthy()
    fireEvent.change(screen.getByPlaceholderText('What should this helper do?'), { target: { value: 'Check it' } })
    fireEvent.click(screen.getByRole('button', { name: 'Attach helper' }))
    await waitFor(() => expect(attachSpy).toHaveBeenCalledWith('impl', expect.objectContaining({ profileId: 'reviewer' })))
  })
})
