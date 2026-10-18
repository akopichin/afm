import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { RunTeamList } from './RunTeamList'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true },
  { id: 'stage:implement', kind: 'stage_agent', scope: 'stage', stage_id: 'implement', display_name: 'Implement agent', reachable: true },
  { id: 'helper:h1', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Correctness helper', reachable: false, unreachable_reason: 'task h1 has reached a terminal status' },
]

const threads: MeshThread[] = [{ id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }]

const stages = [
  { id: 'implement', title: 'Implement feature', status: 'running' },
]

function renderList(overrides: Partial<Parameters<typeof RunTeamList>[0]> = {}) {
  const onSelectParticipant = vi.fn()
  const onSelectThread = vi.fn()
  const onAddRunAgent = vi.fn()
  render(
    <RunTeamList
      participants={participants}
      threads={threads}
      stages={stages}
      onSelectParticipant={onSelectParticipant}
      onSelectThread={onSelectThread}
      onAddRunAgent={onAddRunAgent}
      {...overrides}
    />,
  )
  return { onSelectParticipant, onSelectThread, onAddRunAgent }
}

describe('RunTeamList', () => {
  test('groups participants under "This run", "Stage · …" and "Conversations"', () => {
    renderList()
    expect(screen.getByText('This run')).toBeInTheDocument()
    expect(screen.getByText('Stage · Implement feature')).toBeInTheDocument()
    expect(screen.getByText('Conversations')).toBeInTheDocument()
  })

  test('the human operator ("user") is never listed as a row', () => {
    renderList()
    expect(screen.queryByText('user')).toBeNull()
  })

  test('"This run" lists run-scoped participants, "Stage · …" lists that stage\'s own', () => {
    renderList()
    expect(screen.getByText('QA reviewer')).toBeInTheDocument()
    expect(screen.getByText('Implement agent')).toBeInTheDocument()
    expect(screen.getByText('Correctness helper')).toBeInTheDocument()
  })

  test('clicking a person row calls onSelectParticipant with that participant', () => {
    const { onSelectParticipant } = renderList()
    fireEvent.click(screen.getByText('QA reviewer'))
    expect(onSelectParticipant).toHaveBeenCalledWith(participants[1])
  })

  test('a participant bound to a paused stage shows "Paused"', () => {
    renderList({ stages: [{ id: 'implement', title: 'Implement feature', status: 'paused' }] })
    expect(screen.getAllByText('Paused').length).toBeGreaterThan(0)
  })

  test('an unreachable participant (not paused) shows its unreachable reason', () => {
    renderList()
    expect(screen.getByText('task h1 has reached a terminal status')).toBeInTheDocument()
  })

  test('"Add run agent" calls onAddRunAgent', () => {
    const { onAddRunAgent } = renderList()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    expect(onAddRunAgent).toHaveBeenCalled()
  })

  test('clicking a conversation row calls onSelectThread', () => {
    const { onSelectThread } = renderList()
    fireEvent.click(screen.getByText('Hand-off'))
    expect(onSelectThread).toHaveBeenCalledWith(threads[0])
  })
})
