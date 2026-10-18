import { useState, type ReactElement } from 'react'
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

// TeamView — Agent Mesh Phase 7, T7.3: the run-wide Team workspace. Owns its OWN Messages/Board/Activity
// tabs and conversation selection internally (WorkspaceView only ever needs to know "we're on 'team'" — see
// use-workspace-view/workspace-view.ts's openTeam — the same "manual read-only-ish view owns its own detail
// state" posture 'discussion' already established for StageDiscussionPanel/StageProposals).
export function TeamView({ roster, stages, events, profiles }: TeamViewProps): ReactElement {
  const [tab, setTab] = useState<TeamTab>('messages')
  const [activeConversation, setActiveConversation] = useState<ThreadConversationTarget | null>(null)
  const [addRunAgentOpen, setAddRunAgentOpen] = useState(false)
  // pendingThreads — direct threads opened via ThreadConversation's onThreadOpened that the next roster poll
  // (every 3s, see useMeshRoster) hasn't reported yet. Without this, re-selecting the same person row between
  // a successful first send and that poll finds nothing in roster.threads, falls back to a fresh 'direct'
  // target, and the next send mints a SECOND thread via openMeshThread. Filtered against roster.threads below
  // rather than pruned on poll, so it never needs an effect and stays correct the instant the roster catches up.
  const [pendingThreads, setPendingThreads] = useState<MeshThread[]>([])

  const [createOpen, setCreateOpen] = useState(false)
  const [createTopic, setCreateTopic] = useState('')
  const [createSelected, setCreateSelected] = useState<Set<string>>(new Set())
  const [createError, setCreateError] = useState<string | null>(null)
  const [createSubmitting, setCreateSubmitting] = useState(false)

  const unconfirmedThreads = pendingThreads.filter((p) => !roster.threads.some((t) => t.id === p.id))
  const searchableThreads = unconfirmedThreads.length === 0 ? roster.threads : [...roster.threads, ...unconfirmedThreads]

  function handleSelectParticipant(p: MeshParticipant): void {
    setCreateOpen(false)
    const existing = findExistingDirectThread(searchableThreads, p.id)
    setActiveConversation(existing ? { kind: 'thread', thread: existing } : { kind: 'direct', participant: p })
    setTab('messages')
  }

  function handleThreadOpened(thread: MeshThread): void {
    setActiveConversation({ kind: 'thread', thread })
    setPendingThreads((prev) => (prev.some((t) => t.id === thread.id) ? prev : [...prev, thread]))
  }

  function handleSelectThread(t: MeshThread): void {
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
    openMeshThread({ requestId: genMeshRequestId('mesh-conversation'), title: topic, scope: 'run', memberIds: [...createSelected] })
      .then((thread) => {
        setActiveConversation({ kind: 'thread', thread })
        resetCreateForm()
      })
      .catch((err: unknown) => setCreateError(err instanceof Error ? err.message : 'request failed'))
      .finally(() => setCreateSubmitting(false))
  }

  if (roster.probed && !roster.enabled) {
    return <div className="team-view-disabled empty-hint">Team is not available for this run</div>
  }

  const addressable = roster.participants.filter((p) => p.kind !== 'user')
  const teamStages = stages.map((s) => ({ id: s.id, title: s.name !== '' ? s.name : s.id, status: s.status }))

  return (
    <div className="team-view">
      <RunTeamList
        participants={roster.participants}
        threads={roster.threads}
        stages={teamStages}
        selectedParticipantId={activeConversation?.kind === 'direct' ? activeConversation.participant.id : null}
        selectedThreadId={activeConversation?.kind === 'thread' ? activeConversation.thread.id : null}
        onSelectParticipant={handleSelectParticipant}
        onSelectThread={handleSelectThread}
        onAddRunAgent={() => setAddRunAgentOpen(true)}
      />
      <div className="team-main">
        <header className="team-conversation-header">
          {activeConversation?.kind === 'direct' ? (
            <MeshAvatar name={conversationTitle(activeConversation, roster.participants)} kind={activeConversation.participant.kind} size="large" />
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
            <h3>{activeConversation === null ? 'Team' : conversationTitle(activeConversation, roster.participants)}</h3>
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
              participants={roster.participants}
              threads={searchableThreads}
              onThreadOpened={handleThreadOpened}
            />
          </div>
        )}
        {tab === 'board' && <BoardPanel />}
        {tab === 'activity' && <ActivityPanel events={events} />}
      </div>
      {addRunAgentOpen && (
        <AddRunAgentDialog
          profiles={profiles}
          onAttached={(result) => {
            setAddRunAgentOpen(false)
            // Open the new agent's conversation right away (the next roster poll confirms it).
            handleThreadOpened({ id: result.thread_id, scope: 'run', members: ['user', result.participant_id], status: 'open' })
            setTab('messages')
          }}
          onCancel={() => setAddRunAgentOpen(false)}
        />
      )}
    </div>
  )
}
