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

  test('roster groups participants under "This run", "Stages" and "Conversations"', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    expect(screen.getByRole('region', { name: 'This run' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Stages' })).getByText('Implement agent')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Conversations' })).toBeInTheDocument()
  })

  test('the conversation header names the selected person and what they are', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    expect(screen.getByRole('heading', { name: 'Team' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    expect(await screen.findByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.getByText('Direct conversation · Run agent')).toBeInTheDocument()
  })

  test('a thread header counts its participants', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Hand-off'))
    expect(screen.getByRole('heading', { name: 'Hand-off' })).toBeInTheDocument()
    expect(screen.getByText('3 participants · you and 2 agents')).toBeInTheDocument()
  })

  test('tabs are "Messages", "Board", "Activity"', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const tablist = screen.getByRole('tablist', { name: 'Team' })
    expect(within(tablist).getByRole('tab', { name: 'Messages' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Board' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Activity' })).toBeInTheDocument()
  })

  test('the Board tab counts the tasks on the board, not the reports filed under them', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1 },
        { key: 'task/health/qa', status: 'checked', author_id: 'run:qa', version: 1 },
        { key: 'api/ready', status: 'yes', author_id: 'stage:implement', version: 1 },
      ],
    } as Awaited<ReturnType<typeof meshClient.getMeshBoard>>)
    renderTeamView()
    const tab = screen.getByRole('tab', { name: 'Board' })
    await waitFor(() => expect(tab).toHaveTextContent('Board2'))
  })

  test('a direct conversation opens from a person row', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
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

  test('a new conversation cannot include a participant that can no longer answer (a finished stage)', () => {
    mockThreadAndBoardFetches()
    const done: meshClient.MeshParticipant = { id: 'stage:plan', kind: 'stage_agent', scope: 'stage', stage_id: 'plan', display_name: 'Plan agent', reachable: false }
    renderTeamView([makeStage(), makeStage({ id: 'plan', name: 'Plan', status: 'done' })], [], enabledRoster({ participants: [...participants, done] }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    const form = document.querySelector('.team-create-conversation-participants') as HTMLElement
    expect(within(form).getByText('QA reviewer')).toBeInTheDocument()
    expect(within(form).queryByText('Plan agent')).toBeNull()
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

  test('switching to a different conversation sends into the newly selected thread, not the previous one', async () => {
    mockThreadAndBoardFetches()
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const threadA: meshClient.MeshThread = { id: 'mt-a', title: 'With QA', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const threadB: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [threadA, threadB] }))

    fireEvent.click(screen.getByText('With QA'))
    await waitFor(() => expect(screen.getByPlaceholderText('Message With QA…')).toBeInTheDocument())

    fireEvent.click(screen.getByText('With Implement agent'))
    await waitFor(() => expect(screen.getByPlaceholderText('Message With Implement agent…')).toBeInTheDocument())

    fireEvent.change(screen.getByPlaceholderText('Message With Implement agent…'), { target: { value: 'for B' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(sendSpy).toHaveBeenCalled())
    expect(sendSpy).toHaveBeenCalledWith('mt-b', expect.objectContaining({ body: 'for B' }))
  })

  test('a failed first send keeps the draft and error visible even though the thread opened', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({
      id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open',
    })
    vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValue(new Error('boom'))
    renderTeamView()

    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())

    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('please review')
  })

  test('re-selecting the same person after a successful first send returns to the opened thread instead of opening a second one', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({
      id: 'mt-9', title: 'QA reviewer', scope: 'run', members: ['user', 'run:qa'], status: 'open',
    })
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    // roster.threads intentionally stays empty throughout — the 3s poll has not caught up with the thread
    // the send below opens, which is exactly the window the duplicate-thread bug lived in.
    renderTeamView()

    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1))

    // The thread now also shows under Conversations right away; click the PERSON row again.
    fireEvent.click(within(screen.getByRole('region', { name: 'This run' })).getByRole('button', { name: /^QA reviewer/ }))
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'again' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalledWith('mt-9', expect.objectContaining({ body: 'again' })))
    expect(openSpy).toHaveBeenCalledTimes(1)
  })

  test('a thread that opened but whose first send failed is still re-used when the person is clicked again', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-9', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
    vi.spyOn(meshClient, 'sendMeshMessage').mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce({ message_id: 'm1', seq: 1 })
    renderTeamView()
    const runRegion = () => screen.getByRole('region', { name: 'This run' })

    fireEvent.click(within(runRegion()).getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'first' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))

    // Leave and come back before any roster poll: the thread opened by the failed attempt must be reused.
    fireEvent.click(within(screen.getByRole('region', { name: 'Stages' })).getByRole('button', { name: /^Implement agent/ }))
    fireEvent.click(within(runRegion()).getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'second' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenLastCalledWith('mt-9', expect.objectContaining({ body: 'second' })))
    expect(openSpy).toHaveBeenCalledTimes(1)
  })

  test('a conversation created with the form shows up in Conversations at once and is reused for that person', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-7', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Release' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Release')).toBeInTheDocument())

    fireEvent.click(within(screen.getByRole('region', { name: 'This run' })).getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(await screen.findByPlaceholderText('Message Release…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalledWith('mt-7', expect.objectContaining({ body: 'hi' })))
    expect(openSpy).toHaveBeenCalledTimes(1)
  })

  test('a new run agent is shown by name right away, never by its participant id', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'run:t-1', thread_id: 'mt-5' })
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    const dialog = screen.getByRole('dialog', { name: 'Add run agent' })
    fireEvent.change(within(dialog).getByPlaceholderText('What should this run agent do?'), { target: { value: 'watch tests' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add run agent' }))
    expect(await screen.findByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.queryByText(/run:t-1/)).toBeNull()
    expect(within(screen.getByRole('region', { name: 'This run' })).getAllByText('QA reviewer').length).toBeGreaterThan(0)
  })

  test('the roster drawer toggle (narrow screens) opens the roster and picking someone closes it', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const toggle = screen.getByRole('button', { name: 'Show team roster' })
    const roster = screen.getByRole('navigation', { name: 'Team roster' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(roster).not.toHaveClass('open')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(roster).toHaveClass('open')
    fireEvent.click(within(screen.getByRole('region', { name: 'This run' })).getByRole('button', { name: /^QA reviewer/ }))
    expect(roster).not.toHaveClass('open')
    expect(await screen.findByPlaceholderText('Message QA reviewer…')).toBeInTheDocument()
  })

  test('renders the disabled empty state when the mesh capability is off', () => {
    renderTeamView([makeStage()], [], { enabled: false, probed: true, participants: [], threads: [] })
    expect(screen.getByText('Team is not available for this run')).toBeInTheDocument()
  })
})
