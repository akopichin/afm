import { useRef, useState, type ReactElement } from 'react'
import type { AfmEvent, Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import { genMeshRequestId, openMeshThread, type MeshParticipant, type MeshThread } from '../../api/mesh-client'
import type { MeshRoster } from '../../hooks/use-mesh'
import { findExistingDirectThread, participantLabel, participantRole } from '../../lib/mesh-roster'
import { RunTeamList } from './RunTeamList'
import { conversationSubtitle, conversationTitle, targetKey, ThreadConversation, type ThreadConversationTarget } from './ThreadConversation'
import { MeshAvatar } from './MeshAvatar'
import { BoardPanel } from './BoardPanel'
import { ActivityPanel } from './ActivityPanel'
import { AddRunAgentDialog } from './AddRunAgentDialog'

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
  const [createError, setCreateError] = useState<string | null>(null)
  const [createSubmitting, setCreateSubmitting] = useState(false)

  // createRequestIdRef — one id per "Create conversation" form, reused by retries (a lost response must not
  // produce a second thread); a new form gets a new id.
  const createRequestIdRef = useRef(genMeshRequestId('mesh-conversation'))

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
    createRequestIdRef.current = genMeshRequestId('mesh-conversation')
    setCreateOpen(false)
    setCreateTopic('')
    setCreateSelected(new Set())
    setCreateError(null)
  }

  function submitCreate(): void {
    const topic = createTopic.trim()
    if (topic === '' || createSelected.size === 0) {
      setCreateError('Enter a topic and select at least one participant.')
      return
    }
    setCreateError(null)
    setCreateSubmitting(true)
    openMeshThread({ requestId: createRequestIdRef.current, title: topic, scope: 'run', memberIds: [...createSelected] })
      .then((thread) => {
        handleThreadOpened(thread)
        resetCreateForm()
      })
      .catch((err: unknown) => setCreateError(err instanceof Error ? err.message : 'request failed'))
      .finally(() => setCreateSubmitting(false))
  }

  if (roster.probed && !roster.enabled) {
    return <div className="team-view-disabled empty-hint">Team is not available for this run</div>
  }

  const addressable = participants.filter((p) => p.kind !== 'user')
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
          {activeConversation?.kind === 'direct' ? (
            <MeshAvatar name={conversationTitle(activeConversation, participants)} kind={activeConversation.participant.kind} size="large" />
          ) : (
            <span className="team-thread-symbol" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
                <circle cx="9" cy="7" r="4" />
                <path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" />
              </svg>
            </span>
          )}
          <div className="team-conversation-heading">
            <h3>{activeConversation === null ? 'Team' : conversationTitle(activeConversation, participants)}</h3>
            <p>{activeConversation === null ? 'Select a person or a conversation' : conversationSubtitle(activeConversation)}</p>
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
        <div className="team-tabs" role="tablist" aria-label="Team">
          {(['messages', 'board', 'activity'] as const).map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              className={`team-tab${tab === t ? ' active' : ''}`}
              onClick={() => setTab(t)}
            >
              {t === 'messages' ? 'Messages' : t === 'board' ? 'Board' : 'Activity'}
            </button>
          ))}
        </div>
        {tab === 'messages' && (
          <div className="team-messages">
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
                    <button type="button" className="btn btn-send" disabled={createSubmitting} onClick={submitCreate}>
                      {createSubmitting ? 'Creating…' : 'Create conversation'}
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
        {tab === 'board' && <BoardPanel />}
        {tab === 'activity' && <ActivityPanel events={events} />}
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
