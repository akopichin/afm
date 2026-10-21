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
      listView="people"
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
  test('New to you count is the number of marked conversations', () => {
    render(<RunTeamList participants={participants} threads={threads} stages={stages}
      listView="conversations" onAddRunAgent={vi.fn()} onNewOnlyChange={vi.fn()}
      unreadThreadIds={new Set(threads.map((thread) => thread.id))} />)
    expect(screen.getByRole('button', { name: 'New to you 2' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Conversations' })).getAllByRole('button', { name: /New messages/ })).toHaveLength(2)
  })

  test('New to you filters marked conversations, stays removable when empty, and keeps the selected thread outside the list', () => {
    const onNewOnlyChange = vi.fn()
    const { rerender } = render(<RunTeamList participants={participants} threads={threads} stages={stages}
      listView="conversations" selectedThreadId="mt-1" onAddRunAgent={vi.fn()}
      unreadThreadIds={new Set(['mt-e95e00425b9b4640a0bc'])} newOnly onNewOnlyChange={onNewOnlyChange} />)
    const filter = screen.getByRole('button', { name: /New to you.*1/ })
    expect(filter).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByRole('button', { name: /Hand-off/ })).toBeNull()
    expect(screen.getByRole('button', { name: /Commit & cleanup.*New messages/ })).toBeInTheDocument()
    rerender(<RunTeamList participants={participants} threads={threads} stages={stages}
      listView="conversations" selectedThreadId="mt-1" onAddRunAgent={vi.fn()}
      unreadThreadIds={new Set()} newOnly onNewOnlyChange={onNewOnlyChange} />)
    expect(screen.getByText("You're all caught up")).toBeInTheDocument()
    fireEvent.click(filter)
    expect(onNewOnlyChange).toHaveBeenCalledWith(false)
  })

  test('focus moves to the active filter if reading removes its focused conversation row', () => {
    const common = { participants, threads, stages, listView: 'conversations' as const,
      onAddRunAgent: vi.fn(), newOnly: true, onNewOnlyChange: vi.fn() }
    const { rerender } = render(<RunTeamList {...common} unreadThreadIds={new Set([threads[0]!.id])} />)
    screen.getByRole('button', { name: /Hand-off.*New messages/ }).focus()
    rerender(<RunTeamList {...common} unreadThreadIds={new Set()} />)
    expect(screen.getByRole('button', { name: /New to you.*0/ })).toHaveFocus()
  })
  test('Team People stably puts only working run and Side agents first after a roster update', () => {
    const people: MeshParticipant[] = [
      { id: 'run:waiting', kind: 'run_agent', scope: 'run', display_name: 'Waiting', reachable: true, activity: 'waiting' },
      { id: 'side:working', kind: 'side_agent', scope: 'run', display_name: 'Side working', reachable: true, activity: 'working' },
      { id: 'run:idle', kind: 'run_agent', scope: 'run', display_name: 'Idle', reachable: true, activity: 'idle' },
      { id: 'run:working', kind: 'run_agent', scope: 'run', display_name: 'Run working', reachable: true, activity: 'working' },
    ]
    const view = render(<RunTeamList participants={people} stages={stages} listView="people" onAddRunAgent={vi.fn()} />)
    const names = () => within(screen.getByRole('region', { name: 'This run' })).getAllByRole('button')
      .map((row) => row.querySelector('.team-person-name')?.textContent)
    expect(names()).toEqual(['Side working', 'Run working', 'Waiting', 'Idle'])
    view.rerender(<RunTeamList participants={people.map((p) => p.id === 'run:waiting' ? { ...p, activity: 'working' } : p)} stages={stages} listView="people" onAddRunAgent={vi.fn()} />)
    expect(names()).toEqual(['Waiting', 'Side working', 'Run working', 'Idle'])
  })

  test('Team Stages can collapse while preserving flow order and nested helpers when reopened', () => {
    renderList()
    const toggle = screen.getByRole('button', { name: /Stages/ })
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(inStages().queryByRole('button', { name: /Correctness helper/ })).toBeNull()
    fireEvent.click(toggle)
    expect(stageRowNames()).toEqual(['Plan', 'Implement feature', 'Commit & cleanup'])
    expect(screen.getByText('Correctness helper').closest('button')).toHaveClass('nested')
  })

  test('People keeps run and stage groups separate from Conversations', () => {
    renderList()
    expect(screen.getByRole('region', { name: 'This run' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Stages' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Conversations' })).toBeNull()
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

  test('a stage waiting for a colleague says "Waiting" — short enough to keep its @name readable', () => {
    renderList({ stages: [stages[0], { id: 'implement', title: 'Implement feature', status: 'awaiting_peer' }, stages[2]] as typeof stages })
    const row = screen.getByText('Implement feature').closest('button') as HTMLElement
    expect(within(row).getByText('Waiting')).toBeInTheDocument()
  })

  test('a run agent or helper row says what it is doing now, and its presence dot follows', () => {
    renderList({
      participants: [
        ...participants.filter((p) => p.id !== 'run:qa' && p.id !== 'helper:h1'),
        { id: 'run:qa', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true, activity: 'waiting' },
        { id: 'helper:h1', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Correctness helper', reachable: false, activity: 'done' },
      ],
    })
    const qa = screen.getByText('QA reviewer').closest('button') as HTMLElement
    expect(within(qa).getByText('Waiting')).toBeInTheDocument()
    expect(qa.querySelector('.team-presence')).toHaveAttribute('data-presence', 'waiting')
    const helper = screen.getByText('Correctness helper').closest('button') as HTMLElement
    expect(within(helper).getByText('Done')).toBeInTheDocument()
    expect(helper.querySelector('.team-presence')).toHaveAttribute('data-presence', 'idle')
  })

  test('a finished task row says how it ended when the server gives the reason', () => {
    renderList({
      participants: [
        ...participants.filter((p) => p.id !== 'helper:h1'),
        {
          id: 'helper:h1', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Correctness helper', reachable: false,
          activity: 'done', unreachable_reason: 'task h1 failed: invalid result — invalid verdict value "approved"',
        },
      ],
    })
    const helper = screen.getByText('Correctness helper').closest('button') as HTMLElement
    expect(within(helper).getByText('task h1 failed: invalid result — invalid verdict value "approved"')).toBeInTheDocument()
  })

  test("a stage's helpers are listed right under that stage", () => {
    renderList()
    const names = within(screen.getByRole('region', { name: 'Stages' }))
      .getAllByRole('button')
      .filter((button) => button.classList.contains('team-person-row'))
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
    const { onSelectThread } = renderList({ listView: 'conversations' })
    const conversations = screen.getByRole('region', { name: 'Conversations' })
    expect(within(conversations).getByText('Commit & cleanup')).toBeInTheDocument()
    expect(within(conversations).queryByText(/^mt-/)).toBeNull()
    fireEvent.click(within(conversations).getByText('Hand-off'))
    expect(onSelectThread).toHaveBeenCalledWith(threads[0])
  })
})

// The stage Feed's people panel: the same roster, focused on one stage.
describe('RunTeamList with focusStageId', () => {
  const focusParticipants: MeshParticipant[] = [
    ...participants,
    {
      id: 'run:sec', kind: 'run_agent', scope: 'run', alias: 'security', display_name: 'Security', reachable: true,
      command: 'codex-as-claude', readonly_enforcement: 'os',
    },
    { id: 'helper:h2', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Docs helper', reachable: true, command: 'claude', readonly_enforcement: 'none' },
    { id: 'helper:h3', kind: 'helper', scope: 'stage', stage_id: 'commit', display_name: 'Commit helper', reachable: true },
  ]

  function renderFocused(overrides: Partial<Parameters<typeof RunTeamList>[0]> = {}) {
    const onPick = vi.fn()
    const onSelectParticipant = vi.fn()
    const onAddRunAgent = vi.fn()
    render(
      <RunTeamList
        id="stage-people-roster"
        drawer={false}
        focusStageId="implement"
        participants={focusParticipants}
        stages={stages}
        onPick={onPick}
        onSelectParticipant={onSelectParticipant}
        onAddRunAgent={onAddRunAgent}
        {...overrides}
      />,
    )
    return { onPick, onSelectParticipant, onAddRunAgent }
  }

  function groupNames(): string[] {
    return screen.getAllByRole('region').map((r) => r.querySelector('h3')?.firstChild?.textContent ?? '')
  }

  function rowNames(group: string): string[] {
    return within(screen.getByRole('region', { name: new RegExp(`^${group}`) }))
      .getAllByRole('button')
      .map((b) => b.querySelector('.team-person-name')?.textContent ?? '')
  }

  test('groups: this stage first, then run agents, then the other stages in flow order — no conversations', () => {
    renderFocused()
    expect(groupNames()).toEqual(['This stage', 'Run agents', 'Other stages'])
    expect(rowNames('This stage')).toEqual(['Implement feature', 'Correctness helper', 'Docs helper'])
    expect(rowNames('Run agents')).toEqual(['QA reviewer', 'claude', 'Security'])
    // Flow order (plan → commit), each stage's helpers right under it; the focused stage is not repeated.
    expect(rowNames('Other stages')).toEqual(['Plan', 'Commit & cleanup', 'Commit helper'])
    expect(screen.queryByRole('region', { name: 'Conversations' })).toBeNull()
  })

  test('the root and its group headings use the given id, not the Team tab ids', () => {
    renderFocused()
    expect(document.getElementById('stage-people-roster')).not.toBeNull()
    expect(document.getElementById('team-roster')).toBeNull()
    expect(document.getElementById('stage-people-roster-stage')).not.toBeNull()
    expect(screen.getByRole('navigation', { name: 'People' })).not.toHaveClass('open')
  })

  test('a click picks the person through onPick, not onSelectParticipant', () => {
    const { onPick, onSelectParticipant } = renderFocused()
    fireEvent.click(screen.getByText('Security'))
    expect(onPick).toHaveBeenCalledWith(focusParticipants.find((p) => p.id === 'run:sec'))
    expect(onSelectParticipant).not.toHaveBeenCalled()
  })

  test("a finished participant is listed but disabled: it can't be written to", () => {
    const { onPick } = renderFocused()
    const helper = screen.getByText('Correctness helper').closest('button') as HTMLElement
    expect(helper).toBeDisabled()
    expect(within(helper).getByText("Finished — can't write")).toBeInTheDocument()
    fireEvent.click(helper)
    expect(onPick).not.toHaveBeenCalled()
  })

  test('a stage not started yet is addressable: it reads the message when it starts', () => {
    renderFocused()
    const commit = screen.getByText('Commit & cleanup').closest('button') as HTMLElement
    expect(commit).toBeEnabled()
    expect(within(commit).getByText('Not started — reads it when it starts')).toBeInTheDocument()
  })

  test('a script stage the server marks unreachable is disabled with the server reason, not "finished"', () => {
    renderFocused({
      participants: [
        ...focusParticipants.filter((p) => p.id !== 'stage:commit'),
        { id: 'stage:commit', kind: 'stage_agent', scope: 'stage', stage_id: 'commit', display_name: 'Commit & cleanup', reachable: false, unreachable_reason: 'script stage: no agent reads messages' },
      ],
    })
    const commit = screen.getByText('Commit & cleanup').closest('button') as HTMLElement
    expect(commit).toBeDisabled()
    expect(within(commit).getByText('Script stage: no agent reads messages')).toBeInTheDocument()
  })

  test('the sandbox badge shows the current guarantee; a different admission-time one is in its tooltip', () => {
    renderFocused({
      participants: [
        ...focusParticipants.filter((p) => p.id !== 'run:sec'),
        { id: 'run:sec', kind: 'run_agent', scope: 'run', display_name: 'Security', reachable: true, command: 'codex', readonly_enforcement: 'none', admitted_readonly_enforcement: 'os' },
      ],
    })
    const badge = within(screen.getByText('Security').closest('button') as HTMLElement).getByText('no sandbox')
    expect(badge).toHaveAttribute('data-enforcement', 'none')
    expect(badge.getAttribute('title')).toMatch(/^No OS read-only sandbox.*When it was added: read-only, enforced by an OS sandbox$/)
    // Same value at admission and now: no history note.
    const docs = within(screen.getByText('Docs helper').closest('button') as HTMLElement).getByText('no sandbox')
    expect(docs.getAttribute('title')).not.toMatch(/When it was added/)
  })

  test('the command chip and the sandbox come from the roster fields', () => {
    renderFocused()
    const sec = screen.getByText('Security').closest('button') as HTMLElement
    expect(within(sec).getByText('codex-as-claude')).toHaveClass('team-person-chip')
    expect(within(sec).getByText('read-only')).toHaveAttribute('data-enforcement', 'os')
    const docs = screen.getByText('Docs helper').closest('button') as HTMLElement
    expect(within(docs).getByText('claude')).toHaveClass('team-person-chip')
    expect(within(docs).getByText('no sandbox')).toHaveAttribute('data-enforcement', 'none')
    // Nothing guessed: a row without the fields has no chips.
    const qa = screen.getByText('QA reviewer').closest('button') as HTMLElement
    expect(qa.querySelector('.team-person-chip')).toBeNull()
  })

  test('without onPick the rows are disabled and the hint says when writing is possible', () => {
    renderFocused({ onPick: undefined, pickHint: 'You can write to them here while the run is going.' })
    expect(screen.getByText('You can write to them here while the run is going.')).toBeInTheDocument()
    expect(screen.getByText('Security').closest('button')).toBeDisabled()
  })

  test('"Add run agent…" calls onAddRunAgent; "Attach helper to stage…" shows only with onAttachHelper', () => {
    const { onAddRunAgent } = renderFocused()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent…' }))
    expect(onAddRunAgent).toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Attach helper to stage…' })).toBeNull()
  })
})

describe('RunTeamList without focusStageId (the Team tab)', () => {
  test('keeps its groups, ids and rows: no chips, nothing disabled, finished rows keep their reason', () => {
    render(
      <RunTeamList
        participants={[...participants, { id: 'run:sec', kind: 'run_agent', scope: 'run', display_name: 'Security', reachable: true, command: 'codex', readonly_enforcement: 'os' }]}
        threads={threads}
        stages={stages}
        onSelectParticipant={vi.fn()}
        onSelectThread={vi.fn()}
        onAddRunAgent={vi.fn()}
        listView="people"
        open
      />,
    )
    expect(screen.getAllByRole('region').map((r) => r.querySelector('h3')?.textContent)).toEqual(['This run', '▾ Stages'])
    const nav = screen.getByRole('navigation', { name: 'Team roster' })
    expect(nav).toHaveAttribute('id', 'team-roster')
    expect(nav).toHaveClass('team-roster', 'open')
    expect(nav).not.toHaveClass('team-roster-embedded')
    expect(document.getElementById('team-roster-run')).not.toBeNull()
    expect(nav.querySelector('.team-person-chip, .team-person-title, .team-person-sandbox')).toBeNull()
    expect(within(nav).getAllByRole('button').every((b) => !(b as HTMLButtonElement).disabled)).toBe(true)
    expect(screen.getByText('task h1 has reached a terminal status')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add run agent' })).toBeInTheDocument()
  })
})
