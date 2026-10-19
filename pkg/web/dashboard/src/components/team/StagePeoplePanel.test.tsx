import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
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
  render(
    <StagePeoplePanel
      stageId="impl"
      participants={participants}
      stages={[stage('impl', 'running')]}
      profiles={[{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }]}
      open
      onClose={onClose}
      onPick={onPick}
      {...overrides}
    />,
  )
  return { onPick, onClose }
}

describe('StagePeoplePanel', () => {
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
})
