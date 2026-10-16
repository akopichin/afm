import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AddStageDialog } from './AddStageDialog'

const stages = [
  { id: 'build', title: 'Build' },
  { id: 'deploy', title: 'Deploy' },
]

function renderDialog(overrides: Partial<Parameters<typeof AddStageDialog>[0]> = {}) {
  const onCommitted = vi.fn()
  const onCancel = vi.fn()
  render(
    <AddStageDialog
      capabilityEnabled={true}
      graphRevision={3}
      stages={stages}
      onCommitted={onCommitted}
      onCancel={onCancel}
      {...overrides}
    />,
  )
  return { onCommitted, onCancel }
}

function okResponse(body: unknown): Response {
  return { ok: true, json: async () => body } as Response
}

function errResponse(status: number, code: string): Response {
  return { ok: false, status, json: async () => ({ error: code }) } as Response
}

function fillCommonFields(stageId: string, command: string, prompt: string): void {
  fireEvent.change(screen.getByLabelText(/^stage id$/i), { target: { value: stageId } })
  fireEvent.change(screen.getByLabelText(/^command$/i), { target: { value: command } })
  fireEvent.change(screen.getByLabelText(/^prompt$/i), { target: { value: prompt } })
}

describe('AddStageDialog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // TestDialog_ShowsDependencyDiffNotJustPosition — dev-plan.md §11.3: inserting before an existing stage must
  // surface the actual dependency DIFF of the target (before vs. after), not merely "a new row will appear".
  test('TestDialog_ShowsDependencyDiffNotJustPosition', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      okResponse({
        stage: { id: 'lint', command: 'claude', prompt: 'lint it', depends_on: ['build'], extra_deps: { deploy: ['lint'] } },
        graph_revision: 4,
      }),
    )
    renderDialog()

    fireEvent.click(screen.getByRole('radio', { name: /insert before an existing stage/i }))
    fireEvent.change(screen.getByLabelText(/target stage/i), { target: { value: 'deploy' } })
    fillCommonFields('lint', 'claude', 'lint it')

    const before = (await screen.findByText(/before — deploy depends on/i)).closest('.graph-preview-diff-col')
    const after = screen.getByText(/after — deploy depends on/i).closest('.graph-preview-diff-col')
    expect(before).toHaveTextContent('build')
    expect(before).not.toHaveTextContent('lint')
    expect(after).toHaveTextContent('build')
    expect(after).toHaveTextContent('lint')
  })

  // TestDialog_ShowsWhatNewStageWillWaitFor — appending to the end must name the actual dependency ids the
  // new stage will wait for, with any warning (e.g. a failed dependency) visibly flagged, not buried in prose.
  test('TestDialog_ShowsWhatNewStageWillWaitFor', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      okResponse({
        stage: {
          id: 'notify',
          command: 'claude',
          prompt: 'notify the team',
          depends_on: ['review', 'docs'],
          warnings: ['dependency "review" is currently failed'],
        },
        graph_revision: 4,
      }),
    )
    renderDialog()

    fillCommonFields('notify', 'claude', 'notify the team')

    await screen.findByText('review')
    expect(screen.getByText('docs')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/review.*currently failed/i)
  })

  // TestDialog_DisabledWhenCapabilityOff — when the server reports capabilities.add_stage === false, the
  // dialog must not let the user submit anything, and must never even attempt a preview round trip.
  test('TestDialog_DisabledWhenCapabilityOff', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderDialog({ capabilityEnabled: false })

    expect(screen.getByRole('button', { name: /add stage/i })).toBeDisabled()
    expect(screen.getByLabelText(/^stage id$/i)).toBeDisabled()
    expect(screen.getByText(/does not support adding stages/i)).toBeInTheDocument()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  // TestDialog_StaleRevisionShowsFreshPreview — a stale_revision rejection must trigger an automatic re-fetch
  // of a fresh preview instead of leaving the user staring at a dead error.
  test('TestDialog_StaleRevisionShowsFreshPreview', async () => {
    let call = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      call += 1
      if (call === 1) return errResponse(409, 'stale_revision')
      return okResponse({
        stage: { id: 'notify', command: 'claude', prompt: 'notify the team', depends_on: ['docs'] },
        graph_revision: 9,
      })
    })
    renderDialog()

    fillCommonFields('notify', 'claude', 'notify the team')

    await screen.findByText('docs')
    expect(call).toBe(2)
    expect(screen.queryByText(/preview failed/i)).not.toBeInTheDocument()
  })

  // TestDialog_ThemesAndKeyboard — shares the same theme-agnostic modal-overlay/modal-content classes and
  // focus-trap mechanics as AttachAssistantDialog: initial focus lands inside the dialog, Escape cancels.
  test('TestDialog_ThemesAndKeyboard', () => {
    const { onCancel } = renderDialog()

    const dialog = screen.getByRole('dialog')
    expect(dialog.querySelector('.modal-content.add-stage-dialog')).not.toBeNull()
    expect(dialog.contains(document.activeElement)).toBe(true)

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})
