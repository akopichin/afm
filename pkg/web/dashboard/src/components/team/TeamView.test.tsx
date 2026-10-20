import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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

function mockTeamWidth(initialWidth: number) {
  let width = initialWidth
  const observers = new Set<{ callback: ResizeObserverCallback; targets: Set<Element> }>()
  vi.stubGlobal('ResizeObserver', class {
    private targets = new Set<Element>()
    private record: { callback: ResizeObserverCallback; targets: Set<Element> }
    constructor(callback: ResizeObserverCallback) {
      this.record = { callback, targets: this.targets }
      observers.add(this.record)
    }
    observe(target: Element) { this.targets.add(target) }
    unobserve(target: Element) { this.targets.delete(target) }
    disconnect() { observers.delete(this.record); this.targets.clear() }
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const measured = this.matches('.team-view') ? width : 0
    return { x: 0, y: 0, left: 0, top: 0, right: measured, bottom: 0, width: measured, height: 0, toJSON: () => ({}) } as DOMRect
  })
  vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(function (this: Element) {
    return this.matches('.team-view') ? width : 0
  })
  return {
    resize(nextWidth: number) {
      width = nextWidth
      act(() => {
        for (const observer of observers) {
          const entries = [...observer.targets]
            .filter((target) => target.isConnected)
            .map((target) => ({ target, contentRect: target.getBoundingClientRect() } as ResizeObserverEntry))
          if (entries.length > 0) observer.callback(entries, {} as ResizeObserver)
        }
      })
    },
  }
}

function teamSeparator() {
  return screen.getByRole('separator', { name: 'Resize run team panel' })
}

