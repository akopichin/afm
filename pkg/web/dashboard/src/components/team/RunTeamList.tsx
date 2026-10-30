import { useId, useLayoutEffect, useRef, useState, type ReactElement, type ReactNode, type Ref } from 'react'
import type { AssistantTask } from '../../types/assistant-task'
import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../../api/mesh-client'
import { activityLabel, activityPresence, participantLabel, participantRole, stagePresence, threadLabel, type Presence } from '../../lib/mesh-roster'
import { MeshAvatar } from './MeshAvatar'
import { STAGE_STATUS_LABELS, type Stage, type StageStatus } from '../../types'
import { ThreadList } from './ThreadList'
import { TabList, tabPanelProps } from '../tab-list'

export type RunTeamListStage = { id: string; title: string; status: string }

// teamStagesOf — the live stage list (/api/status, flow order) as the roster's stage rows.
export function teamStagesOf(stages: Stage[]): RunTeamListStage[] {
  return stages.map((s) => ({ id: s.id, title: s.name !== '' ? s.name : s.id, status: s.status }))
}

export type RunTeamListProps = {
  participants: MeshParticipant[]
  // threads/onSelectThread — the Team-only Conversations list; focusStageId shows only People.
  threads?: MeshThread[]
  // stages — the SAME live Stage list App.tsx already polls (/api/status), in flow order. It orders the
  // "Stages" group and gives each stage row its live status: Participant.Reachable only tracks whether a
  // stage/task is TERMINAL (pkg/orchestrator/agentcomm_roster.go), not paused/pending/running.
  stages: RunTeamListStage[]
  selectedParticipantId?: string | null
  selectedThreadId?: string | null
  onSelectParticipant?: (participant: MeshParticipant) => void
  onSelectThread?: (thread: MeshThread) => void
  onAddRunAgent: () => void
  listView?: 'conversations' | 'people'
  onListViewChange?: (view: 'conversations' | 'people') => void
  search?: string
  onSearchChange?: (value: string) => void
  summaries?: MeshThreadSummaries
  unreadThreadIds?: ReadonlySet<string>
  newOnly?: boolean
  onNewOnlyChange?: (next: boolean) => void
  onCreateConversation?: (opener: HTMLButtonElement) => void
  actionButtonRef?: Ref<HTMLButtonElement>
  // onAttachHelper — focus mode only: "Attach helper to stage…" next to "Add run agent…" (a helper works for
  // this stage, a run agent for the whole run). Undefined — no such button.
  onAttachHelper?: () => void
  // id — the root element's id (a drawer toggle's aria-controls) and the prefix of its group headings' ids,
  // so a second embedded roster never duplicates the Team tab's ids.
  id?: string
  // drawer — false: an embedding view owns layout; open is then ignored.
  drawer?: boolean
  // open — the embedding layout marks the roster as the visible narrow panel.
  open?: boolean
  // focusStageId — the roster as one stage's "people you can talk to" (the stage Feed's side panel): groups
  // "This stage" (its agent and helpers), "Run agents", "Other stages" in flow order; each row shows its
  // command and read-only guarantee from the roster; finished participants are disabled. No conversations.
  focusStageId?: string
  // onPick — a click on a person in focus mode (the feed addresses them). Without it the rows are disabled:
  // there is nowhere to write from right now, and pickHint says why.
  onPick?: (participant: MeshParticipant) => void
  pickHint?: string
  helperTasks?: readonly AssistantTask[]
  onClose?: () => void
}

function stageStatusLabel(status: string): string {
  return STAGE_STATUS_LABELS[status as StageStatus] ?? status
}

// participantStatus — the status label shown under a row's name, or null for "nothing to say". A stage
// agent shows its stage's live status. Any other participant shows "Paused" when its stage is paused
// (Reachable never distinguishes "paused" from "running"), else its unreachable reason.
function participantStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  // "Awaiting colleague" is the rail's wording; the narrow roster row says it shorter.
  if (p.kind === 'stage_agent' && stage !== undefined) return stage.status === 'awaiting_peer' ? 'Waiting' : stageStatusLabel(stage.status)
  if (stage?.status === 'paused') return 'Paused'
  // A finished task says how it ended (failed and why, cancelled) when the server gives the reason.
  if (p.activity === 'done' && p.unreachable_reason !== undefined && p.unreachable_reason !== '') return p.unreachable_reason
  const activity = activityLabel(p.activity)
  if (activity !== null) return activity
  if (!p.reachable) return p.unreachable_reason && p.unreachable_reason !== '' ? p.unreachable_reason : 'Unreachable'
  return null
}

