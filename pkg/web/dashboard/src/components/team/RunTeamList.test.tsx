import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { RunTeamList } from './RunTeamList'
import type { MeshParticipant, MeshThread } from '../../api/mesh-client'

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true },
  { id: 'side:run-1', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'claude', reachable: true },
  // Сервер отдаёт участников, отсортированных по id: commit < implement < plan.
  { id: 'stage:commit', kind: 'stage_agent', scope: 'stage', stage_id: 'commit', display_name: 'Commit & cleanup', reachable: true },
  { id: 'stage:implement', kind: 'stage_agent', scope: 'stage', stage_id: 'implement', display_name: 'Implement feature', reachable: true },
  { id: 'helper:h1', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Correctness helper', reachable: false, unreachable_reason: 'task h1 has reached a terminal status' },
  { id: 'stage:plan', kind: 'stage_agent', scope: 'stage', stage_id: 'plan', display_name: 'Plan', reachable: true },
]

const threads: MeshThread[] = [
  { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
  { id: 'mt-e95e00425b9b4640a0bc', title: '', scope: 'run', members: ['user', 'stage:commit'], status: 'open' },
]

// Порядок флоу: plan → implement → commit.
const stages = [
  { id: 'plan', title: 'Plan', status: 'done' },
  { id: 'implement', title: 'Implement feature', status: 'running' },
  { id: 'commit', title: 'Commit & cleanup', status: 'pending' },
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

function inStages() {
  return within(screen.getByRole('region', { name: 'Stages' }))
}

function stageRowNames(): string[] {
  const section = screen.getByRole('region', { name: 'Stages' })
  return within(section)
    .getAllByRole('button')
    .filter((b) => b.dataset.kind === 'stage_agent')
    .map((b) => b.querySelector('.team-person-name')?.textContent ?? '')
}

describe('RunTeamList', () => {
  test('groups participants under "This run", "Stages" and "Conversations"', () => {
    renderList()
    expect(screen.getByRole('region', { name: 'This run' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Stages' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Conversations' })).toBeInTheDocument()
  })

  test('stages follow the flow order, not the server id order', () => {
    renderList()
    expect(stageRowNames()).toEqual(['Plan', 'Implement feature', 'Commit & cleanup'])
  })

  test('a stage is one row — its name is not repeated as a group heading', () => {
    renderList()
    expect(inStages().getAllByText('Commit & cleanup')).toHaveLength(1)
    expect(screen.queryByText(/^Stage · /)).toBeNull()
  })

  test('each stage row shows its live status; a pending stage is dimmed', () => {
    renderList()
    const commitRow = inStages().getByText('Commit & cleanup').closest('button') as HTMLElement
    expect(within(commitRow).getByText('Pending')).toBeInTheDocument()
    expect(commitRow).toHaveAttribute('data-status', 'pending')
    const implementRow = screen.getByText('Implement feature').closest('button') as HTMLElement
    expect(within(implementRow).getByText('Running')).toBeInTheDocument()
  })

  test("a stage's helpers are listed right under that stage", () => {
    renderList()
    const names = within(screen.getByRole('region', { name: 'Stages' }))
      .getAllByRole('button')
      .map((b) => b.querySelector('.team-person-name')?.textContent)
    expect(names).toEqual(['Plan', 'Implement feature', 'Correctness helper', 'Commit & cleanup'])
    const helperRow = screen.getByText('Correctness helper').closest('button') as HTMLElement
    expect(helperRow).toHaveClass('nested')
  })

  test('the human operator ("user") is never listed as a row', () => {
    renderList()
    expect(screen.queryByText('user')).toBeNull()
  })

  test('run-scoped rows say what they are: the Side agent and run agents', () => {
    renderList()
    const sideRow = screen.getByText('claude').closest('button') as HTMLElement
    expect(within(sideRow).getByText('Side agent')).toBeInTheDocument()
    const qaRow = screen.getByText('QA reviewer').closest('button') as HTMLElement
    expect(within(qaRow).getByText('Run agent')).toBeInTheDocument()
  })

  test('long names carry a tooltip with the full text', () => {
    renderList()
    expect(inStages().getByText('Commit & cleanup')).toHaveAttribute('title', 'Commit & cleanup')
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

  test('an unreachable helper shows its unreachable reason', () => {
    renderList()
    expect(screen.getByText('task h1 has reached a terminal status')).toBeInTheDocument()
  })

  test('"Add run agent" calls onAddRunAgent', () => {
    const { onAddRunAgent } = renderList()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    expect(onAddRunAgent).toHaveBeenCalled()
  })

  test('conversations are named by title, or by the other member when untitled', () => {
    const { onSelectThread } = renderList()
    const conversations = screen.getByRole('region', { name: 'Conversations' })
    expect(within(conversations).getByText('Commit & cleanup')).toBeInTheDocument()
    expect(within(conversations).queryByText(/^mt-/)).toBeNull()
    fireEvent.click(within(conversations).getByText('Hand-off'))
    expect(onSelectThread).toHaveBeenCalledWith(threads[0])
  })
})