describe('TeamView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  })

  test('keeps side in the run roster and shows its durable terminal failure beside the original message', async () => {
    mockThreadAndBoardFetches()
    const side: meshClient.MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'Side agent', reachable: true }
    const thread: meshClient.MeshThread = { id: 'mt-side', scope: 'run', members: ['user', 'side:r1'], status: 'open' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-side', sender_id: 'user', recipient_ids: ['side:r1'], kind: 'message', body: 'help', seq: 1, created_at: '' },
    ], next_cursor: 1 })
    renderTeamView([makeStage()], [{ type: 'side_agent_mesh_failure', payload: { source_message_id: 'm1' }, stageId: '', timestamp: '' }],
      enabledRoster({ participants: [...participants, side], threads: [thread] }))
    expect(within(screen.getByRole('region', { name: 'This run' })).getAllByText('Side agent').length).toBeGreaterThan(0)
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByRole('button', { name: /Side agent/ }))
    await waitFor(() => expect(screen.getByText('Could not answer')).toBeInTheDocument())
  })

  test('recovers an old side failure from the thread on reconnect after event replay drops its notice', async () => {
    mockThreadAndBoardFetches()
    const side: meshClient.MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'Side agent', reachable: true }
    const thread: meshClient.MeshThread = { id: 'mt-side', scope: 'run', members: ['user', 'side:r1'], status: 'closed' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-side', sender_id: 'user', recipient_ids: ['side:r1'], kind: 'message', body: 'help', side_agent_failed: true, seq: 1, created_at: '' },
    ], next_cursor: 1 })
    renderTeamView([makeStage()], [], enabledRoster({ participants: [...participants, side], threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByRole('button', { name: /Side agent/ }))
    await waitFor(() => expect(document.querySelector('[data-message-id="m1"]')).toHaveTextContent('Could not answer'))
  })

  test('does not show a failure on a passive side result whose orphan receipt was released', async () => {
    mockThreadAndBoardFetches()
    const side: meshClient.MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'Side agent', reachable: true }
    const thread: meshClient.MeshThread = { id: 'mt-side', scope: 'run', members: ['user', 'side:r1'], status: 'open' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [
      { id: 'm1', thread_id: 'mt-side', sender_id: 'user', recipient_ids: ['side:r1'], kind: 'result', auto_result: true, body: 'passive', side_agent_failed: true, seq: 1, created_at: '' },
    ], next_cursor: 1 })
    const receipt = { type: 'mesh_receipt', payload: { message_id: 'm1', participant_id: 'side:r1', status: 'undeliverable' }, stageId: '', timestamp: '' }
    renderTeamView([makeStage()], [receipt], enabledRoster({ participants: [...participants, side], threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByRole('button', { name: /Side agent/ }))
    await waitFor(() => expect(document.querySelector('[data-message-id="m1"]')).toHaveTextContent('passive'))
    expect(document.querySelector('[data-message-id="m1"]')).not.toHaveTextContent('Could not answer')
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

  test('a conversation agents started between themselves does not count you in it', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-8', title: 'Pairing', scope: 'run', members: ['run:qa', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Pairing'))
    expect(screen.getByText('2 participants · 2 agents')).toBeInTheDocument()
  })

  test('tabs are "Messages", "Board", "Activity"', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const tablist = screen.getByRole('tablist', { name: 'Team' })
    expect(within(tablist).getByRole('tab', { name: 'Messages' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Board' })).toBeInTheDocument()
    expect(within(tablist).getByRole('tab', { name: 'Activity' })).toBeInTheDocument()
  })

  test('the Board tab counts the tasks on the board, not the reports filed under them or facts', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [
        { key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1, seq: 1, ts: '' },
        { key: 'task/health/qa', status: 'checked', author_id: 'run:qa', version: 1, seq: 2, ts: '' },
        { key: 'task/export', status: 'CSV export', author_id: 'user', version: 1, seq: 3, ts: '' },
        { key: 'api/ready', status: 'yes', author_id: 'stage:implement', version: 1, seq: 4, ts: '' },
      ],
    })
    renderTeamView()
    const tab = screen.getByRole('tab', { name: 'Board' })
    await waitFor(() => expect(tab).toHaveTextContent('Board2'))
    expect(tab).not.toHaveTextContent('Board3')
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

  test('the labeled vertical separator grows Run team to the right and survives remount independently of People', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(1200)
    localStorage.setItem('afm.peopleWidth', '400')
    const view = renderTeamView()
    const handle = teamSeparator()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    expect(handle).toHaveAttribute('aria-valuemin', '200')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
    expect(handle).toHaveAttribute('aria-valuenow', '260')

    fireEvent.mouseDown(handle, { clientX: 260 })
    fireEvent.mouseMove(document, { clientX: 340 })
    fireEvent.mouseUp(document)
    expect(layout.style.getPropertyValue('--team-roster-width')).toBe('340px')
    expect(handle).toHaveAttribute('aria-valuenow', '340')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('340')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('400')

    view.unmount()
    const remount = renderTeamView()
    expect((remount.container.querySelector('.team-view') as HTMLElement).style.getPropertyValue('--team-roster-width')).toBe('340px')
    expect(teamSeparator()).toHaveAttribute('aria-valuenow', '340')
  })

  test('ArrowRight widens Run team, ArrowLeft narrows it, both clamp, and double-click resets', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(1200)
    const view = renderTeamView()
    const handle = teamSeparator()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(handle).toHaveAttribute('aria-valuenow', '276')
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '260')
    fireEvent.mouseDown(handle, { clientX: 260 })
    fireEvent.mouseMove(document, { clientX: 9999 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '560')
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(handle).toHaveAttribute('aria-valuenow', '560')
    fireEvent.doubleClick(handle)
    expect(layout.style.getPropertyValue('--team-roster-width')).toBe('260px')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('260')
    fireEvent.mouseDown(handle, { clientX: 260 })
    fireEvent.mouseMove(document, { clientX: -9999 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '200')
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '200')
  })

  test('a narrow desktop Team container clamps the visible width without changing the stored preference', () => {
    mockThreadAndBoardFetches()
    localStorage.setItem('afm.teamRosterWidth', '500')
    const measured = mockTeamWidth(600)
    const view = renderTeamView()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const handle = teamSeparator()
    expect(layout.style.getPropertyValue('--team-roster-width')).toBe('280px')
    expect(handle).toHaveAttribute('aria-valuenow', '280')
    expect(handle).toHaveAttribute('aria-valuemax', '280')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('500')

    measured.resize(580)
    expect(handle).toHaveAttribute('aria-valuenow', '260')
    expect(handle).toHaveAttribute('aria-valuemax', '260')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('500')

    measured.resize(1000)
    expect(layout.style.getPropertyValue('--team-roster-width')).toBe('500px')
    expect(handle).toHaveAttribute('aria-valuenow', '500')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
  })

  test('outward resize at a temporary clamp keeps the saved Run team preference', () => {
    mockThreadAndBoardFetches()
    localStorage.setItem('afm.teamRosterWidth', '500')
    const measured = mockTeamWidth(600)
    const view = renderTeamView()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const handle = teamSeparator()
    expect(handle).toHaveAttribute('aria-valuenow', '280')

    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    fireEvent.mouseDown(handle, { clientX: 260 })
    fireEvent.mouseMove(document, { clientX: 360 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '280')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('500')

    measured.resize(1000)
    expect(layout.style.getPropertyValue('--team-roster-width')).toBe('500px')
  })

  test('a measured Team container that narrows into drawer mode transfers separator focus', () => {
    mockThreadAndBoardFetches()
    const measured = mockTeamWidth(600)
    const view = renderTeamView()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const handle = teamSeparator()
    const toggle = screen.getByRole('button', { name: 'Show team roster' })
    handle.focus()

    measured.resize(341)
    expect(layout).toHaveClass('drawer')
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    expect(handle).toHaveAttribute('tabindex', '-1')
    expect(toggle).toHaveFocus()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
    fireEvent.click(screen.getByRole('button', { name: 'Close team roster' }))
    expect(screen.getByRole('navigation', { name: 'Team roster' })).not.toHaveClass('open')
  })

  test('an initially narrow Team container starts in drawer mode', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(341)
    const view = renderTeamView()
    expect(view.container.querySelector('.team-view')).toHaveClass('drawer')
    const handle = view.container.querySelector('.team-roster-resizer')
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    expect(handle).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('button', { name: 'Show team roster' })).toBeInTheDocument()
  })

  test('keeps measuring Team after a temporary disabled state', () => {
    mockThreadAndBoardFetches()
    const measured = mockTeamWidth(600)
    const initialRoster = enabledRoster({ probed: false })
    const view = render(
      <TeamView roster={initialRoster} stages={[makeStage()]} events={[]} profiles={profiles} />,
    )
    const firstLayout = view.container.querySelector('.team-view')
    expect(teamSeparator()).toHaveAttribute('aria-valuemax', '280')

    view.rerender(<TeamView roster={enabledRoster({ enabled: false })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByText('Team is not available for this run')).toBeInTheDocument()
    view.rerender(<TeamView roster={enabledRoster()} stages={[makeStage()]} events={[]} profiles={profiles} />)
    const nextLayout = view.container.querySelector('.team-view')
    expect(nextLayout).toBe(firstLayout)

    measured.resize(500)
    expect(nextLayout).toHaveClass('drawer')
    expect(nextLayout?.querySelector('.team-roster-resizer')).toHaveAttribute('aria-hidden', 'true')
  })

  test('invalid stored width falls back to the Run team default', () => {
    mockThreadAndBoardFetches()
    localStorage.setItem('afm.teamRosterWidth', 'NaN')
    mockTeamWidth(1200)
    renderTeamView()
    expect(teamSeparator()).toHaveAttribute('aria-valuenow', '260')
  })

  test('mouseup outside the handle and unmount clean up drag listeners and body styles', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(1200)
    const view = renderTeamView()
    const handle = teamSeparator()
    fireEvent.mouseDown(handle, { clientX: 260 })
    fireEvent.mouseMove(document, { clientX: 310 })
    fireEvent.mouseUp(document.body)
    expect(document.body.style.cursor).toBe('')
    expect(document.body.style.userSelect).toBe('')
    fireEvent.mouseMove(document, { clientX: 500 })
    expect(handle).toHaveAttribute('aria-valuenow', '310')

    fireEvent.mouseDown(handle, { clientX: 260 })
    expect(document.body.style.cursor).toBe('col-resize')
    view.unmount()
    expect(document.body.style.cursor).toBe('')
    expect(document.body.style.userSelect).toBe('')
    const savedWidth = localStorage.getItem('afm.teamRosterWidth')
    fireEvent.mouseMove(document, { clientX: 500 })
    fireEvent.mouseUp(document)
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe(savedWidth)
  })

  test('renders the disabled empty state when the mesh capability is off', () => {
    renderTeamView([makeStage()], [], { enabled: false, probed: true, participants: [], threads: [] })
    expect(screen.getByText('Team is not available for this run')).toBeInTheDocument()
  })

  test('the tabs follow the tab pattern: arrows switch tabs and each tab controls its panel', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const messages = screen.getByRole('tab', { name: 'Messages' })
    expect(messages).toHaveAttribute('aria-controls', screen.getByRole('tabpanel').id)
    messages.focus()
    fireEvent.keyDown(messages, { key: 'ArrowRight' })
    const board = screen.getByRole('tab', { name: /^Board/ })
    expect(board).toHaveFocus()
    expect(board).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', board.id)
    expect(screen.getByRole('button', { name: 'Post task' })).toBeInTheDocument()
  })

  test('a direct send that finishes after the user picked another conversation does not switch back to it', async () => {
    mockThreadAndBoardFetches()
    const opened: meshClient.MeshThread = { id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(opened)
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalled())

    fireEvent.click(screen.getByText('With Implement agent'))
    await act(async () => resolveSend({ message_id: 'm1', seq: 1 }))
    expect(screen.getByRole('heading', { name: 'With Implement agent' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message With Implement agent…')).toBeInTheDocument()
  })

  test('a conversation created after the user picked another one is listed but does not steal the selection', async () => {
    mockThreadAndBoardFetches()
    let resolveOpen!: (v: meshClient.MeshThread) => void
    vi.spyOn(meshClient, 'openMeshThread').mockReturnValue(new Promise((res) => (resolveOpen = res)))
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.click(screen.getByText('With Implement agent'))
    await act(async () => resolveOpen({ id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }))

    expect(screen.getByRole('heading', { name: 'With Implement agent' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Hand-off')).toBeInTheDocument()
  })

  test('text typed while the first direct message is sending stays in the composer of the opened thread', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    renderTeamView()

    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    const composer = screen.getByPlaceholderText('Message QA reviewer…')
    fireEvent.change(composer, { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalled())
    fireEvent.change(composer, { target: { value: 'please review\nand the tests too' } })

    await act(async () => resolveSend({ message_id: 'm1', seq: 1 }))
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('QA reviewer')).toBeInTheDocument())
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('and the tests too')
  })

  test('a draft stays with its conversation when the user switches away and back', () => {
    mockThreadAndBoardFetches()
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'half-written' } })
    fireEvent.click(screen.getByText('With Implement agent'))
    expect((screen.getByPlaceholderText('Message With Implement agent…') as HTMLTextAreaElement).value).toBe('')
    fireEvent.click(screen.getByRole('button', { name: /^QA reviewer/ }))
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('half-written')
  })

  test('a message that finished sending after the user switched away is gone from the draft on return', async () => {
    mockThreadAndBoardFetches()
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    const withQA: meshClient.MeshThread = { id: 'mt-9', title: 'With QA', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [withQA, other] }))

    fireEvent.click(screen.getByText('With QA'))
    fireEvent.change(screen.getByPlaceholderText('Message With QA…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalled())
    fireEvent.click(screen.getByText('With Implement agent'))
    await act(async () => resolveSend({ message_id: 'm1', seq: 1 }))

    fireEvent.click(screen.getByText('With QA'))
    expect((screen.getByPlaceholderText('Message With QA…') as HTMLTextAreaElement).value).toBe('')
  })

  test('"Create conversation" locks its fields while the conversation is being created', async () => {
    mockThreadAndBoardFetches()
    let resolveOpen!: (v: meshClient.MeshThread) => void
    vi.spyOn(meshClient, 'openMeshThread').mockReturnValue(new Promise((res) => (resolveOpen = res)))
    renderTeamView()

    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))

    expect(screen.getByPlaceholderText('What is this about?')).toBeDisabled()
    for (const box of screen.getAllByRole('checkbox')) expect(box).toBeDisabled()
    await act(async () => resolveOpen({ id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }))
    expect(screen.getByPlaceholderText('Message Hand-off…')).toBeInTheDocument()
  })

  test('a retry of the same "Create conversation" form reuses its request id', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockRejectedValue(new Error('network'))
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    fireEvent.click(screen.getByRole('button', { name: 'Create conversation' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(2))
    expect(openSpy.mock.calls[1]?.[0].requestId).toBe(openSpy.mock.calls[0]?.[0].requestId)
  })
})