// participantPresence — the row's presence dot: a stage agent follows its stage's status, anyone else is
// "working" while reachable and "idle" once its stage/task is terminal.
function participantPresence(p: MeshParticipant, stage: RunTeamListStage | undefined): Presence {
  if (stage !== undefined && (p.kind === 'stage_agent' || stage.status === 'paused')) return stagePresence(stage.status)
  return activityPresence(p.activity, p.reachable)
}

// isFinished — the participant's stage or task has ended. The server marks a task unreachable only once it
// is terminal; a stage can also be unreachable for another reason (a script stage has no agent).
function isFinished(p: MeshParticipant, stage: RunTeamListStage | undefined): boolean {
  if (p.kind === 'stage_agent') return stage?.status === 'done' || stage?.status === 'failed'
  return true
}

// focusStatus — a row's status in the stage people panel: whether a message can reach this person now.
// Finished participants cannot be written to; anyone else the server marks unreachable (a script stage has
// no agent) shows the server's reason; a stage not started yet reads it when it starts.
function focusStatus(p: MeshParticipant, stage: RunTeamListStage | undefined): string | null {
  if (!p.reachable) {
    if (isFinished(p, stage)) return "Finished — can't write"
    const reason = p.unreachable_reason ?? ''
    return reason === '' ? "Can't write" : reason.charAt(0).toUpperCase() + reason.slice(1)
  }
  if (p.kind === 'stage_agent' && stage?.status === 'pending') return 'Not started — reads it when it starts'
  return participantStatus(p, stage)
}

// sandboxLabel — the read-only guarantee the server froze into a run agent's or helper's task: an OS sandbox,
// or none (only the container or the prompt limits it). Stage agents write the project and carry none.
// The badge shows the server's current guarantee; when the task was admitted under a different one (a host
// run resumed in a container), the tooltip says so.
const SANDBOX_TEXT: Record<string, { text: string; title: string }> = {
  os: { text: 'read-only', title: 'Read-only, enforced by an OS sandbox' },
  none: { text: 'no sandbox', title: 'No OS read-only sandbox: only the container or the prompt limits it' },
}

function sandboxLabel(p: MeshParticipant): { text: string; title: string } | null {
  const current = SANDBOX_TEXT[p.readonly_enforcement ?? '']
  if (current === undefined) return null
  const admitted = SANDBOX_TEXT[p.admitted_readonly_enforcement ?? '']
  if (admitted === undefined || p.admitted_readonly_enforcement === p.readonly_enforcement) return current
  return { text: current.text, title: `${current.title}. When it was added: ${admitted.title.charAt(0).toLowerCase()}${admitted.title.slice(1)}` }
}

// stageOrder — stage ids in flow order; stages the roster knows but /api/status doesn't (yet) go last, in
// the order the roster lists them.
function stageOrder(stageParticipants: MeshParticipant[], stages: RunTeamListStage[]): string[] {
  const order = stages.map((s) => s.id).filter((id) => stageParticipants.some((p) => p.stage_id === id))
  for (const p of stageParticipants) {
    const id = p.stage_id as string
    if (!order.includes(id)) order.push(id)
  }
  return order
}

