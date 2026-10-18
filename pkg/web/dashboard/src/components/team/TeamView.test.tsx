import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { TeamView } from './TeamView'
import * as meshClient from '../../api/mesh-client'
import type { MeshRoster } from '../../hooks/use-mesh'
import type { AfmEvent, Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'

function makeStage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 'implement', name: 'Implement feature', status: 'running', updatedAt: '', interactive: false,
    autonomous: false, autoApprove: false, hasDialog: false, showPlan: false, showDialog: false,
    isScript: false, pausedFrom: '', preNote: '', buttons: [],
    ...overrides,
  }
}

const profiles: AssistantProfile[] = [{ id: 'qa', title: 'QA reviewer', command: 'claude', available: true, unavailableReason: '' }]

const participants: meshClient.MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true },
  { id: 'stage:implement', kind: 'stage_agent', scope: 'stage', stage_id: 'implement', display_name: 'Implement agent', reachable: true },
]

function enabledRoster(overrides: Partial<MeshRoster> = {}): MeshRoster {
  return { enabled: true, probed: true, participants, threads: [], ...overrides }
}

// mockThreadAndBoardFetches stubs the network calls TeamView's CHILDREN still make directly (ThreadConversation's
// useMeshThread, BoardPanel's own poll) — TeamView itself no longer calls listMeshParticipants/listMeshThreads
// (roster is now owned by App.tsx and passed down as a prop, see TeamViewProps's own comment).
function mockThreadAndBoardFetches(): void {
  vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
  vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
}

function renderTeamView(stages: Stage[] = [makeStage()], events: AfmEvent[] = [], roster: MeshRoster = enabledRoster()) {
  return render(<TeamView roster={roster} stages={stages} events={events} profiles={profiles} />)
}

describe('TeamView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('roster groups participants under "This run", "Stage · …" and "Conversations"', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    expect(screen.getByText('This run')).toBeInTheDocument()
    expect(screen.getByText('Stage · Implement feature')).toBeInTheDocument()
    expect(screen.getByText('Conversations')).toBeInTheDocument()
  })

  test('tabs are "Messages", "Board", "Activity"', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const tablist = screen.getByRole('tablist', { name: 'Team' })
    expect(within(tablist).getByRole('tab', { name: 'Messages' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Board' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Activity' })).toBeInTheDocument()
  })

  test('a direct conversation opens from a person row', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    fireEvent.click(screen.getByText('QA reviewer'))
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())
  })

  test('"Create conversation" requires a topic and at least one participant', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    expect(screen.getByText('Enter a topic and select at least one participant.')).toBeInTheDocument()
  })

  test('"Create conversation" opens a new thread once a topic and a participant are chosen', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({
      id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open',
    })
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: 'Hand-off', scope: 'run', memberIds: ['run:qa'] })))
    await waitFor(() => expect(screen.getByPlaceholderText('Message Hand-off…')).toBeInTheDocument())
  })

  test('"Add run agent" shows a read-only "Configured executor" with no model selector, and "Cancel" creates nothing', () => {
    mockThreadAndBoardFetches()
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))

    const dialog = screen.getByRole('dialog', { name: 'Add run agent' })
    expect(within(dialog).getByText('Configured executor')).toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/model/i)).toBeNull()

    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(attachSpy).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog', { name: 'Add run agent' })).toBeNull()
  })

  test('a paused participant shows "Paused"', () => {
    mockThreadAndBoardFetches()
    renderTeamView([makeStage({ status: 'paused' })])
    expect(screen.getByText('Paused')).toBeInTheDocument()
  })

  test('renders the disabled empty state when the mesh capability is off', () => {
    renderTeamView([makeStage()], [], { enabled: false, probed: true, participants: [], threads: [] })
    expect(screen.getByText('Team is not available for this run')).toBeInTheDocument()
  })
})
