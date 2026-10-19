import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import { StagePeoplePanel } from './StagePeoplePanel'
import type { MeshParticipant } from '../../api/mesh-client'
import type { Stage } from '../../types'

function stage(id: string, status: Stage['status']): Stage {
  return {
    id, name: id, status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false,
    showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [],
  }
}

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', reachable: true, command: 'claude' },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true },
]

function renderPanel(overrides: Partial<Parameters<typeof StagePeoplePanel>[0]> = {}) {
  const onPick = vi.fn()
  const onClose = vi.fn()
  const onAgentAdded = vi.fn()
  render(
    <StagePeoplePanel
      stageId="impl"
      participants={participants}
      stages={[stage('impl', 'running')]}
      profiles={[{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }]}
      open
      onClose={onClose}
      onPick={onPick}
      onAgentAdded={onAgentAdded}
      {...overrides}
    />,
  )
  return { onPick, onClose, onAgentAdded }
}

describe('StagePeoplePanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('an added agent is handed to the owner right away, before the roster poll has it', async () => {
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't9', participant_id: 'run:t9', thread_id: 'mt-9' })
    const { onAgentAdded } = renderPanel()
    fireEvent.click(screen.getByRole('button', { name: 'Add agent' }))
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() =>
      expect(onAgentAdded).toHaveBeenCalledWith({ id: 'run:t9', kind: 'run_agent', scope: 'run', display_name: 'QA', reachable: true }),
    )
    // Its first conversation shows in this stage's feed.
    expect(attachSpy).toHaveBeenCalledWith(expect.objectContaining({ originStage: 'impl' }))
  })

  test('a pick addresses the person and closes the drawer', () => {
    const { onPick, onClose } = renderPanel()
    fireEvent.click(screen.getByText('QA reviewer'))
    expect(onPick).toHaveBeenCalledWith(participants[2])
    expect(onClose).toHaveBeenCalled()
  })

  test('without a composer the rows are disabled and a hint says when writing is possible', () => {
    renderPanel({ onPick: undefined })
    expect(screen.getByText('QA reviewer').closest('button')).toBeDisabled()
    expect(screen.getByText(/while this stage is running/)).toBeInTheDocument()
  })

  test('"+ Add agent" opens the Team tab\'s Add run agent dialog', () => {
    renderPanel()
    fireEvent.click(screen.getByRole('button', { name: 'Add agent' }))
    expect(screen.getByRole('dialog', { name: 'Add run agent' })).toBeInTheDocument()
  })

  test('the drawer closes on Escape and on its scrim', () => {
    const { onClose } = renderPanel()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Close people' }))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  test('the drawer takes the focus when opened, keeps Tab inside, and gives it back when closed', () => {
    const props = {
      stageId: 'impl', participants, stages: [stage('impl', 'running')], onClose: vi.fn(), onPick: vi.fn(), onAgentAdded: vi.fn(),
      profiles: [{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }],
    }
    const { rerender } = render(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open={false} />
      </>,
    )
    const opener = screen.getByRole('button', { name: 'Show people' })
    opener.focus()
    rerender(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open />
      </>,
    )
    const drawer = document.getElementById('stage-people') as HTMLElement
    expect(drawer.contains(document.activeElement)).toBe(true)

    // Tab from the last control wraps to the first one; Shift+Tab from the first goes to the last.
    const focusables = [...drawer.querySelectorAll<HTMLElement>('button:not([disabled])')]
    focusables.at(-1)?.focus()
    fireEvent.keyDown(drawer, { key: 'Tab' })
    expect(document.activeElement).toBe(focusables[0])
    fireEvent.keyDown(drawer, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(focusables.at(-1))

    rerender(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open={false} />
      </>,
    )
    expect(document.activeElement).toBe(opener)
  })
})