// RunTeamList — the Team navigation rail. Conversations is the initial list; People groups the run agents,
// Side agent, stages and helpers. With focusStageId it remains the stage Feed's focused People panel.
export function RunTeamList({
  participants, threads = [], stages, selectedParticipantId = null, selectedThreadId = null,
  onSelectParticipant, onSelectThread, onAddRunAgent, id = 'team-roster', drawer = true, open = false,
  focusStageId, onPick, pickHint, onAttachHelper, listView = 'conversations', onListViewChange,
  search = '', onSearchChange, summaries = {}, unreadThreadIds, newOnly = false, onNewOnlyChange,
  onCreateConversation, actionButtonRef, helperTasks = [], onClose,
}: RunTeamListProps): ReactElement {
  const listTabsId = useId()
  const newFilterRef = useRef<HTMLButtonElement>(null)
  const focusedThreadRowRef = useRef<HTMLButtonElement | null>(null)
  const [stagesExpanded, setStagesExpanded] = useState(true)
  const focused = focusStageId !== undefined
  const runParticipants = participants.filter((p) => p.scope === 'run' && p.kind !== 'user')
  const stageParticipants = participants.filter((p) => p.scope === 'stage' && p.stage_id !== undefined && p.stage_id !== '')
  const query = search.trim().toLocaleLowerCase()
  const matchesPerson = (p: MeshParticipant): boolean =>
    `${participantLabel(p)} ${p.alias ?? ''} ${p.alias ? `@${p.alias}` : ''}`.toLocaleLowerCase().includes(query)
  const matchingRunParticipants = runParticipants.filter(matchesPerson)
  // The two filters keep roster order within each presence group, including across polls.
  const visibleRunParticipants = [
    ...matchingRunParticipants.filter((p) => participantPresence(p, undefined) === 'working'),
    ...matchingRunParticipants.filter((p) => participantPresence(p, undefined) !== 'working'),
  ]
  const visibleStageParticipants = stageParticipants.filter(matchesPerson)
  const unreadCount = threads.filter((t) => unreadThreadIds?.has(t.id)).length
  const visibleThreads = threads.filter((t) => {
    if (newOnly && !unreadThreadIds?.has(t.id)) return false
    const members = t.members.map((id) => participants.find((p) => p.id === id)).filter((p): p is MeshParticipant => p !== undefined)
    const terms = [threadLabel(t, participants), t.title ?? '', summaries[t.id]?.last_message?.preview ?? '',
      ...members.flatMap((p) => [participantLabel(p), p.alias ?? '', p.alias ? `@${p.alias}` : ''])]
    return terms.some((term) => term.toLocaleLowerCase().includes(query))
  })
  useLayoutEffect(() => {
    if (!newOnly || focusedThreadRowRef.current?.isConnected !== false || document.activeElement !== document.body) return
    newFilterRef.current?.focus()
    focusedThreadRowRef.current = null
  }, [newOnly, visibleThreads])

  function renderRow(p: MeshParticipant, nested = false): ReactElement {
    const stage = stages.find((s) => s.id === p.stage_id)
    const name = participantLabel(p)
    const status = focused ? focusStatus(p, stage) : participantStatus(p, stage)
    const detail = status ?? (p.scope === 'run' ? participantRole(p.kind) : null)
    const sandbox = focused ? sandboxLabel(p) : null
    const helperTask = p.kind === 'helper' ? helperTasks.find((task) => p.id === `helper:${task.id}`) : undefined
    const disabled = focused && (onPick === undefined || !p.reachable)
    return (
      <li key={p.id}>
        <button
          type="button"
          className={`team-person-row${nested ? ' nested' : ''}${p.id === selectedParticipantId ? ' active' : ''}`}
          data-kind={p.kind}
          data-status={p.kind === 'stage_agent' ? stage?.status : undefined}
          disabled={disabled}
          onClick={() => (focused ? onPick?.(p) : onSelectParticipant?.(p))}
        >
          <MeshAvatar name={name} kind={p.kind} />
          <span className="team-person-text">
            {focused ? (
              <span className="team-person-title">
                <span className="team-person-name" title={name}>{name}</span>
                {p.command !== undefined && p.command !== '' && (
                  <span className="team-person-chip" title={`Command: ${p.command}`}>{p.command}</span>
                )}
              </span>
            ) : (
              <span className="team-person-name" title={name}>{name}</span>
            )}
            <span className="team-person-detail" title={detail ?? undefined}>
              {p.alias !== undefined && p.alias !== '' && <span className="team-person-handle">@{p.alias}</span>}
              {detail !== null && <span className="team-person-status">{detail}</span>}
              {helperTask !== undefined && <span className="team-person-mode">{helperTask.mode === 'required' ? 'Required' : 'Advisory'}</span>}
              {sandbox !== null && (
                <span className="team-person-sandbox" data-enforcement={p.readonly_enforcement} title={sandbox.title}>
                  {sandbox.text}
                </span>
              )}
            </span>
          </span>
          <span className="team-presence" data-presence={participantPresence(p, stage)} aria-hidden="true" />
        </button>
      </li>
    )
  }

  // A stage agent first, then its helpers; a helper whose stage agent isn't in the roster still shows up.
  function renderStage(stageId: string, source = stageParticipants): ReactElement[] {
    const members = source.filter((p) => p.stage_id === stageId)
    const agents = members.filter((p) => p.kind === 'stage_agent')
    const helpers = members.filter((p) => p.kind !== 'stage_agent')
    return [...agents.map((p) => renderRow(p)), ...helpers.map((p) => renderRow(p, agents.length > 0))]
  }

  const rootClass = `team-roster${drawer ? '' : ' team-roster-embedded'}${drawer && open ? ' open' : ''}`

  if (focused) {
    const visibleParticipants = stageParticipants.filter(matchesPerson)
    const thisStageRows = renderStage(focusStageId, visibleParticipants)
    const otherStages = stageOrder(visibleParticipants, stages).filter((s) => s !== focusStageId)
    const otherCount = visibleParticipants.filter((p) => p.stage_id !== focusStageId).length
    const agents = matchingRunParticipants.filter((p) => p.kind !== 'side_agent')
    const sideAgents = matchingRunParticipants.filter((p) => p.kind === 'side_agent')
    return (
      <nav id={id} className={rootClass} aria-label="People">
        <div className="team-roster-top">
          <div className="stage-people-heading"><h2 className="team-roster-title">People</h2>{onClose !== undefined && <button type="button" className="team-icon-btn" aria-label="Close people panel" onClick={onClose}>×</button>}</div>
          <p className="team-roster-hint">Click someone to mention them in this feed.</p>
          <input type="search" aria-label="Search people" placeholder="Find a name or @alias…" value={search} onChange={(event) => onSearchChange?.(event.target.value)} />
          {pickHint !== undefined && <p className="team-roster-hint">{pickHint}</p>}
        </div>
        <RosterGroup id={`${id}-stage`} label="This stage" count={thisStageRows.length}>
          <ul className="team-roster-list">{thisStageRows}</ul>
        </RosterGroup>
        <RosterGroup id={`${id}-run`} label="Run agents" count={agents.length}>
          <ul className="team-roster-list">{agents.map((p) => renderRow(p))}</ul>
        </RosterGroup>
        {otherStages.length > 0 && (
          <RosterGroup id={`${id}-other`} label="Other stages" count={otherCount}>
            <ul className="team-roster-list">{otherStages.flatMap((stageId) => renderStage(stageId, visibleParticipants))}</ul>
          </RosterGroup>
        )}
        {sideAgents.length > 0 && <RosterGroup id={`${id}-assistant`} label="Assistant" count={sideAgents.length}>
          <ul className="team-roster-list">{sideAgents.map((p) => renderRow(p))}</ul>
        </RosterGroup>}
        <div className="team-add-agent-bottom">
          <button type="button" className="btn team-add-run-agent" onClick={onAddRunAgent} title="An agent of the whole run: it talks to every stage">
            <span aria-hidden="true">+ </span>Add run agent…
          </button>
          {onAttachHelper !== undefined && (
            <button type="button" className="btn team-attach-helper" onClick={onAttachHelper} title="A helper of this stage: it works for the stage's agent">
              <span aria-hidden="true">+ </span>Attach helper to stage…
            </button>
          )}
        </div>
      </nav>
    )
  }

  return (
    <nav id={id} className={rootClass} aria-label="Team roster">
      <TabList
        idPrefix={listTabsId}
        ariaLabel="Team navigation"
        className="team-list-tabs"
        tabClassName="team-list-tab"
        selected={listView}
        onSelect={(view) => onListViewChange?.(view)}
        tabs={[{ id: 'conversations', label: 'Conversations' }, { id: 'people', label: 'People' }]}
      />
      <label className="team-roster-search">
        <span aria-hidden="true">⌕</span>
        <input
          type="search"
          aria-label={listView === 'conversations' ? 'Search conversations' : 'Search people'}
          placeholder={listView === 'conversations' ? 'Search conversations…' : 'Search people…'}
          value={search}
          onChange={(event) => onSearchChange?.(event.target.value)}
        />
      </label>
      <div className="team-roster-actions">
        <button
          ref={actionButtonRef}
          type="button"
          className="team-roster-action"
          onClick={listView === 'conversations' ? (event) => onCreateConversation?.(event.currentTarget) : onAddRunAgent}
        >
          <span aria-hidden="true">+ </span>{listView === 'conversations' ? 'New conversation' : 'Add run agent'}
        </button>
        {listView === 'conversations' && onNewOnlyChange !== undefined && (
          <button ref={newFilterRef} type="button" className={`team-new-filter${newOnly ? ' active' : ''}`}
            aria-pressed={newOnly} onClick={() => onNewOnlyChange(!newOnly)}>
            New to you <span className="team-new-filter-count">{unreadCount}</span>
          </button>
        )}
      </div>
      {listView === 'conversations' ? (
        <div className="team-roster-results" {...tabPanelProps(listTabsId, 'conversations')}
          onFocusCapture={(event) => {
            focusedThreadRowRef.current = (event.target as HTMLElement).closest<HTMLButtonElement>('.team-thread-item')
          }}>
          <section className="team-roster-group" aria-labelledby={`${id}-conversations`}>
            <h3 id={`${id}-conversations`} className="team-sr-only">Conversations<span className="team-roster-count" aria-hidden="true">{threads.length}</span></h3>
            {visibleThreads.length === 0 ? (
              <div className="team-roster-empty-state">
                <p className="team-thread-list-empty empty-hint">{newOnly ? "You're all caught up" : query === '' ? 'No conversations yet' : 'No matching conversations'}</p>
                {!newOnly && query === '' && <button type="button" className="btn" onClick={(event) => onCreateConversation?.(event.currentTarget)}>Start a conversation</button>}
                {!newOnly && <button type="button" className="btn" onClick={() => {
                  onListViewChange?.('people')
                  document.getElementById(`${listTabsId}-tab-people`)?.focus()
                }}>Browse people</button>}
              </div>
            ) : (
              <ThreadList threads={visibleThreads} participants={participants} summaries={summaries} unreadThreadIds={unreadThreadIds} selectedThreadId={selectedThreadId} onSelect={(t) => onSelectThread?.(t)} />
            )}
          </section>
        </div>
      ) : (
        <div className="team-roster-results" {...tabPanelProps(listTabsId, 'people')}>
          <section className="team-roster-group" aria-labelledby={`${id}-run`}>
            <h3 id={`${id}-run`}>This run</h3>
            {visibleRunParticipants.length === 0 ? (
              <p className="team-roster-empty empty-hint">{query === '' ? 'No run-scoped agents yet' : 'No matching run agents'}</p>
            ) : (
              <ul className="team-roster-list">{visibleRunParticipants.map((p) => renderRow(p))}</ul>
            )}
          </section>
          {stageParticipants.length > 0 && (
            <section className="team-roster-group" aria-labelledby={`${id}-stages`}>
              <h3 id={`${id}-stages`}>
                <button type="button" className="team-stages-toggle" aria-expanded={stagesExpanded}
                  aria-controls={`${id}-stage-rows`} onClick={() => setStagesExpanded((value) => !value)}>
                  <span aria-hidden="true">{stagesExpanded ? '▾' : '▸'}</span> Stages
                </button>
              </h3>
              <div id={`${id}-stage-rows`} hidden={!stagesExpanded}>
                {visibleStageParticipants.length === 0 ? (
                  <p className="team-roster-empty empty-hint">{query === '' ? 'No stages yet' : 'No matching stage agents'}</p>
                ) : (
                  <ul className="team-roster-list">{stageOrder(visibleStageParticipants, stages).flatMap((stageId) => renderStage(stageId, visibleStageParticipants))}</ul>
                )}
              </div>
            </section>
          )}
        </div>
      )}
    </nav>
  )
}

// RosterGroup — one titled group of the people panel, with how many people it lists.
function RosterGroup({ id, label, count, children }: { id: string; label: string; count: number; children: ReactNode }): ReactElement {
  return (
    <section className="team-roster-group" aria-labelledby={id}>
      <h3 id={id}>
        {label}
        <span className="team-roster-count" aria-hidden="true">{count}</span>
      </h3>
      {count === 0 ? <p className="team-roster-empty empty-hint">No one yet</p> : children}
    </section>
  )
}
