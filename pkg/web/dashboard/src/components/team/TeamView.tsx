import { useId, useState, type ReactElement } from 'react'
import type { AfmEvent, Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import { openMeshThread, type MeshParticipant, type MeshThread } from '../../api/mesh-client'
import { useMeshBoard, type MeshRoster } from '../../hooks/use-mesh'
import { useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { directPeer, findExistingDirectThread, participantLabel, participantRole } from '../../lib/mesh-roster'
import { RunTeamList } from './RunTeamList'
import { conversationSubtitle, conversationTitle, targetKey, ThreadConversation, type ThreadConversationTarget } from './ThreadConversation'
import { MeshAvatar, ThreadSymbol } from './MeshAvatar'
import { BoardPanel, boardTaskCount } from './BoardPanel'
import { ActivityPanel } from './ActivityPanel'
import { AddRunAgentDialog } from './AddRunAgentDialog'
import { TabList, tabPanelProps } from '../tab-list'

export type TeamViewProps = {
  // roster — owned by App.tsx (ONE useMeshRoster poll for the whole dashboard, same "capability hook lives at
  // the top, passed down" convention useStageAssistants/useAssistantProfiles already follow), not fetched a
  // second time here. App.tsx also uses roster.enabled to decide whether the Team tab is even shown.
  roster: MeshRoster
  stages: Stage[]
  events: AfmEvent[]
  profiles: AssistantProfile[]
}

type TeamTab = 'messages' | 'board' | 'activity'

// mergeById — the roster's list plus local items it does not know yet (the roster wins once it has them).
function mergeById<T extends { id: string }>(fromRoster: T[], local: T[]): T[] {
  const missing = local.filter((l) => !fromRoster.some((r) => r.id === l.id))
  return missing.length === 0 ? fromRoster : [...fromRoster, ...missing]
}

// TeamView — Agent Mesh Phase 7, T7.3: the run-wide Team workspace. Owns its OWN Messages/Board/Activity
// tabs and conversation selection internally (WorkspaceView only ever needs to know "we're on 'team'" — see
// use-workspace-view/workspace-view.ts's openTeam — the same "manual read-only-ish view owns its own detail
// state" posture 'discussion' already established for StageDiscussionPanel/StageProposals).
export function TeamView({ roster, stages, events, profiles }: TeamViewProps): ReactElement {
  const [tab, setTab] = useState<TeamTab>('messages')
  const tabsId = useId()
  const board = useMeshBoard()
  const [activeConversation, setActiveConversation] = useState<ThreadConversationTarget | null>(null)
  const [addRunAgentOpen, setAddRunAgentOpen] = useState(false)
  // rosterOpen — the roster drawer on a narrow screen (CSS shows the toggle only there).
  const [rosterOpen, setRosterOpen] = useState(false)
  // localThreads / localParticipants — threads and run agents this dashboard just created that the next
  // roster poll (every 3s, see useMeshRoster) has not reported yet. Every lookup and the roster UI use the
  // merged lists, so a person clicked before that poll lands in the thread that already exists (no second
  // direct thread) and a new agent shows its name, not its id. Filtered against the roster rather than pruned
  // on poll, so this needs no effect and is correct the instant the roster catches up.
  const [localThreads, setLocalThreads] = useState<MeshThread[]>([])
  const [localParticipants, setLocalParticipants] = useState<MeshParticipant[]>([])

  const [createOpen, setCreateOpen] = useState(false)
  const [createTopic, setCreateTopic] = useState('')
  const [createSelected, setCreateSelected] = useState<Set<string>>(new Set())
  // createInvalid — the form was submitted without a topic or a participant.
  const [createInvalid, setCreateInvalid] = useState(false)
  // createSubmitter — a retry of the same form reuses its request id (a lost response must not produce a
  // second thread). Fenced by the selection: a creation that finishes after the user picked another
  // conversation does not switch away from it.
  const createSubmitter = useRetryableSubmit<{ title: string; memberIds: string[] }>('mesh-conversation', targetKey(activeConversation))

  const threads = mergeById(roster.threads, localThreads)
  const participants = mergeById(roster.participants, localParticipants)

  function handleSelectParticipant(p: MeshParticipant): void {
    setRosterOpen(false)
    setCreateOpen(false)
    const existing = findExistingDirectThread(threads, p.id)
    setActiveConversation(existing ? { kind: 'thread', thread: existing } : { kind: 'direct', participant: p })
    setTab('messages')
  }

  function rememberThread(thread: MeshThread): void {
    setLocalThreads((prev) => (prev.some((t) => t.id === thread.id) ? prev : [...prev, thread]))
  }

  function handleThreadOpened(thread: MeshThread): void {
    rememberThread(thread)
    setActiveConversation({ kind: 'thread', thread })
  }

  function handleSelectThread(t: MeshThread): void {
    setRosterOpen(false)
    setCreateOpen(false)
    setActiveConversation({ kind: 'thread', thread: t })
    setTab('messages')
  }

  function toggleCreateParticipant(id: string): void {
    setCreateSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function resetCreateForm(): void {
    setCreateOpen(false)
    setCreateTopic('')
    setCreateSelected(new Set())
    setCreateInvalid(false)
  }

  function submitCreate(): void {
    const topic = createTopic.trim()
    if (topic === '' || createSelected.size === 0) {
      setCreateInvalid(true)
      return
    }
    setCreateInvalid(false)
    createSubmitter.submit(
      { title: topic, memberIds: [...createSelected].sort() },
      async (p, requestId) => {
        const thread = await openMeshThread({ requestId, title: p.title, scope: 'run', memberIds: [...p.memberIds] })
        // Known to the roster right away, even when the user has moved on to another conversation meanwhile.
        rememberThread(thread)
        return thread
      },
      {
        onSuccess: (thread) => {
          setActiveConversation({ kind: 'thread', thread })
          resetCreateForm()
        },
      },
    )
  }

  const createError = createInvalid ? 'Enter a topic and select at least one participant.' : createSubmitter.error

  if (roster.probed && !roster.enabled) {
    return <div className="team-view-disabled empty-hint">Team is not available for this run</div>
  }

  // addressable — who a new conversation can include: a finished stage or helper cannot answer any more.
  const addressable = participants.filter((p) => p.kind !== 'user' && p.reachable)
  const teamStages = stages.map((s) => ({ id: s.id, title: s.name !== '' ? s.name : s.id, status: s.status }))

  return (
    <div className="team-view">
      <RunTeamList
        participants={participants}
        threads={threads}
        stages={teamStages}
        selectedParticipantId={activeConversation?.kind === 'direct' ? activeConversation.participant.id : null}
        selectedThreadId={activeConversation?.kind === 'thread' ? activeConversation.thread.id : null}
        onSelectParticipant={handleSelectParticipant}
        onSelectThread={handleSelectThread}
        onAddRunAgent={() => {
          setRosterOpen(false)
          setAddRunAgentOpen(true)
        }}
        open={rosterOpen}
      />
      {rosterOpen && <button type="button" className="team-roster-scrim" aria-label="Close team roster" onClick={() => setRosterOpen(false)} />}
      <div className="team-main">
        <header className="team-conversation-header">
          <button
            type="button"
            className="team-icon-btn team-roster-toggle"
            aria-label="Show team roster"
            aria-controls="team-roster"
            aria-expanded={rosterOpen}
            onClick={() => setRosterOpen((v) => !v)}
          >
            ☰
          </button>
          <ConversationSymbol target={activeConversation} participants={participants} />
          <div className="team-conversation-heading">
            <h3>{activeConversation === null ? 'Team' : conversationTitle(activeConversation, participants)}</h3>
            <p>{activeConversation === null ? 'Select a person or a conversation' : conversationSubtitle(activeConversation, participants)}</p>
          </div>
          {!createOpen && (
            <button
              type="button"
              className="team-icon-btn"
              aria-label="Create conversation"
              title="Create conversation"
              onClick={() => {
                setTab('messages')
                setCreateOpen(true)
              }}
            >
              +
            </button>
          )}
        </header>
        <TabList
          idPrefix={tabsId}
          ariaLabel="Team"
          className="team-tabs"
          tabClassName="team-tab"
          selected={tab}
          onSelect={setTab}
          tabs={[
            { id: 'messages', label: 'Messages' },
            {
              id: 'board',
              label: (
                <>
                  Board
                  <span className="team-tab-count" aria-hidden="true">
                    {boardTaskCount(board.entries)}
                  </span>
                </>
              ),
            },
            { id: 'activity', label: 'Activity' },
          ]}
        />
        {tab === 'messages' && (
          <div className="team-messages" {...tabPanelProps(tabsId, 'messages')}>
            {createOpen && (
              <div className="team-create-conversation">
                <div className="team-create-conversation-form">
                  <label className="team-create-conversation-topic">
                    <span>Topic</span>
                    <input
                      type="text"
                      value={createTopic}
                      onChange={(e) => setCreateTopic(e.target.value)}
                      placeholder="What is this about?"
                    />
                  </label>
                  <fieldset className="team-create-conversation-participants">
                    <legend>Participants</legend>
                    {addressable.length === 0 ? (
                      <p className="empty-hint">No one to add yet</p>
                    ) : (
                      addressable.map((p) => {
                        const name = participantLabel(p)
                        return (
                          <label key={p.id} className="team-create-conversation-participant">
                            <MeshAvatar name={name} kind={p.kind} />
                            <span className="team-create-conversation-participant-text">
                              <span className="team-create-conversation-participant-name">{name}</span>
                              <span className="team-create-conversation-participant-role">{participantRole(p.kind)}</span>
                            </span>
                            <input
                              type="checkbox"
                              checked={createSelected.has(p.id)}
                              onChange={() => toggleCreateParticipant(p.id)}
                            />
                          </label>
                        )
                      })
                    )}
                  </fieldset>
                  {createError !== null && (
                    <div className="team-create-conversation-error" role="alert">
                      {createError}
                    </div>
                  )}
                  <div className="team-create-conversation-actions">
                    <button type="button" className="btn btn-cancel" onClick={resetCreateForm}>
                      Cancel
                    </button>
                    <button type="button" className="btn btn-send" disabled={createSubmitter.pending} onClick={submitCreate}>
                      {createSubmitter.pending ? 'Creating…' : 'Create conversation'}
                    </button>
                  </div>
                </div>
              </div>
            )}
            <ThreadConversation
              key={targetKey(activeConversation)}
              target={activeConversation}
              participants={participants}
              threads={threads}
              onThreadOpened={handleThreadOpened}
              onThreadCreated={rememberThread}
            />
          </div>
        )}
        {tab === 'board' && (
          <div className="team-tab-panel" {...tabPanelProps(tabsId, 'board')}>
            <BoardPanel board={board} participants={participants} stages={teamStages} />
          </div>
        )}
        {tab === 'activity' && (
          <div className="team-tab-panel" {...tabPanelProps(tabsId, 'activity')}>
            <ActivityPanel events={events} />
          </div>
        )}
      </div>
      {addRunAgentOpen && (
        <AddRunAgentDialog
          profiles={profiles}
          onAttached={(result, name) => {
            setAddRunAgentOpen(false)
            // Show the new agent by name and open its conversation right away (the next poll confirms both).
            setLocalParticipants((prev) => [
              ...prev,
              { id: result.participant_id, kind: 'run_agent', scope: 'run', display_name: name, reachable: true },
            ])
            handleThreadOpened({ id: result.thread_id, title: name, scope: 'run', members: ['user', result.participant_id], status: 'open' })
            setTab('messages')
          }}
          onCancel={() => setAddRunAgentOpen(false)}
        />
      )}
    </div>
  )
}

// ConversationSymbol — the conversation header's picture: the person's avatar for a one-to-one conversation
// (picked from the roster or opened from the list), the group symbol otherwise.
function ConversationSymbol({ target, participants }: { target: ThreadConversationTarget | null; participants: MeshParticipant[] }): ReactElement {
  const person = target?.kind === 'direct' ? target.participant : target?.kind === 'thread' ? directPeer(target.thread, participants) : undefined
  if (person !== undefined) return <MeshAvatar name={participantLabel(person)} kind={person.kind} size="large" />
  return <ThreadSymbol />
}
