import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { TeamView, type TeamViewProps } from './TeamView'
import * as meshClient from '../../api/mesh-client'
import { mergeById } from '../../lib/mesh-roster'
import { useMeshBoard, type MeshBoard, type MeshRoster } from '../../hooks/use-mesh'
import type { AfmEvent, Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import { isTeamUnread } from '../../lib/team-unread'
import { typedTaskView } from '../../test/board-tasks'

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
  vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
}

// TeamViewWithBoard — TeamView with the real board poll its parent (App) owns.
// It also plays App's part for added run agents: it merges them into the roster it passes down.
function TeamViewWithBoard({ roster, ...props }: Omit<TeamViewProps, 'board' | 'onParticipantAdded'>) {
  const board = useMeshBoard()
  const [added, setAdded] = useState<meshClient.MeshParticipant[]>([])
  const merged = { ...roster, participants: mergeById(roster.participants, added) }
  return <TeamView board={board} roster={merged} onParticipantAdded={(p) => setAdded((prev) => [...prev, p])} {...props} />
}

function renderTeamView(stages: Stage[] = [makeStage()], events: AfmEvent[] = [], roster: MeshRoster = enabledRoster()) {
  return render(<TeamViewWithBoard runId="test-existing" roster={roster} stages={stages} events={events} profiles={profiles} />)
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

function showPeople(): void {
  fireEvent.click(screen.getByRole('tab', { name: 'People' }))
}

function showConversations(): void {
  fireEvent.click(screen.getByRole('tab', { name: 'Conversations' }))
}

function peopleRegion(name: 'This run' | 'Stages'): HTMLElement {
  showPeople()
  return screen.getByRole('region', { name })
}

function pickRunPerson(name: RegExp): void {
  fireEvent.click(within(peopleRegion('This run')).getByRole('button', { name }))
}

function clickCreateConversation(): void {
  const submit = screen.queryByRole('button', { name: 'Create conversation' })
  fireEvent.click(submit ?? screen.getByRole('button', { name: 'New conversation' }))
}

function openAddRunAgent(): void {
  showPeople()
  fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
}

describe('TeamView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  })

  test('Board and People keep a new marker until Messages shows the rendered incoming message', async () => {
    const thread: meshClient.MeshThread = { id: 'mt-unread-board', title: 'QA hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const summary = (seq: number): meshClient.MeshThreadSummary => ({ last_other_message_seq: seq, title_seq: 0 })
    const message: meshClient.MeshMessage = { id: 'm2', thread_id: thread.id, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'new answer', seq: 2, created_at: '' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message], next_cursor: 2 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
    const initial = enabledRoster({ threads: [thread], summaries: { [thread.id]: summary(1) } })
    const view = render(<TeamViewWithBoard runId="unread-board-people" roster={initial} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    view.rerender(<TeamViewWithBoard runId="unread-board-people" roster={enabledRoster({ threads: [thread], summaries: { [thread.id]: summary(2) } })}
      stages={[makeStage()]} events={[]} profiles={profiles} />)
    await waitFor(() => expect(screen.getByRole('button', { name: /QA hand-off.*New messages/ })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('tab', { name: 'People' }))
    fireEvent.click(screen.getByRole('tab', { name: 'Conversations' }))
    expect(screen.getByRole('button', { name: /New to you.*1/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /QA hand-off.*New messages/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: 'Messages' }))
    await waitFor(() => expect(screen.getByRole('button', { name: /New to you.*0/ })).toBeInTheDocument())
  })

  test('incoming message stays new while the selected conversation is scrolled up', async () => {
    const thread: meshClient.MeshThread = { id: 'mt-scrolled-up', title: 'Pinger', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const summary = (seq: number): meshClient.MeshThreadSummary => ({ last_other_message_seq: seq, title_seq: 0 })
    const message = (seq: number): meshClient.MeshMessage => ({ id: `m${seq}`, thread_id: thread.id, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'message', body: `ping ${seq}`, seq, created_at: '' })
    const getThread = vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message(5)], next_cursor: 5 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
    const roster = (seq: number) => enabledRoster({ threads: [thread], summaries: { [thread.id]: summary(seq) } })
    const view = render(<TeamViewWithBoard runId="unread-scrolled-up" roster={roster(5)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    await waitFor(() => expect(screen.getByText('ping 5')).toBeInTheDocument())

    // The reader scrolls the long log to its top: far from the bottom, so nothing new is read.
    const log = screen.getByRole('log', { name: 'Messages' })
    Object.defineProperty(log, 'scrollHeight', { configurable: true, value: 1710 })
    Object.defineProperty(log, 'clientHeight', { configurable: true, value: 455 })
    log.scrollTop = 0
    fireEvent.scroll(log)

    getThread.mockResolvedValue({ messages: [message(5), message(6)], next_cursor: 6 })
    view.rerender(<TeamViewWithBoard runId="unread-scrolled-up" roster={roster(6)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    await waitFor(() => expect(screen.getByText('ping 6')).toBeInTheDocument(), { timeout: 5000 })
    expect(screen.getByRole('button', { name: /New to you.*1/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Pinger.*New messages/ })).toBeInTheDocument()
  })

  test.each([false, true])('visible Messages marks incoming read while People is selected (narrow=%s)', async (narrow) => {
    if (narrow) mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: `mt-people-${narrow}`, title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const message: meshClient.MeshMessage = { id: 'incoming', thread_id: thread.id, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'ready', seq: 2, created_at: '' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message], next_cursor: 2 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
    const roster = (seq: number) => enabledRoster({ threads: [thread], summaries: { [thread.id]: { last_other_message_seq: seq, title_seq: 0 } } })
    const view = render(<TeamViewWithBoard runId={`people-read-${narrow}`} roster={roster(1)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    showPeople()
    view.rerender(<TeamViewWithBoard runId={`people-read-${narrow}`} roster={roster(2)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    if (narrow) pickRunPerson(/^QA reviewer/)
    else fireEvent.click(within(screen.getByRole('region', { name: 'This run' })).getByRole('button', { name: /^QA reviewer/ }))
    await waitFor(() => expect(screen.getByText('ready')).toBeInTheDocument())
    showConversations()
    await waitFor(() => expect(screen.getByRole('button', { name: /New to you.*0/ })).toBeInTheDocument())
  })

  test('New conversation form covering a selected chat does not clear its incoming marker', async () => {
    const thread: meshClient.MeshThread = { id: 'mt-covered', title: 'Review', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const message: meshClient.MeshMessage = { id: 'incoming', thread_id: thread.id, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'covered message', seq: 2, created_at: '' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message], next_cursor: 2 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
    const roster = (seq: number) => enabledRoster({ threads: [thread], summaries: { [thread.id]: { last_other_message_seq: seq, title_seq: 0 } } })
    const view = render(<TeamViewWithBoard runId="covered-read" roster={roster(1)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    clickCreateConversation()
    view.rerender(<TeamViewWithBoard runId="covered-read" roster={roster(2)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    await waitFor(() => expect(screen.getByText('covered message')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /New to you.*1/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.getByRole('button', { name: /New to you.*0/ })).toBeInTheDocument())
  })

  test('Add run agent dialog keeps incoming unread until the visible chat is uncovered', async () => {
    const thread: meshClient.MeshThread = { id: 'mt-agent-covered', title: 'Review', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const message: meshClient.MeshMessage = { id: 'incoming', thread_id: thread.id, sender_id: 'run:qa', recipient_ids: ['user'], kind: 'reply', body: 'arrived under dialog', seq: 2, created_at: '' }
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [message], next_cursor: 2 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [] })
    const roster = (seq: number) => enabledRoster({ threads: [thread], summaries: { [thread.id]: { last_other_message_seq: seq, title_seq: 0 } } })
    const view = render(<TeamViewWithBoard runId="agent-covered-read" roster={roster(1)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    openAddRunAgent()
    view.rerender(<TeamViewWithBoard runId="agent-covered-read" roster={roster(2)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    await waitFor(() => expect(screen.getByText('arrived under dialog')).toBeInTheDocument())
    expect(isTeamUnread('agent-covered-read', thread.id, 2)).toBe(true)
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Add run agent' })).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(isTeamUnread('agent-covered-read', thread.id, 2)).toBe(false))
  })

  test('a new thread after the page baseline is marked without changing server receipts', async () => {
    mockThreadAndBoardFetches()
    const send = vi.spyOn(meshClient, 'sendMeshMessage')
    const topic = vi.spyOn(meshClient, 'updateMeshThreadTitle')
    const thread: meshClient.MeshThread = { id: 'mt-late-thread', title: 'Late review', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = render(<TeamViewWithBoard runId="late-thread" roster={enabledRoster()} stages={[makeStage()]} events={[]} profiles={profiles} />)
    view.rerender(<TeamViewWithBoard runId="late-thread" roster={enabledRoster({ threads: [thread], summaries: {
      [thread.id]: { last_other_message_seq: 4, title_seq: 0 },
    } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('button', { name: /Late review.*New messages/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /New to you.*1/ })).toBeInTheDocument()
    expect(send).not.toHaveBeenCalled()
    expect(topic).not.toHaveBeenCalled()
  })

  test('Team remount retains a marker, a new run baselines it, and filtering preserves the selected draft', () => {
    mockThreadAndBoardFetches()
    const first: meshClient.MeshThread = { id: 'mt-unread-first', title: 'First thread', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const second: meshClient.MeshThread = { id: 'mt-unread-second', title: 'Second thread', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    const roster = (seq: number) => enabledRoster({ threads: [first, second], summaries: {
      [first.id]: { last_other_message_seq: 1, title_seq: 0 },
      [second.id]: { last_other_message_seq: seq, title_seq: 0 },
    } })
    const view = render(<TeamViewWithBoard runId="unread-remount" roster={roster(1)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.change(screen.getByPlaceholderText('Message First thread…'), { target: { value: 'draft stays' } })
    view.rerender(<TeamViewWithBoard runId="unread-remount" roster={roster(7)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('button', { name: /Second thread.*New messages/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /New to you.*1/ }))
    expect(screen.queryByRole('button', { name: /First thread/ })).toBeNull()
    expect(screen.getByRole('heading', { name: 'First thread' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message First thread…')).toHaveValue('draft stays')
    view.unmount()
    const remount = render(<TeamViewWithBoard runId="unread-remount" roster={roster(7)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('button', { name: /Second thread.*New messages/ })).toBeInTheDocument()
    remount.rerender(<TeamViewWithBoard runId="unread-new-run" roster={roster(7)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.queryByRole('button', { name: /Second thread.*New messages/ })).toBeNull()
  })

  test('an own send does not clear an older incoming marker that the log has not rendered', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-unread-send', title: 'QA hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const roster = (seq: number) => enabledRoster({ threads: [thread], summaries: { [thread.id]: {
      last_other_message_seq: seq, title_seq: 0,
    } } })
    const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'own', seq: 5 })
    const view = render(<TeamViewWithBoard runId="unread-own-send" roster={roster(1)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: 'People' }))
    view.rerender(<TeamViewWithBoard runId="unread-own-send" roster={roster(4)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: 'Conversations' }))
    fireEvent.change(screen.getByPlaceholderText('Message QA hand-off…'), { target: { value: 'my follow-up' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(send).toHaveBeenCalled())
    await waitFor(() => expect(screen.getByRole('button', { name: /QA hand-off.*New messages/ })).toHaveTextContent('Message sent'))
    // A lagging poll must not replace the latest incoming seq remembered beside the local receipt.
    view.rerender(<TeamViewWithBoard runId="unread-own-send" roster={roster(2)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('button', { name: /QA hand-off.*New messages/ })).toBeInTheDocument()
    view.rerender(<TeamViewWithBoard runId="unread-own-send" roster={enabledRoster({ threads: [thread], summaries: { [thread.id]: {
      last_message: { id: 'own', seq: 5, sender_id: 'user', preview: 'my follow-up', created_at: '' },
      last_other_message_seq: 2, title_seq: 0,
    } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('button', { name: /QA hand-off.*New messages/ })).toBeInTheDocument()
  })

  test('narrow Team starts with the list, then Back restores its scroll and keeps the same conversation', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const roster = screen.getByRole('navigation', { name: 'Team roster' })
    expect(layout).toHaveClass('drawer')
    expect(layout).not.toHaveClass('mobile-detail')
    roster.scrollTop = 177
    const row = within(roster).getByRole('button', { name: /Release/ })
    fireEvent.click(row)
    expect(layout).toHaveClass('mobile-detail')
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
    const composer = screen.getByPlaceholderText('Message Release…')
    expect(composer).toHaveFocus()
    fireEvent.change(composer, { target: { value: 'draft on mobile' } })
    const log = screen.getByRole('log', { name: 'Messages' })
    log.scrollTop = 90
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(layout).not.toHaveClass('mobile-detail')
    expect(roster.scrollTop).toBe(177)
    expect(row).toHaveFocus()
    fireEvent.click(row)
    expect(screen.getByPlaceholderText('Message Release…')).toBe(composer)
    expect(composer).toHaveValue('draft on mobile')
    expect(screen.getByRole('log', { name: 'Messages' })).toBe(log)
    expect(log.scrollTop).toBe(90)
  })

  test('narrow closed detail explains why its saved draft cannot be sent', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-narrow-closed', title: 'Archive', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Archive/ }))
    const composer = screen.getByPlaceholderText('Message Archive…')
    fireEvent.change(composer, { target: { value: 'saved draft' } })
    composer.focus()
    expect(composer).toHaveFocus()
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...thread, status: 'closed' }] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(view.container.querySelector('.team-view')).toHaveClass('mobile-detail')
    expect(screen.getByRole('status')).toHaveTextContent('This conversation is closed')
    expect(screen.getByRole('status')).toHaveFocus()
    expect(composer).toBeDisabled()
    expect(composer).toHaveValue('saved draft')
  })

  test('opening a closed conversation on narrow screens focuses Back', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-closed-focus', title: 'Archive', scope: 'run', members: ['user', 'run:qa'], status: 'closed' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Archive/ }))
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toHaveFocus()
  })

  test('read-only conversation focuses visible detail and Back restores the roster row', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(390)
    const thread: meshClient.MeshThread = {
      id: 'mt-agents', title: 'Agent exchange', scope: 'run',
      members: ['run:qa', 'stage:implement'], status: 'open',
    }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const roster = screen.getByRole('navigation', { name: 'Team roster' })
    const row = within(roster).getByRole('button', { name: /Agent exchange/ })
    row.focus()
    fireEvent.click(row)

    expect(layout).toHaveClass('drawer', 'mobile-detail')
    expect(screen.getByRole('status')).toHaveTextContent('The agents are talking between themselves')
    expect(screen.queryByRole('textbox', { name: /Message Agent exchange/ })).toBeNull()
    const back = screen.getByRole('button', { name: 'Back to conversations' })
    expect(back).toHaveFocus()

    fireEvent.click(back)
    expect(layout).not.toHaveClass('mobile-detail')
    expect(row).toHaveFocus()
    fireEvent.click(row)
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toHaveFocus()
  })

  test('desktop to narrow and back keeps a focused draft and reading position', () => {
    mockThreadAndBoardFetches()
    const width = mockTeamWidth(1200)
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const composer = screen.getByPlaceholderText('Message Release…')
    fireEvent.change(composer, { target: { value: 'keep me' } })
    composer.focus()
    const log = screen.getByRole('log', { name: 'Messages' })
    log.scrollTop = 101
    width.resize(390)
    expect(layout).toHaveClass('drawer', 'mobile-detail')
    expect(composer).toHaveFocus()
    width.resize(1200)
    expect(layout).not.toHaveClass('drawer')
    expect(screen.getByPlaceholderText('Message Release…')).toBe(composer)
    expect(composer).toHaveValue('keep me')
    expect(log.scrollTop).toBe(101)
  })

  test('wide roster focus restores the narrow list after previously showing detail', () => {
    mockThreadAndBoardFetches()
    const width = mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const roster = screen.getByRole('navigation', { name: 'Team roster' })
    const row = within(roster).getByRole('button', { name: /Release/ })
    roster.scrollTop = 140
    fireEvent.click(row)
    expect(layout).toHaveClass('drawer', 'mobile-detail')
    const composer = screen.getByPlaceholderText('Message Release…')
    fireEvent.change(composer, { target: { value: 'keep this draft' } })
    const log = screen.getByRole('log', { name: 'Messages' })
    log.scrollTop = 80

    width.resize(1200)
    act(() => row.focus())
    expect(row).toHaveFocus()
    width.resize(390)
    expect(layout).toHaveClass('drawer')
    expect(layout).not.toHaveClass('mobile-detail')
    expect(roster).toHaveClass('open')
    expect(row).toHaveFocus()
    expect(roster.scrollTop).toBe(140)
    expect(screen.getByPlaceholderText('Message Release…')).toBe(composer)
    expect(composer).toHaveValue('keep this draft')
    expect(log).toBeInTheDocument()
    expect(log.scrollTop).toBe(80)
  })

  test('a hidden Team keeps the narrow list the reader left while the window is resized', () => {
    mockThreadAndBoardFetches()
    const width = mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-hidden-resize', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const roster = enabledRoster({ threads: [thread] })
    const view = render(<TeamViewWithBoard runId="hidden-resize" active roster={roster} stages={[makeStage()]} events={[]} profiles={profiles} />)
    const layout = view.container.querySelector('.team-view') as HTMLElement
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(layout).not.toHaveClass('mobile-detail')

    // Another view is shown; the window is widened and narrowed again meanwhile.
    view.rerender(<TeamViewWithBoard runId="hidden-resize" active={false} roster={roster} stages={[makeStage()]} events={[]} profiles={profiles} />)
    act(() => (document.activeElement as HTMLElement | null)?.blur())
    width.resize(1200)
    width.resize(390)
    view.rerender(<TeamViewWithBoard runId="hidden-resize" active roster={roster} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(layout).toHaveClass('drawer')
    expect(layout).not.toHaveClass('mobile-detail')
  })

  test('wide detail focus keeps the narrow conversation after previously showing detail', () => {
    mockThreadAndBoardFetches()
    const width = mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    const layout = view.container.querySelector('.team-view') as HTMLElement
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ }))
    const composer = screen.getByPlaceholderText('Message Release…')
    width.resize(1200)
    act(() => composer.focus())
    width.resize(390)
    expect(layout).toHaveClass('drawer', 'mobile-detail')
    expect(composer).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
  })

  test.each(['Board', 'Activity'])('narrowing keeps the %s panel visible without a selected conversation', (tabName) => {
    mockThreadAndBoardFetches()
    const width = mockTeamWidth(1200)
    const view = renderTeamView()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const tab = screen.getByRole('tab', { name: new RegExp(tabName) })
    fireEvent.click(tab)
    expect(tab).toHaveAttribute('aria-selected', 'true')
    const panel = screen.getByRole('tabpanel', { name: new RegExp(tabName) })

    width.resize(390)
    expect(layout).toHaveClass('drawer', 'mobile-detail')
    expect(screen.getByRole('tabpanel', { name: new RegExp(tabName) })).toBe(panel)
    expect(screen.getByRole('navigation', { name: 'Team roster' })).not.toHaveClass('open')
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
  })

  test('chooses the newest once after first loaded roster, then preserves row focus, selection and draft on reorder', async () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(1200)
    const older: meshClient.MeshThread = { id: 'mt-old', title: 'Older', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const newer: meshClient.MeshThread = { id: 'mt-new', title: 'Newer', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    const summary = (seq: number): meshClient.MeshThreadSummary => ({ last_message: {
      id: `m${seq}`, seq, created_at: '2026-10-21T11:00:00Z', sender_id: 'run:qa', preview: `Message ${seq}`,
    }, last_other_message_seq: seq, title_seq: 0 })
    const view = renderTeamView([makeStage()], [], enabledRoster({ probed: false }))
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [older, newer], summaries: { 'mt-old': summary(1), 'mt-new': summary(2) } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('heading', { name: 'Newer' })).toBeInTheDocument()
    const nav = screen.getByRole('navigation', { name: 'Team roster' })
    const rows = () => within(nav).getAllByRole('button').filter((button) => button.classList.contains('team-thread-item'))
    expect(rows().map((row) => row.textContent)).toEqual([expect.stringContaining('Newer'), expect.stringContaining('Older')])
    expect(rows()[0]).toHaveTextContent('QA reviewer: Message 2')
    const focusedRow = rows()[0]!
    focusedRow.focus()
    fireEvent.change(screen.getByPlaceholderText('Message Newer…'), { target: { value: 'keep this draft' } })
    const composer = screen.getByPlaceholderText('Message Newer…')
    const conversationLog = screen.getByRole('log', { name: 'Messages' })
    conversationLog.scrollTop = 140
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [older, newer], summaries: { 'mt-old': summary(3), 'mt-new': summary(2) } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(rows().map((row) => row.textContent)).toEqual([expect.stringContaining('Older'), expect.stringContaining('Newer')])
    expect(rows()[0]).toHaveTextContent('QA reviewer: Message 3')
    expect(focusedRow).toHaveFocus()
    expect(screen.getByRole('heading', { name: 'Newer' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Newer…')).toBe(composer)
    expect(composer).toHaveValue('keep this draft')
    expect(screen.getByRole('log', { name: 'Messages' })).toBe(conversationLog)
    expect(conversationLog.scrollTop).toBe(140)
  })

  test('moves an own send on its receipt, then replaces safe provisional fields with saved and roster fields', async () => {
    mockThreadAndBoardFetches()
    const first: meshClient.MeshThread = { id: 'mt-first', title: 'First', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const second: meshClient.MeshThread = { id: 'mt-second', title: 'Second', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    const summary = (seq: number, preview: string, createdAt = '2026-10-21T10:00:00Z'): meshClient.MeshThreadSummary => ({
      last_message: { id: `m${seq}`, seq, created_at: createdAt, sender_id: 'run:qa', preview }, last_other_message_seq: seq, title_seq: 0,
    })
    const stale = enabledRoster({ threads: [first, second], summaries: { 'mt-first': summary(2, 'Old'), 'mt-second': summary(5, 'Other') } })
    let resolveRead!: (page: meshClient.MeshMessagePage) => void
    vi.mocked(meshClient.getMeshThread).mockImplementation((_id, cursor) => cursor === 9
      ? new Promise((resolve) => { resolveRead = resolve })
      : Promise.resolve({ messages: [], next_cursor: 0 }))
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm10', seq: 10 })
    const view = renderTeamView([makeStage()], [], stale)
    const rows = () => within(screen.getByRole('navigation', { name: 'Team roster' })).getAllByRole('button').filter((button) => button.classList.contains('team-thread-item'))
    fireEvent.click(rows()[1]!)
    const composer = screen.getByPlaceholderText('Message First…')
    fireEvent.change(composer, { target: { value: 'secret submitted body' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(rows()[0]).toHaveTextContent('First'))
    expect(rows()[0]).toHaveTextContent('You: Message sent')
    expect(rows()[0]).not.toHaveTextContent('secret submitted body')
    expect(meshClient.getMeshThread).toHaveBeenCalledWith('mt-first', 9)
    expect(screen.getByPlaceholderText('Message First…')).toBe(composer)
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={stale} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(rows()[0]).toHaveTextContent('Message sent')

    await act(async () => resolveRead({ messages: [{
      id: 'm10', thread_id: 'mt-first', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message',
      body: 'saved [REDACTED]\n  body', seq: 10, created_at: '2026-10-21T09:00:00Z',
    }], next_cursor: 10 }))
    expect(rows()[0]).toHaveTextContent('You: saved [REDACTED] body')
    expect(within(rows()[0]!).getByRole('time')).toHaveAttribute('datetime', '2026-10-21T09:00:00Z')
    const durable = enabledRoster({ threads: [first, second], summaries: { 'mt-first': summary(10, 'Roster redacted'), 'mt-second': summary(5, 'Other') } })
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={durable} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(rows()[0]).toHaveTextContent('Roster redacted')
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={stale} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(rows()[0]).toHaveTextContent('Roster redacted')
    expect(sendSpy).toHaveBeenCalledTimes(1)
  })

  test('a failed follow-up read leaves the committed send visible and does not retry it', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    vi.mocked(meshClient.getMeshThread).mockImplementation((_id, cursor) => cursor === 6
      ? Promise.reject(new Error('read failed'))
      : Promise.resolve({ messages: [], next_cursor: 0 }))
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm7', seq: 7 })
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ }))
    fireEvent.change(screen.getByPlaceholderText('Message Release…'), { target: { value: 'private message' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    const row = within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ })
    await waitFor(() => expect(row).toHaveTextContent('You: Message sent'))
    expect(row).not.toHaveTextContent('private message')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(sendSpy).toHaveBeenCalledTimes(1)
  })

  test('a late receipt read cannot overwrite a newer roster message', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    let resolveRead!: (page: meshClient.MeshMessagePage) => void
    vi.mocked(meshClient.getMeshThread).mockImplementation((_id, cursor) => cursor === 9
      ? new Promise((resolve) => { resolveRead = resolve })
      : Promise.resolve({ messages: [], next_cursor: 0 }))
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm10', seq: 10 })
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ }))
    fireEvent.change(screen.getByPlaceholderText('Message Release…'), { target: { value: 'submitted' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    const row = within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Release/ })
    await waitFor(() => expect(row).toHaveTextContent('Message sent'))

    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [thread], summaries: { 'mt-1': {
      last_message: { id: 'm12', seq: 12, created_at: '2026-10-21T12:00:00Z', sender_id: 'run:qa', preview: 'Newer reply' },
      last_other_message_seq: 12, title_seq: 0,
    } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    await act(async () => resolveRead({ messages: [{
      id: 'm10', thread_id: 'mt-1', sender_id: 'user', recipient_ids: ['run:qa'], kind: 'message',
      body: 'Old saved body', seq: 10, created_at: '2026-10-21T11:00:00Z',
    }], next_cursor: 10 }))
    expect(row).toHaveTextContent('QA reviewer: Newer reply')
    expect(row).not.toHaveTextContent('Old saved body')
  })

  test('does not auto-select after user interaction before roster data arrives, or on a narrow first load', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const pending = renderTeamView([makeStage()], [], enabledRoster({ probed: false }))
    fireEvent.click(screen.getByRole('tab', { name: 'People' }))
    pending.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [thread] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('heading', { name: 'Team' })).toBeInTheDocument()
    pending.unmount()

    mockTeamWidth(341)
    const narrow = renderTeamView([makeStage()], [], enabledRoster({ probed: false }))
    narrow.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [thread] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(narrow.container.querySelector('.team-view')).not.toHaveClass('mobile-detail')
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release')).toBeInTheDocument()
  })

  test('updates selected thread metadata from roster without remounting its conversation', async () => {
    mockThreadAndBoardFetches()
    const oldThread: meshClient.MeshThread = { id: 'mt-1', title: 'Old topic', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [oldThread] }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Old topic'))
    const composer = screen.getByPlaceholderText('Message Old topic…')
    fireEvent.change(composer, { target: { value: 'draft' } })
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...oldThread, title: 'Fresh topic', status: 'closed' }] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('heading', { name: 'Fresh topic' })).toBeInTheDocument()
    expect(screen.getByText('Closed')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Fresh topic…')).toBe(composer)
    expect(composer).toHaveValue('draft')
  })

  test('edits a topic using roster revision, keeps failed input, retries with one request ID, and refreshes without reordering messages', async () => {
    mockThreadAndBoardFetches()
    const first: meshClient.MeshThread = { id: 'mt-first', title: 'Old topic', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const second: meshClient.MeshThread = { id: 'mt-second', title: 'Other', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    const summary = (seq: number, titleSeq: number): meshClient.MeshThreadSummary => ({
      last_message: { id: `m${seq}`, seq, created_at: '', sender_id: 'run:qa', preview: 'Last message' },
      last_other_message_seq: seq, title_seq: titleSeq,
    })
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle')
      .mockRejectedValueOnce(new Error('Connection lost'))
      .mockResolvedValueOnce({ title: 'Fresh topic', title_seq: 9 })
    const roster = enabledRoster({ threads: [first, second], summaries: { 'mt-first': summary(2, 7), 'mt-second': summary(5, 0) } })
    const view = renderTeamView([makeStage()], [], roster)
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Old topic/ }))
    const composer = screen.getByPlaceholderText('Message Old topic…')
    fireEvent.change(composer, { target: { value: 'Keep this draft' } })
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    const input = screen.getByRole('textbox', { name: 'Conversation topic' })
    expect(input).toHaveFocus()
    fireEvent.change(input, { target: { value: 'Fresh topic' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Connection lost'))
    expect(input).toHaveValue('Fresh topic')
    expect(screen.getByRole('heading', { name: 'Old topic' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Conversation topic' })).toBeNull())
    expect(screen.getByRole('button', { name: 'Edit topic' })).toHaveFocus()
    expect(edit).toHaveBeenCalledTimes(2)
    expect(edit.mock.calls[0]).toEqual(edit.mock.calls[1])
    expect(edit.mock.calls[0]?.[1]).toMatchObject({ title: 'Fresh topic', expectedTitleSeq: 7 })

    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...first, title: 'Fresh topic' }, second], summaries: {
      'mt-first': summary(2, 9), 'mt-second': summary(5, 0),
    } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    const rows = within(screen.getByRole('navigation', { name: 'Team roster' })).getAllByRole('button')
      .filter((button) => button.classList.contains('team-thread-item'))
    expect(rows.map((row) => row.textContent)).toEqual([expect.stringContaining('Other'), expect.stringContaining('Fresh topic')])
    expect(screen.getByRole('heading', { name: 'Fresh topic' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Fresh topic…')).toBe(composer)
    expect(composer).toHaveValue('Keep this draft')
  })

  test('clears a closed thread topic after fresh roster', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-closed', title: 'Archive', scope: 'run', members: ['user', 'run:qa'], status: 'closed' }
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle').mockResolvedValue({ title: '', title_seq: 13 })
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread], summaries: { 'mt-closed': {
      last_other_message_seq: 0, title_seq: 11,
    } } }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.click(screen.getByRole('button', { name: 'Clear topic' }))
    await waitFor(() => expect(edit).toHaveBeenCalledWith('mt-closed', expect.objectContaining({ title: '', expectedTitleSeq: 11 })))
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...thread, title: '' }], summaries: { 'mt-closed': {
      last_other_message_seq: 0, title_seq: 13,
    } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /QA reviewer/ })).toBeInTheDocument()
  })

  test('shows a recoverable topic conflict, preserving the draft and using the next roster revision', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Old', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle')
      .mockRejectedValueOnce(new meshClient.MeshApiError('/api/mesh/threads/mt-1/topic', 409, 'operation_conflict'))
      .mockResolvedValueOnce({ title: 'Mine', title_seq: 12 })
    const view = renderTeamView([makeStage()], [], enabledRoster({ threads: [thread], summaries: { 'mt-1': {
      last_other_message_seq: 0, title_seq: 8,
    } } }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Topic changed elsewhere'))
    expect(screen.getByRole('textbox', { name: 'Conversation topic' })).toHaveValue('Mine')
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...thread, title: 'Theirs' }], summaries: { 'mt-1': {
      last_other_message_seq: 0, title_seq: 10,
    } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('textbox', { name: 'Conversation topic' })).toHaveValue('Mine')
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(edit).toHaveBeenCalledTimes(2))
    expect(edit.mock.calls[0]?.[1]).toMatchObject({ expectedTitleSeq: 8 })
    expect(edit.mock.calls[1]?.[1]).toMatchObject({ expectedTitleSeq: 10 })
    expect(edit.mock.calls[1]?.[1].requestId).not.toBe(edit.mock.calls[0]?.[1].requestId)
  })

  test('a topic conflict waits for a newer roster revision before showing the current topic or retrying', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-topic-wait', title: 'Original', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle')
      .mockRejectedValueOnce(new meshClient.MeshApiError('/api/mesh/threads/mt-topic-wait/topic', 409, 'operation_conflict'))
      .mockResolvedValueOnce({ title: 'Mine', title_seq: 11 })
    const roster = (title: string, seq: number) => enabledRoster({ threads: [{ ...thread, title }], summaries: {
      [thread.id]: { last_other_message_seq: 0, title_seq: seq },
    } })
    const view = render(<TeamViewWithBoard runId="topic-wait" roster={roster('Original', 8)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Topic changed elsewhere'))
    expect(screen.queryByText(/Current topic:/)).toBeNull()
    expect(screen.getByText(/Waiting for the latest topic/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save topic' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Clear topic' })).toBeDisabled()
    view.rerender(<TeamViewWithBoard runId="topic-wait" roster={roster('Original', 8)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    expect(edit).toHaveBeenCalledTimes(1)
    view.rerender(<TeamViewWithBoard runId="topic-wait" roster={roster('Theirs', 10)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByText('Current topic: Theirs')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save topic' })).toBeEnabled()
  })

  test('a rebased topic retry keeps its reviewed revision and request ID after a lost response', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-topic-rebase-lost', title: 'Original', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    let durableEdits = 0
    let committedRequestId = ''
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle').mockImplementation(async (_id, input) => {
      if (input.expectedTitleSeq === 8) throw new meshClient.MeshApiError('/api/mesh/threads/mt-topic-rebase-lost/topic', 409, 'operation_conflict')
      if (input.requestId !== committedRequestId) {
        committedRequestId = input.requestId
        durableEdits++
        throw new Error('Connection lost')
      }
      return { title: input.title, title_seq: 11 }
    })
    const roster = (title: string, seq: number) => enabledRoster({ threads: [{ ...thread, title }], summaries: {
      [thread.id]: { last_other_message_seq: 0, title_seq: seq },
    } })
    const view = render(<TeamViewWithBoard runId="topic-rebase-lost" roster={roster('Original', 8)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Topic changed elsewhere'))
    view.rerender(<TeamViewWithBoard runId="topic-rebase-lost" roster={roster('Theirs', 10)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Connection lost'))
    view.rerender(<TeamViewWithBoard runId="topic-rebase-lost" roster={roster('Someone else', 12)} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(edit).toHaveBeenCalledTimes(3))
    expect(edit.mock.calls[2]).toEqual(edit.mock.calls[1])
    expect(edit.mock.calls[1]?.[1]).toMatchObject({ title: 'Mine', expectedTitleSeq: 10 })
    expect(durableEdits).toBe(1)
  })

  test.each([false, true])('New conversation closes topic editing and names the form context (narrow=%s)', (narrow) => {
    mockThreadAndBoardFetches()
    if (narrow) mockTeamWidth(390)
    const thread: meshClient.MeshThread = { id: `mt-topic-create-${narrow}`, title: 'Review', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    if (narrow) fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Review/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Unsent topic edit' } })
    if (narrow) fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    fireEvent.click(screen.getByRole('button', { name: 'New conversation' }))
    expect(screen.getByRole('heading', { name: 'New conversation' })).toBeInTheDocument()
    expect(screen.getByText('Choose a topic and participants')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Conversation topic' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Edit topic' })).toBeNull()
    expect(screen.getByPlaceholderText('What is this about?')).toHaveFocus()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    if (narrow) fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /Review/ }))
    expect(screen.getByRole('heading', { name: 'Review' })).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Conversation topic' })).toBeNull()
  })

  test('topic edit keeps its opening revision when roster changes before first Save', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-topic-stale', title: 'Original', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle').mockRejectedValue(new meshClient.MeshApiError('/api/mesh/threads/mt-topic-stale/topic', 409, 'operation_conflict'))
    const view = render(<TeamViewWithBoard runId="topic-stale" roster={enabledRoster({ threads: [thread], summaries: { [thread.id]: { last_other_message_seq: 0, title_seq: 4 } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Mine' } })
    view.rerender(<TeamViewWithBoard runId="topic-stale" roster={enabledRoster({ threads: [{ ...thread, title: 'Theirs' }], summaries: { [thread.id]: { last_other_message_seq: 0, title_seq: 5 } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(edit).toHaveBeenCalled())
    expect(edit.mock.calls[0]?.[1].expectedTitleSeq).toBe(4)
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Topic changed elsewhere'))
    expect(screen.getByRole('textbox', { name: 'Conversation topic' })).toHaveValue('Mine')
    expect(screen.getByRole('heading', { name: 'Theirs' })).toBeInTheDocument()
  })

  test('lost topic response retries the committed intent after another roster edit', async () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-topic-lost', title: 'Original', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    let durableEdits = 0
    const receipts = new Map<string, meshClient.MeshThreadTitleReceipt>()
    const edit = vi.spyOn(meshClient, 'updateMeshThreadTitle').mockImplementation(async (_id, input) => {
      let receipt = receipts.get(input.requestId)
      if (!receipt) {
        durableEdits++
        receipt = { title: input.title, title_seq: 5 }
        receipts.set(input.requestId, receipt)
        throw new Error('Connection lost')
      }
      return receipt
    })
    const view = render(<TeamViewWithBoard runId="topic-lost" roster={enabledRoster({ threads: [thread], summaries: { [thread.id]: { last_other_message_seq: 0, title_seq: 4 } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Connection lost'))
    view.rerender(<TeamViewWithBoard runId="topic-lost" roster={enabledRoster({ threads: [{ ...thread, title: 'Another edit' }], summaries: { [thread.id]: { last_other_message_seq: 0, title_seq: 6 } } })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    await waitFor(() => expect(edit).toHaveBeenCalledTimes(2))
    expect(edit.mock.calls[1]).toEqual(edit.mock.calls[0])
    expect(durableEdits).toBe(1)
  })

  test('a late topic reply does not close the editor for the newly selected thread', async () => {
    mockThreadAndBoardFetches()
    const threads: meshClient.MeshThread[] = [
      { id: 'mt-a', title: 'A', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
      { id: 'mt-b', title: 'B', scope: 'run', members: ['user', 'stage:implement'], status: 'open' },
    ]
    let resolveEdit!: (receipt: meshClient.MeshThreadTitleReceipt) => void
    vi.spyOn(meshClient, 'updateMeshThreadTitle').mockImplementation(() => new Promise((resolve) => { resolveEdit = resolve }))
    renderTeamView([makeStage()], [], enabledRoster({ threads }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /^A/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'A changed' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save topic' }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByRole('button', { name: /^B/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit topic' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Conversation topic' }), { target: { value: 'B changed' } })
    await act(async () => resolveEdit({ title: 'A changed', title_seq: 7 }))
    expect(screen.getByRole('textbox', { name: 'Conversation topic' })).toHaveValue('B changed')
  })

  test('keeps a locally created conversation until the roster catches up, then uses its fresh metadata', async () => {
    mockThreadAndBoardFetches()
    const created: meshClient.MeshThread = { id: 'mt-local', title: 'Local topic', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(created)
    const view = renderTeamView()
    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Local topic' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Local topic' })).toBeInTheDocument())
    const composer = screen.getByPlaceholderText('Message Local topic…')
    fireEvent.change(composer, { target: { value: 'waiting on roster' } })
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster()} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Local topic')).toBeInTheDocument()
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [{ ...created, title: 'Durable topic' }] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('heading', { name: 'Durable topic' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Durable topic…')).toBe(composer)
    expect(composer).toHaveValue('waiting on roster')
  })

  test('opens Conversations first, switches to People with the keyboard, and keeps a selected draft', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const manyAgents: meshClient.MeshParticipant[] = Array.from({ length: 12 }, (_, i) => ({
      id: `run:agent-${i}`, kind: 'run_agent', scope: 'run', display_name: `Agent ${i}`, reachable: true,
    }))
    renderTeamView([makeStage()], [], enabledRoster({ participants: [...participants, ...manyAgents], threads: [thread] }))

    const navigation = screen.getByRole('navigation', { name: 'Team roster' })
    expect(within(navigation).getByRole('tab', { name: /Conversations/ })).toHaveAttribute('aria-selected', 'true')
    expect(within(navigation).getByText('Release')).toBeInTheDocument()
    expect(within(navigation).queryByText('Agent 0')).toBeNull()
    fireEvent.click(within(navigation).getByText('Release'))
    fireEvent.change(screen.getByPlaceholderText('Message Release…'), { target: { value: 'draft' } })

    const conversations = within(navigation).getByRole('tab', { name: /Conversations/ })
    conversations.focus()
    fireEvent.keyDown(conversations, { key: 'ArrowRight' })
    expect(within(navigation).getByRole('tab', { name: /People/ })).toHaveFocus()
    expect(within(navigation).getByText('Agent 0')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Release' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Release…')).toHaveValue('draft')
    fireEvent.keyDown(within(navigation).getByRole('tab', { name: /People/ }), { key: 'ArrowLeft' })
    expect(within(navigation).getByText('Release')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Release…')).toHaveValue('draft')
  })

  test('searches the current list by thread preview or participant handle without changing selection', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({
      participants: participants.map((p) => p.id === 'run:qa' ? { ...p, alias: 'reviewer' } : p),
      threads: [thread], summaries: { 'mt-1': { last_message: { id: 'm1', seq: 1, created_at: '', sender_id: 'run:qa', preview: 'Ship checklist' }, last_other_message_seq: 1, title_seq: 0 } },
    }))
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release'))
    const search = screen.getByRole('searchbox', { name: 'Search conversations' })
    search.focus()
    fireEvent.change(search, { target: { value: 'QA reviewer' } })
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release')).toBeInTheDocument()
    fireEvent.change(search, { target: { value: '@reviewer' } })
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release')).toBeInTheDocument()
    fireEvent.change(search, { target: { value: 'ship checklist' } })
    expect(search).toHaveFocus()
    expect(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release')).toBeInTheDocument()
    fireEvent.change(search, { target: { value: 'missing' } })
    expect(screen.getByText('No matching conversations')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Release' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: /People/ }))
    const peopleSearch = screen.getByRole('searchbox', { name: 'Search people' })
    fireEvent.change(peopleSearch, { target: { value: 'reviewer' } })
    expect(screen.getByText('@reviewer')).toBeInTheDocument()
    expect(screen.queryByText('Implement agent')).toBeNull()
  })

  test('offers the current list action and returns focus after cancelling creation', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const newConversation = screen.getByRole('button', { name: 'New conversation' })
    expect(screen.getByRole('button', { name: 'Browse people' })).toBeInTheDocument()
    fireEvent.click(newConversation)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(newConversation).toHaveFocus()
    fireEvent.click(screen.getByRole('button', { name: 'Browse people' }))
    expect(screen.getByRole('tab', { name: 'People' })).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Add run agent' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New conversation' })).toBeNull()
  })

  test.each([
    { layout: 'wide', width: null },
    { layout: 'narrow drawer', width: 341 },
  ])('returns focus to the persistent list action when the empty-state opener disappears in the $layout layout', async ({ width }) => {
    mockThreadAndBoardFetches()
    if (width !== null) mockTeamWidth(width)
    const view = renderTeamView()
    await act(async () => {})

    const emptyStateAction = screen.getByRole('button', { name: 'Start a conversation' })
    fireEvent.click(emptyStateAction)
    const thread: meshClient.MeshThread = { id: 'mt-new', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ threads: [thread] })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(emptyStateAction.isConnected).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    if (width !== null) expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
    expect(screen.getByRole('button', { name: 'New conversation' })).toHaveFocus()
  })

  test('returns focus to the list action after cancelling in the narrow drawer', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(341)
    renderTeamView()
    fireEvent.click(screen.getByRole('button', { name: 'New conversation' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
    expect(screen.getByRole('button', { name: 'New conversation' })).toHaveFocus()
    fireEvent.click(screen.getByRole('tab', { name: 'People' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Add run agent' })).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
    expect(screen.getByRole('button', { name: 'Add run agent' })).toHaveFocus()
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
    expect(within(peopleRegion('This run')).getAllByText('Side agent').length).toBeGreaterThan(0)
    showConversations()
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

  test('Conversations is the initial list and People keeps the run and stage groups', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    expect(screen.getByRole('region', { name: 'Conversations' })).toBeInTheDocument()
    expect(peopleRegion('This run')).toBeInTheDocument()
    expect(within(peopleRegion('Stages')).getByText('Implement agent')).toBeInTheDocument()
  })

  test('the conversation header names the selected person and what they are', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    expect(screen.getByRole('heading', { name: 'Team' })).toBeInTheDocument()
    pickRunPerson(/^QA reviewer/)
    expect(await screen.findByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.getByText('Direct conversation · Run agent')).toBeInTheDocument()
  })

  test('a thread header counts its participants', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Hand-off'))
    expect(screen.getByRole('heading', { name: 'Hand-off' })).toBeInTheDocument()
    expect(screen.getByText('3 participants · you and 2 agents · QA reviewer, Implement agent')).toBeInTheDocument()
  })

  test('a conversation agents started between themselves does not count you in it', () => {
    mockThreadAndBoardFetches()
    const thread: meshClient.MeshThread = { id: 'mt-8', title: 'Pairing', scope: 'run', members: ['run:qa', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Pairing'))
    expect(screen.getByText('2 participants · 2 agents · QA reviewer, Implement agent')).toBeInTheDocument()
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
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false,
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

  test('the Board tab shows the tasks as coloured cards in one grid, next to the facts', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false,
      entries: [
        { key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1, seq: 1, ts: '' },
        { key: 'task/export', status: 'CSV export', author_id: 'user', version: 1, seq: 2, ts: '' },
        { key: 'task/export/implement', status: 'in progress: wiring', author_id: 'stage:implement', version: 1, seq: 3, ts: '' },
        { key: 'api/ready', status: 'yes', author_id: 'stage:implement', version: 1, seq: 4, ts: '' },
      ],
    })
    renderTeamView()
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(await screen.findByRole('button', { name: 'Health endpoint, Todo' })).toHaveAttribute('data-status', 'todo')
    expect(screen.getByRole('button', { name: 'CSV export, In progress' })).toHaveAttribute('data-status', 'in_progress')
    expect(screen.getByRole('article', { name: 'api/ready' })).toBeInTheDocument()
    expect(document.querySelectorAll('.team-board-list')).toHaveLength(1)
    expect(screen.getByRole('tab', { name: 'Messages' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Activity' })).toBeInTheDocument()
  })

  // The board poll is owned by App and passed in: TeamView only shows what it is given.
  test('the Board tab shows what the owner passes in, without a second board poll', async () => {
    mockThreadAndBoardFetches()
    const poll = vi.spyOn(meshClient, 'getMeshBoard')
    const board: MeshBoard = {
      tasks: [],
      typedTasks: false,
      entries: [{ key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1, seq: 1, ts: '' }],
      status: 'ready',
      reload: async () => {},
    }
    render(<TeamView runId="board-load" roster={enabledRoster()} onParticipantAdded={() => {}} board={board} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('tab', { name: /^Board/ })).toHaveTextContent('Board1')
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(await screen.findByRole('button', { name: 'Health endpoint, Todo' })).toBeInTheDocument()
    expect(poll).not.toHaveBeenCalled()
  })

  test('with typed tasks the Board tab counts the typed tasks, not the task/ storage entries', () => {
    mockThreadAndBoardFetches()
    const board: MeshBoard = {
      tasks: [typedTaskView({ key: 'task/a', title: 'A' }), typedTaskView({ key: 'task/b', title: 'B' }), typedTaskView({ key: 'task/c', title: 'C' })],
      typedTasks: true,
      entries: [
        { key: 'task/a', status: 'storage', author_id: 'user', version: 1, seq: 1, ts: '' },
        { key: 'api/ready', status: 'yes', author_id: 'stage:implement', version: 1, seq: 2, ts: '' },
      ],
      status: 'ready',
      reload: async () => {},
    }
    render(<TeamView runId="board-typed" roster={enabledRoster()} onParticipantAdded={() => {}} board={board} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByRole('tab', { name: /^Board/ })).toHaveTextContent('Board3')
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByRole('button', { name: 'A, Todo' })).toBeInTheDocument()
    expect(screen.getByRole('article', { name: 'api/ready' })).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: 'task/a' })).toBeNull()
  })

  test('the Board tab says it is loading, not that the board is empty, before the first fetch lands', () => {
    mockThreadAndBoardFetches()
    const board: MeshBoard = { entries: [], tasks: [], typedTasks: false, status: 'loading', reload: async () => {} }
    render(<TeamView runId="board-load" roster={enabledRoster()} onParticipantAdded={() => {}} board={board} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByRole('status')).toHaveTextContent('Loading board')
    expect(screen.queryByText(/Nothing on the board yet/)).toBeNull()
  })

  test('a first-load failure shows an error with Retry instead of an empty board', () => {
    mockThreadAndBoardFetches()
    const reload = vi.fn(async () => {})
    const board: MeshBoard = { entries: [], tasks: [], typedTasks: false, status: 'error', reload }
    render(<TeamView runId="board-load" roster={enabledRoster()} onParticipantAdded={() => {}} board={board} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByRole('alert')).toHaveTextContent('Board unavailable')
    expect(screen.queryByText(/Nothing on the board yet/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('a failed refresh keeps the last cards under a stale-data warning with Retry', () => {
    mockThreadAndBoardFetches()
    const reload = vi.fn(async () => {})
    const board: MeshBoard = {
      tasks: [],
      typedTasks: false,
      entries: [{ key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1, seq: 1, ts: '' }],
      status: 'stale',
      reload,
    }
    render(<TeamView runId="board-load" roster={enabledRoster()} onParticipantAdded={() => {}} board={board} stages={[makeStage()]} events={[]} profiles={profiles} />)
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByRole('alert')).toHaveTextContent('out of date')
    expect(screen.getByRole('button', { name: 'Health endpoint, Todo' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('a failed refresh and its recovery keep the task typed in the Board form', () => {
    mockThreadAndBoardFetches()
    const entries = [{ key: 'task/health', status: 'Health endpoint', author_id: 'user', version: 1, seq: 1, ts: '' }]
    const view = (status: MeshBoard['status']) => (
      <TeamView runId="board-load" roster={enabledRoster()} onParticipantAdded={() => {}} board={{ entries, tasks: [], typedTasks: false, status, reload: async () => {} }} stages={[makeStage()]} events={[]} profiles={profiles} />
    )
    const { rerender } = render(view('ready'))
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'CSV export' } })
    fireEvent.change(screen.getByLabelText('Details'), { target: { value: 'all rows' } })
    rerender(view('stale'))
    expect(screen.getByRole('alert')).toHaveTextContent('out of date')
    expect(screen.getByLabelText('Task')).toHaveValue('CSV export')
    rerender(view('ready'))
    expect(screen.getByLabelText('Task')).toHaveValue('CSV export')
    expect(screen.getByLabelText('Details')).toHaveValue('all rows')
  })

  test('a direct conversation opens from a person row', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    pickRunPerson(/^QA reviewer/)
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())
  })

  test('"Create conversation" requires at least one participant, even with no topic', () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    clickCreateConversation()
    clickCreateConversation()
    expect(screen.getByText('Choose at least one participant.')).toBeInTheDocument()
  })

  test('an empty topic opens an untitled conversation with participant names', async () => {
    mockThreadAndBoardFetches()
    const opened: meshClient.MeshThread = { id: 'mt-dm', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(opened)
    renderTeamView()
    clickCreateConversation()
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: '', memberIds: ['run:qa'] })))
    expect(screen.getByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument()
  })

  test('an untitled group still creates a distinct conversation', async () => {
    mockThreadAndBoardFetches()
    const existing: meshClient.MeshThread = { id: 'mt-dm', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const group: meshClient.MeshThread = { id: 'mt-group', scope: 'run', members: ['user', 'run:qa', 'stage:implement'], status: 'open' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(group)
    renderTeamView([makeStage()], [], enabledRoster({ threads: [existing] }))
    clickCreateConversation()
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    fireEvent.click(screen.getByText('Implement agent', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: '', memberIds: ['run:qa', 'stage:implement'] })))
    expect(screen.getByRole('heading', { name: 'QA reviewer, Implement agent' })).toBeInTheDocument()
  })

  test('repeated unnamed single-person creation returns the newest DM without opening another thread', async () => {
    mockThreadAndBoardFetches()
    const older: meshClient.MeshThread = { id: 'mt-old', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const newer: meshClient.MeshThread = { ...older, id: 'mt-new' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
    renderTeamView([makeStage()], [], enabledRoster({ threads: [older, newer], summaries: {
      'mt-old': { last_message: { id: 'm9', seq: 9, created_at: '', sender_id: 'run:qa', preview: '' }, last_other_message_seq: 9, title_seq: 0 },
      'mt-new': { last_message: { id: 'm12', seq: 12, created_at: '', sender_id: 'run:qa', preview: '' }, last_other_message_seq: 12, title_seq: 0 },
    } }))
    fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getAllByRole('button', { name: /QA reviewer/ })[1]!)
    clickCreateConversation()
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(screen.queryByPlaceholderText('What is this about?')).toBeNull())
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getAllByText('QA reviewer', { selector: '.team-thread-title' })[0]?.closest('button')).toHaveAttribute('aria-pressed', 'true')
    expect(openSpy).not.toHaveBeenCalled()
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument()
  })

  test('People selects the newest untitled DM while keeping a newer titled task conversation in the list', () => {
    mockThreadAndBoardFetches()
    const task: meshClient.MeshThread = { id: 'mt-task', title: 'Review task', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const oldDM: meshClient.MeshThread = { ...task, id: 'mt-old', title: '' }
    const newDM: meshClient.MeshThread = { ...task, id: 'mt-new', title: '' }
    const summary = (seq: number): meshClient.MeshThreadSummary => ({
      last_message: { id: `m${seq}`, seq, created_at: '', sender_id: 'run:qa', preview: '' }, last_other_message_seq: seq, title_seq: 0,
    })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
    renderTeamView([makeStage()], [], enabledRoster({ threads: [oldDM, task, newDM], summaries: {
      'mt-old': summary(10), 'mt-task': summary(30), 'mt-new': summary(20),
    } }))
    pickRunPerson(/^QA reviewer/)
    expect(screen.getByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getByRole('button', { name: /Review task/ })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getAllByText('QA reviewer', { selector: '.team-thread-title' })[0]?.closest('button')).toHaveAttribute('aria-pressed', 'true')
    expect(openSpy).not.toHaveBeenCalled()
  })

  test('"Create conversation" opens a new thread once a topic and a participant are chosen', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({
      id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open',
    })
    renderTeamView()
    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: 'Hand-off', scope: 'run', memberIds: ['run:qa'] })))
    await waitFor(() => expect(screen.getByPlaceholderText('Message Hand-off…')).toBeInTheDocument())
  })

  test('a new conversation cannot include a participant that can no longer answer (a finished stage)', () => {
    mockThreadAndBoardFetches()
    const done: meshClient.MeshParticipant = { id: 'stage:plan', kind: 'stage_agent', scope: 'stage', stage_id: 'plan', display_name: 'Plan agent', reachable: false }
    renderTeamView([makeStage(), makeStage({ id: 'plan', name: 'Plan', status: 'done' })], [], enabledRoster({ participants: [...participants, done] }))
    clickCreateConversation()
    const form = document.querySelector('.team-create-conversation-participants') as HTMLElement
    expect(within(form).getByText('QA reviewer')).toBeInTheDocument()
    expect(within(form).queryByText('Plan agent')).toBeNull()
  })

  test('"Add run agent" shows a read-only "Configured executor" with no model selector, and "Cancel" creates nothing', () => {
    mockThreadAndBoardFetches()
    const attachSpy = vi.spyOn(meshClient, 'attachAndSendMesh')
    renderTeamView()
    openAddRunAgent()

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
    showPeople()
    expect(screen.getByText('Paused')).toBeInTheDocument()
  })

  test('switching to a different conversation sends into the newly selected thread, not the previous one', async () => {
    mockThreadAndBoardFetches()
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const threadA: meshClient.MeshThread = { id: 'mt-a', title: 'With QA', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const threadB: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [threadA, threadB] }))

    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('With QA'))
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

    pickRunPerson(/^QA reviewer/)
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())

    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('please review')
  })

  test('re-selecting the same person after a successful first send returns to the opened thread instead of opening a second one', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({
      id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open',
    })
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    // roster.threads intentionally stays empty throughout — the 3s poll has not caught up with the thread
    // the send below opens, which is exactly the window the duplicate-thread bug lived in.
    renderTeamView()

    pickRunPerson(/^QA reviewer/)
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toBeInTheDocument())
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(1))

    // The thread now also shows under Conversations right away; click the PERSON row again.
    pickRunPerson(/^QA reviewer/)
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
    const runRegion = () => peopleRegion('This run')

    fireEvent.click(within(runRegion()).getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'first' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))

    // Leave and come back before any roster poll: the thread opened by the failed attempt must be reused.
    fireEvent.click(within(peopleRegion('Stages')).getByRole('button', { name: /^Implement agent/ }))
    fireEvent.click(within(runRegion()).getByRole('button', { name: /^QA reviewer/ }))
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'second' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenLastCalledWith('mt-9', expect.objectContaining({ body: 'second' })))
    expect(openSpy).toHaveBeenCalledTimes(1)
  })

  test('a failed first send keeps its draft when People reselects the opened thread before a roster poll', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-9', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage')
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce({ message_id: 'm1', seq: 1 })
    renderTeamView()

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('boom'))

    // The successful open is still local; the roster has not reported this thread yet.
    fireEvent.click(within(peopleRegion('Stages')).getByRole('button', { name: /^Implement agent/ }))
    pickRunPerson(/^QA reviewer/)
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toHaveValue('please review')

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(2))
    expect(sendSpy).toHaveBeenLastCalledWith('mt-9', expect.objectContaining({ body: 'please review' }))
    expect(openSpy).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toHaveValue(''))
  })

  test.each(['People', 'Conversations'])(
    'a committed direct send with a lost response retries once after reselecting through %s',
    async (via) => {
      mockThreadAndBoardFetches()
      const opened: meshClient.MeshThread = { id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
      const other: meshClient.MeshThread = { id: 'mt-b', title: 'Other', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
      const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(opened)
      const durable = new Map<string, { message_id: string; seq: number }>()
      let loseFirstResponse = true
      const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockImplementation(async (_threadId, input) => {
        let receipt = durable.get(input.requestId)
        if (receipt === undefined) {
          receipt = { message_id: `m${durable.size + 1}`, seq: durable.size + 1 }
          durable.set(input.requestId, receipt)
        }
        if (loseFirstResponse) {
          loseFirstResponse = false
          throw new Error('response lost')
        }
        return receipt
      })
      renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

      pickRunPerson(/^QA reviewer/)
      fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('response lost'))
      expect(durable.size).toBe(1)

      fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Other'))
      if (via === 'People') {
        pickRunPerson(/^QA reviewer/)
      } else {
        showConversations()
        fireEvent.click(within(screen.getByRole('region', { name: 'Conversations' })).getByRole('button', { name: /QA reviewer/ }))
      }
      expect(screen.getByPlaceholderText('Message QA reviewer…')).toHaveValue('please review')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(2))
      expect(sendSpy.mock.calls[1]?.[1].requestId).toBe(sendSpy.mock.calls[0]?.[1].requestId)
      expect(durable.size).toBe(1)
      expect(openSpy).toHaveBeenCalledTimes(1)
      await waitFor(() => expect(screen.getByPlaceholderText('Message QA reviewer…')).toHaveValue(''))

      // A later deliberate send of identical text is a new intent after the retry succeeded.
      fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(sendSpy).toHaveBeenCalledTimes(3))
      expect(sendSpy.mock.calls[2]?.[1].requestId).not.toBe(sendSpy.mock.calls[0]?.[1].requestId)
      expect(durable.size).toBe(2)
    },
  )

  test('a lost direct open response keeps its request id after switching away and back', async () => {
    mockThreadAndBoardFetches()
    const opened: meshClient.MeshThread = { id: 'mt-9', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce(opened)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    expect(sendSpy).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('With Implement agent'))
    pickRunPerson(/^QA reviewer/)
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('please review')
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-9', expect.objectContaining({ body: 'please review' })))
    expect(openSpy).toHaveBeenCalledTimes(2)
    expect(openSpy.mock.calls[1]?.[0].requestId).toBe(openSpy.mock.calls[0]?.[0].requestId)
    expect(screen.getByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
  })

  test('New conversation reuses a pending direct open after its response was lost', async () => {
    mockThreadAndBoardFetches()
    const opened: meshClient.MeshThread = { id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const durable = new Map<string, meshClient.MeshThread>()
    let loseFirstResponse = true
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockImplementation(async (input) => {
      if (!durable.has(input.requestId)) durable.set(input.requestId, opened)
      if (loseFirstResponse) {
        loseFirstResponse = false
        throw new Error('response lost')
      }
      return durable.get(input.requestId)!
    })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    renderTeamView()

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('response lost'))
    expect(sendSpy).not.toHaveBeenCalled()

    clickCreateConversation()
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()

    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(2))
    expect(openSpy.mock.calls[1]?.[0].requestId).toBe(openSpy.mock.calls[0]?.[0].requestId)
    expect(durable.size).toBe(1)
    expect(screen.getByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message QA reviewer…')).toHaveValue('please review')
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-9', expect.objectContaining({ body: 'please review' })))
  })

  test('a titled conversation stays in Conversations when People starts a separate unnamed DM', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
      .mockResolvedValueOnce({ id: 'mt-7', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
      .mockResolvedValueOnce({ id: 'mt-dm', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' })
    vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    renderTeamView()
    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Release' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Release')).toBeInTheDocument())

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(await screen.findByPlaceholderText('Message QA reviewer…'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(2))
    expect(openSpy).toHaveBeenLastCalledWith(expect.objectContaining({ title: '', memberIds: ['run:qa'] }))
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('Release')).toBeInTheDocument()
  })

  test('two topics with one person stay independently selectable beside their untitled DM', async () => {
    mockThreadAndBoardFetches()
    const first: meshClient.MeshThread = { id: 'mt-release', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const second: meshClient.MeshThread = { ...first, id: 'mt-verification', title: 'Verification' }
    const dm: meshClient.MeshThread = { ...first, id: 'mt-dm', title: '' }
    const openSpy = vi.spyOn(meshClient, 'openMeshThread')
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce(second)
      .mockResolvedValueOnce(dm)
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm1', seq: 1 })
    renderTeamView()

    for (const topic of ['Release', 'Verification']) {
      clickCreateConversation()
      fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: topic } })
      fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
      clickCreateConversation()
      await waitFor(() => expect(screen.getByRole('heading', { name: topic })).toBeInTheDocument())
    }
    expect(openSpy.mock.calls.slice(0, 2).map(([input]) => input.title)).toEqual(['Release', 'Verification'])

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'direct note' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-dm', expect.objectContaining({ body: 'direct note' })))
    expect(openSpy).toHaveBeenCalledTimes(3)
    expect(openSpy.mock.calls[2]?.[0]).toEqual(expect.objectContaining({ title: '', memberIds: ['run:qa'] }))

    const rows = within(screen.getByRole('region', { name: 'Conversations' }))
    for (const [topic, threadId] of [['Release', 'mt-release'], ['Verification', 'mt-verification']] as const) {
      fireEvent.click(rows.getByRole('button', { name: new RegExp(topic) }))
      expect(screen.getByRole('heading', { name: topic })).toBeInTheDocument()
      fireEvent.change(screen.getByPlaceholderText(`Message ${topic}…`), { target: { value: `note for ${topic}` } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(sendSpy).toHaveBeenCalledWith(threadId, expect.objectContaining({ body: `note for ${topic}` })))
    }
    expect(rows.getAllByRole('button', { name: /QA reviewer/ })).toHaveLength(3)
  })

  test('a new run agent is shown by name right away, never by its participant id', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'run:t-1', thread_id: 'mt-5' })
    renderTeamView()
    openAddRunAgent()
    const dialog = screen.getByRole('dialog', { name: 'Add run agent' })
    fireEvent.change(within(dialog).getByPlaceholderText('What should this run agent do?'), { target: { value: 'watch tests' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add run agent' }))
    expect(await screen.findByRole('heading', { name: 'QA reviewer' })).toBeInTheDocument()
    expect(screen.queryByText(/run:t-1/)).toBeNull()
    expect(within(peopleRegion('This run')).getAllByText('QA reviewer').length).toBeGreaterThan(0)
  })

  test('Add run agent task stays titled in Conversations while People starts an untitled DM', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'attachAndSendMesh').mockResolvedValue({ task_id: 't1', participant_id: 'run:t-1', thread_id: 'mt-task' })
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue({ id: 'mt-dm', scope: 'run', members: ['user', 'run:t-1'], status: 'open' })
    const sendSpy = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'm2', seq: 2 })
    renderTeamView([makeStage()], [], enabledRoster({ participants: participants.filter((p) => p.id !== 'run:qa') }))
    openAddRunAgent()
    const dialog = screen.getByRole('dialog', { name: 'Add run agent' })
    fireEvent.change(within(dialog).getByPlaceholderText('What should this run agent do?'), { target: { value: 'watch tests' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Conversations' })).getByText('QA reviewer')).toBeInTheDocument())
    pickRunPerson(/^QA reviewer/)
    expect(openSpy).not.toHaveBeenCalled()
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'hello' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sendSpy).toHaveBeenCalledWith('mt-dm', expect.objectContaining({ body: 'hello' })))
    expect(openSpy).toHaveBeenCalledWith(expect.objectContaining({ title: '', memberIds: ['run:t-1'] }))
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getAllByText('QA reviewer').length).toBe(2)
  })

  test('the narrow list opens a conversation when a person is picked', async () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(341)
    renderTeamView()
    const roster = screen.getByRole('navigation', { name: 'Team roster' })
    expect(roster).toHaveClass('open')
    pickRunPerson(/^QA reviewer/)
    expect(roster).not.toHaveClass('open')
    expect(screen.getByRole('button', { name: 'Back to people' })).toBeInTheDocument()
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

  test('a measured Team container that narrows into list mode transfers separator focus', () => {
    mockThreadAndBoardFetches()
    const measured = mockTeamWidth(600)
    const view = renderTeamView()
    const layout = view.container.querySelector('.team-view') as HTMLElement
    const handle = teamSeparator()
    handle.focus()

    measured.resize(341)
    expect(layout).toHaveClass('drawer')
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    expect(handle).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('tab', { name: 'Messages' })).toHaveFocus()
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
  })

  test('an initially narrow Team container starts in drawer mode', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(341)
    const view = renderTeamView()
    expect(view.container.querySelector('.team-view')).toHaveClass('drawer')
    const handle = view.container.querySelector('.team-roster-resizer')
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    expect(handle).toHaveAttribute('tabindex', '-1')
    expect(view.container.querySelector('.team-view')).not.toHaveClass('mobile-detail')
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
  })

  test('keeps measuring Team after a temporary disabled state', () => {
    mockThreadAndBoardFetches()
    const measured = mockTeamWidth(600)
    const initialRoster = enabledRoster({ probed: false })
    const view = render(
      <TeamViewWithBoard runId="test-existing" roster={initialRoster} stages={[makeStage()]} events={[]} profiles={profiles} />,
    )
    const firstLayout = view.container.querySelector('.team-view')
    expect(teamSeparator()).toHaveAttribute('aria-valuemax', '280')

    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster({ enabled: false })} stages={[makeStage()]} events={[]} profiles={profiles} />)
    expect(screen.getByText('Team is not available for this run')).toBeInTheDocument()
    view.rerender(<TeamViewWithBoard runId="test-existing" roster={enabledRoster()} stages={[makeStage()]} events={[]} profiles={profiles} />)
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

  test('the tabs follow the tab pattern: arrows switch tabs and each tab controls its panel', async () => {
    mockThreadAndBoardFetches()
    renderTeamView()
    const messages = screen.getByRole('tab', { name: 'Messages' })
    expect(messages).toHaveAttribute('aria-controls', screen.getByRole('tabpanel', { name: 'Messages' }).id)
    messages.focus()
    fireEvent.keyDown(messages, { key: 'ArrowRight' })
    const board = screen.getByRole('tab', { name: /^Board/ })
    expect(board).toHaveFocus()
    expect(board).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel', { name: /Board/ })).toHaveAttribute('aria-labelledby', board.id)
    expect(await screen.findByRole('button', { name: 'Post task' })).toBeInTheDocument()
  })

  test('run-wide tabs sit above the conversation, hide its heading, and restore its selection and draft', async () => {
    mockThreadAndBoardFetches()
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ tasks: [], typedTasks: false, entries: [
      { author_id: 'user', key: 'task/release', version: 1, status: 'Release', seq: 1, ts: '' },
      { author_id: 'run:qa', key: 'task/release/run:qa', version: 1, status: 'Done', seq: 2, ts: '' },
      { author_id: 'user', key: 'fact/build', version: 1, status: 'Ready', seq: 3, ts: '' },
    ] })
    const thread: meshClient.MeshThread = { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [thread] }))

    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('Release'))
    const heading = screen.getByRole('heading', { name: 'Release' })
    const tabs = screen.getByRole('tablist', { name: 'Team' })
    expect(tabs.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const composer = screen.getByPlaceholderText('Message Release…')
    fireEvent.change(composer, { target: { value: 'draft for QA' } })

    await waitFor(() => expect(screen.getByRole('tab', { name: /^Board/ })).toHaveTextContent('1'))
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.queryByRole('heading', { name: 'Release' })).toBeNull()
    expect(screen.getByRole('tabpanel', { name: /^Board/ })).toBeInTheDocument()
    fireEvent.change(screen.getByPlaceholderText('e.g. CSV export'), { target: { value: 'board draft' } })

    fireEvent.click(screen.getByRole('tab', { name: 'Activity' }))
    expect(screen.queryByRole('heading', { name: 'Release' })).toBeNull()
    expect(screen.getByRole('tabpanel', { name: 'Activity' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByPlaceholderText('e.g. CSV export')).toHaveValue('board draft')

    fireEvent.click(screen.getByRole('tab', { name: 'Messages' }))
    expect(screen.getByRole('heading', { name: 'Release' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Message Release…')).toBe(composer)
    expect(composer).toHaveValue('draft for QA')
  })

  test('switching run-wide tabs preserves the initially selected conversation', () => {
    mockThreadAndBoardFetches()
    renderTeamView([makeStage()], [], enabledRoster({ threads: [
      { id: 'mt-1', title: 'Release', scope: 'run', members: ['user', 'run:qa'], status: 'open' },
    ] }))

    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    fireEvent.click(screen.getByRole('tab', { name: 'Activity' }))
    fireEvent.click(screen.getByRole('tab', { name: 'Messages' }))
    expect(screen.getByRole('heading', { name: 'Release' })).toBeInTheDocument()
  })

  test('Back returns from Board and Activity to the narrow list', () => {
    mockThreadAndBoardFetches()
    mockTeamWidth(341)
    renderTeamView()

    fireEvent.click(screen.getByRole('tab', { name: /^Board/ }))
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(screen.getByRole('navigation', { name: 'Team roster' })).toHaveClass('open')
    fireEvent.click(screen.getByRole('tab', { name: 'Activity' }))
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
  })

  test('a direct send that finishes after the user picked another conversation does not switch back to it', async () => {
    mockThreadAndBoardFetches()
    const opened: meshClient.MeshThread = { id: 'mt-9', title: '', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    vi.spyOn(meshClient, 'openMeshThread').mockResolvedValue(opened)
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [other] }))

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'please review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(meshClient.sendMeshMessage).toHaveBeenCalled())

    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('With Implement agent'))
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

    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    pickRunPerson(/^QA reviewer/)
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('With Implement agent'))
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

    pickRunPerson(/^QA reviewer/)
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

    pickRunPerson(/^QA reviewer/)
    fireEvent.change(screen.getByPlaceholderText('Message QA reviewer…'), { target: { value: 'half-written' } })
    fireEvent.click(screen.getByText('With Implement agent'))
    expect((screen.getByPlaceholderText('Message With Implement agent…') as HTMLTextAreaElement).value).toBe('')
    pickRunPerson(/^QA reviewer/)
    expect((screen.getByPlaceholderText('Message QA reviewer…') as HTMLTextAreaElement).value).toBe('half-written')
  })

  test('a message that finished sending after the user switched away is gone from the draft on return', async () => {
    mockThreadAndBoardFetches()
    let resolveSend!: (v: { message_id: string; seq: number }) => void
    vi.spyOn(meshClient, 'sendMeshMessage').mockReturnValue(new Promise((res) => (resolveSend = res)))
    const withQA: meshClient.MeshThread = { id: 'mt-9', title: 'With QA', scope: 'run', members: ['user', 'run:qa'], status: 'open' }
    const other: meshClient.MeshThread = { id: 'mt-b', title: 'With Implement agent', scope: 'run', members: ['user', 'stage:implement'], status: 'open' }
    renderTeamView([makeStage()], [], enabledRoster({ threads: [withQA, other] }))

    fireEvent.click(within(screen.getByRole('navigation', { name: 'Team roster' })).getByText('With QA'))
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

    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()

    expect(screen.getByPlaceholderText('What is this about?')).toBeDisabled()
    for (const box of screen.getAllByRole('checkbox')) expect(box).toBeDisabled()
    await act(async () => resolveOpen({ id: 'mt-9', title: 'Hand-off', scope: 'run', members: ['user', 'run:qa'], status: 'open' }))
    expect(screen.getByPlaceholderText('Message Hand-off…')).toBeInTheDocument()
  })

  test('a retry of the same "Create conversation" form reuses its request id', async () => {
    mockThreadAndBoardFetches()
    const openSpy = vi.spyOn(meshClient, 'openMeshThread').mockRejectedValue(new Error('network'))
    renderTeamView()
    clickCreateConversation()
    fireEvent.change(screen.getByPlaceholderText('What is this about?'), { target: { value: 'Hand-off' } })
    fireEvent.click(screen.getByText('QA reviewer', { selector: 'label span' }))
    clickCreateConversation()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('network'))
    clickCreateConversation()
    await waitFor(() => expect(openSpy).toHaveBeenCalledTimes(2))
    expect(openSpy.mock.calls[1]?.[0].requestId).toBe(openSpy.mock.calls[0]?.[0].requestId)
  })
})
