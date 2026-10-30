import { useEffect, useId, useRef, useState, type CSSProperties, type ReactElement, type SetStateAction } from 'react'
import type { AfmEvent, Stage } from '../../types'
import type { AssistantProfile } from '../../types/assistant-profile'
import { genMeshRequestId, getMeshThread, MeshApiError, openMeshThread, updateMeshThreadTitle, type MeshParticipant, type MeshSendReceipt, type MeshThread, type MeshThreadSummaries } from '../../api/mesh-client'
import type { MeshBoard, MeshRoster } from '../../hooks/use-mesh'
import { removeSubmittedText, useRetryableSubmit } from '../../hooks/use-retryable-submit'
import { boardTaskCountOf } from '../../lib/board-tasks'
import { directPeer, findExistingDirectThread, mergeById, participantLabel, participantRole } from '../../lib/mesh-roster'
import { orderInboxThreads } from '../../lib/team-inbox'
import { advanceTeamSeen, baselineTeamUnread, isTeamUnread } from '../../lib/team-unread'
import { RunTeamList, teamStagesOf } from './RunTeamList'
import { conversationSubtitle, conversationTitle, targetKey, ThreadConversation, type ThreadConversationTarget, type ConversationSession } from './ThreadConversation'
import { MeshAvatar, ThreadSymbol } from './MeshAvatar'
import { BoardPanel } from './BoardPanel'
import { BoardLoadState } from './BoardLoadState'
import { ActivityPanel } from './ActivityPanel'
import { AddRunAgentDialog } from './AddRunAgentDialog'
import { TabList, tabPanelProps } from '../tab-list'
import { sideAgentFailures } from '../feed-workspace/mesh-receipt'
import { usePanelResize } from '../layout/use-panel-resize'

export type TeamViewProps = {
  runId: string
  // active — Team is the visible workspace view. App keeps Team mounted while another view is shown, and a
  // hidden Team must not mark anything read. Defaults to true for standalone use.
  active?: boolean
  // roster — owned by App.tsx (ONE useMeshRoster poll for the whole dashboard, same "capability hook lives at
  // the top, passed down" convention useStageAssistants/useAssistantProfiles already follow), not fetched a
  // second time here. App.tsx also uses roster.enabled to decide whether the Team tab is even shown.
  // roster.participants is already merged with the run agents added in this dashboard that the poll has not
  // reported yet, so every view of the roster (people panel, Board tab, this view) reads ONE list.
  roster: MeshRoster
  // onParticipantAdded — hands a run agent just attached from the "Add run agent" dialog to that shared list.
  onParticipantAdded: (participant: MeshParticipant) => void
  // board — also owned by App.tsx (ONE useMeshBoard poll shared with the run-wide Board tab); the Board tab
  // here and its task count both read it.
  board: MeshBoard
  stages: Stage[]
  events: AfmEvent[]
  profiles: AssistantProfile[]
}

type TeamTab = 'messages' | 'board' | 'activity'
type TeamListView = 'conversations' | 'people'

// The thread read returns the saved, redacted body. Keep the roster's plain-text preview shape.
function savedPreview(body: string): string {
  return Array.from(body.replace(/[\s\u0085]+/gu, ' ').trim()).slice(0, 160).join('')
}

// TeamView — Agent Mesh Phase 7, T7.3: the run-wide Team workspace. Owns its OWN Messages/Board/Activity
// tabs and conversation selection internally (WorkspaceView only ever needs to know "we're on 'team'" — see
// use-workspace-view/workspace-view.ts's openTeam — a manual view that owns its own detail state).
export function TeamView({ runId, active = true, roster, onParticipantAdded, board, stages, events, profiles }: TeamViewProps): ReactElement {
  const [tab, setTab] = useState<TeamTab>('messages')
  const visitedTabs = useRef<Set<TeamTab>>(new Set(['messages']))
  const [listView, setListView] = useState<TeamListView>('conversations')
  const [listSearch, setListSearch] = useState('')
  const [newOnly, setNewOnly] = useState(false)
  const [readyRunId, setReadyRunId] = useState<string | null>(null)
  const [, setCursorVersion] = useState(0)
  const listActionRef = useRef<HTMLButtonElement>(null)
  const createOpenerRef = useRef<HTMLButtonElement | null>(null)
  const createTopicRef = useRef<HTMLInputElement>(null)
  const teamMainRef = useRef<HTMLDivElement>(null)
  const backButtonRef = useRef<HTMLButtonElement>(null)
  const pendingReturnFocusRef = useRef<HTMLButtonElement | null>(null)
  const focusConversationRef = useRef(false)
  const returnToListFocusRef = useRef(false)
  const lastListFocusRef = useRef<HTMLElement | null>(null)
  const tabsId = useId()
  const [activeConversation, setActiveConversation] = useState<ThreadConversationTarget | null>(null)
  const [editingThreadId, setEditingThreadId] = useState<string | null>(null)
  const editTopicButtonRef = useRef<HTMLButtonElement>(null)
  const returnToEditTopicRef = useRef(false)
  const autoSelectionDoneRef = useRef(false)
  const userInteractedRef = useRef(false)
  const [addRunAgentOpen, setAddRunAgentOpen] = useState(false)
  // Narrow Team starts on the list. The conversation stays mounted while the list is shown.
  const [mobileDetail, setMobileDetail] = useState(false)
  const resize = usePanelResize({
    storageKey: 'afm.teamRosterWidth', defaultWidth: 260, container: 'self', direction: 1,
    drawerQuery: '(max-width: 760px)', drawerBelow: 560, toggleSelector: '.team-header .team-tab.active',
  })
  // localThreads — threads this dashboard just created that the next roster poll (every 3s, see
  // useMeshRoster) has not reported yet. Every lookup uses the merged list, so a person clicked before that
  // poll lands in the thread that already exists (no second direct thread). Filtered against the roster rather
  // than pruned on poll, so this needs no effect and is correct the instant the roster catches up.
  const [localThreads, setLocalThreads] = useState<MeshThread[]>([])
  // A lost open response must keep the same idempotency key when the direct view is remounted.
  const pendingDirectOpenIds = useRef<Map<string, string>>(new Map())
  // Send IDs also survive a keyed remount. Record which real thread a direct send resolved to, so another
  // untitled thread with the same peer never inherits its retry ID or draft.
  const directThreadPeers = useRef(new Map<string, string>())
  const pendingSendIds = useRef(new Map<string, { conversation: string; requestId: string; attempt: number }>())
  const sendAttempt = useRef(0)
  const [localSummaries, setLocalSummaries] = useState<MeshThreadSummaries>({})
  const rosterSummaries = roster.summaries ?? {}
  useEffect(() => {
    if (!roster.probed || !roster.enabled || runId === '' || (roster.runId !== undefined && roster.runId !== runId)) return
    baselineTeamUnread(runId, rosterSummaries)
    setReadyRunId(runId)
  }, [runId, roster.probed, roster.enabled, rosterSummaries])
  const rosterSummariesRef = useRef(rosterSummaries)
  // Remember a durable roster summary once it catches a local receipt. A later stale poll cannot move it back.
  useEffect(() => {
    rosterSummariesRef.current = rosterSummaries
    setLocalSummaries((prev) => {
      let next = prev
      for (const [id, local] of Object.entries(prev)) {
        const durable = rosterSummaries[id]
        if (durable?.last_message && durable.last_message.seq >= (local.last_message?.seq ?? 0)) {
          if (next === prev) next = { ...prev }
          next[id] = { ...durable, last_other_message_seq: Math.max(local.last_other_message_seq, durable.last_other_message_seq) }
        }
      }
      return next
    })
  }, [rosterSummaries])
  // drafts — each conversation's unsent text by targetKey. Kept here, above the keyed <ThreadConversation>,
  // because a direct conversation's first send remounts it under its new thread's key (handleThreadOpened
  // moves the draft along), and text typed while that send was in flight must not be lost.
  const sessions = useRef<Record<string, ConversationSession>>({})
  const [, refreshReplyTarget] = useState(0)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const activeKey = targetKey(activeConversation)

  useEffect(() => {
    if (editingThreadId !== null || !returnToEditTopicRef.current) return
    editTopicButtonRef.current?.focus()
    returnToEditTopicRef.current = false
  }, [editingThreadId])

  function closeTopicEditor(): void {
    returnToEditTopicRef.current = true
    setEditingThreadId(null)
  }

  function selectTab(next: TeamTab): void {
    visitedTabs.current.add(next)
    setTab(next)
    if (resize.isDrawer) setMobileDetail(next !== 'messages' || activeConversation !== null)
  }

  const [createOpen, setCreateOpen] = useState(false)
  const [createTopic, setCreateTopic] = useState('')
  const [createSelected, setCreateSelected] = useState<Set<string>>(new Set())
  // createInvalid — the form was submitted without a participant.
  const [createInvalid, setCreateInvalid] = useState(false)
  // createSubmitter — a retry of the same form reuses its request id (a lost response must not produce a
  // second thread). Fenced by the selection: a creation that finishes after the user picked another
  // conversation does not switch away from it.
  const createSubmitter = useRetryableSubmit<{ title: string; memberIds: string[] }>('mesh-conversation', activeKey)

  useEffect(() => {
    if (createOpen) createTopicRef.current?.focus()
  }, [createOpen])

  useEffect(() => {
    if (createOpen || addRunAgentOpen) return
    if (pendingReturnFocusRef.current !== null && (!resize.isDrawer || !mobileDetail)) {
      const opener = pendingReturnFocusRef.current
      const focusTarget = opener.isConnected ? opener : listActionRef.current
      focusTarget?.focus()
      pendingReturnFocusRef.current = null
    }
    if (focusConversationRef.current) {
      const composer = teamMainRef.current?.querySelector<HTMLTextAreaElement>('.chat-composer-input')
      const focusTarget = composer && !composer.disabled ? composer : backButtonRef.current
      focusTarget?.focus()
      focusConversationRef.current = false
    }
  }, [createOpen, addRunAgentOpen, mobileDetail, resize.isDrawer, activeConversation])

  const threads = mergeById(roster.threads, localThreads)
  const participants = roster.participants
  const summaries: MeshThreadSummaries = { ...rosterSummaries }
  for (const [id, local] of Object.entries(localSummaries)) {
    const durable = summaries[id]
    summaries[id] = {
      ...((local.last_message?.seq ?? 0) > (durable?.last_message?.seq ?? 0) ? local : durable ?? local),
      last_other_message_seq: Math.max(local.last_other_message_seq, durable?.last_other_message_seq ?? 0),
      title_seq: Math.max(local.title_seq, durable?.title_seq ?? 0),
    }
  }
  const inboxThreads = orderInboxThreads(threads, summaries)
  const unreadThreadIds = new Set<string>()
  for (const thread of inboxThreads) {
    const summary = summaries[thread.id]
    if (summary === undefined) continue
    if (isTeamUnread(runId, thread.id, summary.last_other_message_seq)) unreadThreadIds.add(thread.id)
  }
  const selectedConversation = activeConversation?.kind === 'thread'
    ? { kind: 'thread' as const, thread: threads.find((t) => t.id === activeConversation.thread.id) ?? activeConversation.thread }
    : activeConversation

  const wasDrawerRef = useRef(resize.isDrawer)
  useEffect(() => {
    // A hidden Team ignores width changes: the list/detail choice the reader left is kept for their return.
    if (!active) return
    if (!wasDrawerRef.current && resize.isDrawer) {
      const roster = document.getElementById('team-roster')
      const focusedRoster = roster?.contains(document.activeElement) ?? false
      setMobileDetail(!focusedRoster && (tab !== 'messages' || selectedConversation !== null))
    }
    wasDrawerRef.current = resize.isDrawer
  }, [active, resize.isDrawer, selectedConversation, tab])

  useEffect(() => {
    if (!returnToListFocusRef.current || !resize.isDrawer || mobileDetail) return
    returnToListFocusRef.current = false
    const roster = document.getElementById('team-roster')
    const focusTarget = lastListFocusRef.current?.isConnected ? lastListFocusRef.current
      : roster?.querySelector<HTMLElement>('.team-thread-item.active, .team-person-row.active')
        ?? roster?.querySelector<HTMLElement>('.team-list-tab.active')
    focusTarget?.focus()
  }, [resize.isDrawer, mobileDetail])

  function showDetail(): void {
    const roster = document.getElementById('team-roster')
    lastListFocusRef.current = roster?.contains(document.activeElement) ? document.activeElement as HTMLElement : null
    setMobileDetail(true)
  }

  useEffect(() => {
    if (autoSelectionDoneRef.current || !roster.probed || !roster.enabled || inboxThreads.length === 0) return
    autoSelectionDoneRef.current = true
    if (!resize.isDrawer && !userInteractedRef.current && activeConversation === null) {
      setActiveConversation({ kind: 'thread', thread: inboxThreads[0]! })
    }
  }, [roster.probed, roster.enabled, inboxThreads, resize.isDrawer, activeConversation])

  function handleSelectParticipant(p: MeshParticipant): void {
    setEditingThreadId(null)
    returnToEditTopicRef.current = false
    focusConversationRef.current = true
    if (resize.isDrawer) showDetail()
    setCreateOpen(false)
    const existing = findExistingDirectThread(threads, p.id, summaries)
    if (existing !== null) {
      pendingDirectOpenIds.current.delete(p.id)
      moveDirectDraft(p.id, existing)
    }
    setActiveConversation(existing ? { kind: 'thread', thread: existing } : { kind: 'direct', participant: p })
    if (!resize.isDrawer) {
      setListView('conversations')
      setListSearch('')
    }
    setTab('messages')
  }

  function rememberThread(thread: MeshThread): void {
    setLocalThreads((prev) => (prev.some((t) => t.id === thread.id) ? prev : [...prev, thread]))
  }

  function getDirectOpenRequestId(participantId: string): string {
    let requestId = pendingDirectOpenIds.current.get(participantId)
    if (requestId === undefined) {
      requestId = genMeshRequestId('mesh-open')
      pendingDirectOpenIds.current.set(participantId, requestId)
    }
    return requestId
  }

  function handleDirectThreadResolved(participantId: string, thread: MeshThread): void {
    pendingDirectOpenIds.current.delete(participantId)
    directThreadPeers.current.set(thread.id, participantId)
    rememberThread(thread)
  }

  function moveDirectDraft(participantId: string, thread: MeshThread): void {
    const directKey = `direct:${participantId}`
    const threadKey = targetKey({ kind: 'thread', thread })
    setDrafts((prev) => {
      const directDraft = prev[directKey]
      if (directDraft === undefined) return prev
      const next = { ...prev }
      delete next[directKey]
      if (next[threadKey] === undefined || directDraft !== '') next[threadKey] = directDraft
      return next
    })
  }

  function getSendRequestId(threadId: string, recipientIds: string[], body: string, proposedId: string): string {
    const conversation = `thread:${threadId}`
    const key = JSON.stringify([conversation, recipientIds, body])
    const requestId = pendingSendIds.current.get(key)?.requestId ?? proposedId
    pendingSendIds.current.set(key, { conversation, requestId, attempt: ++sendAttempt.current })
    return requestId
  }

  function handleSendSucceeded(target: ThreadConversationTarget, thread: MeshThread, submitted: string, requestId: string): void {
    const conversation = `thread:${thread.id}`
    const entry = [...pendingSendIds.current.values()].find((value) => value.conversation === conversation && value.requestId === requestId)
    if (entry !== undefined) {
      for (const [key, value] of pendingSendIds.current) {
        if (value.conversation === conversation && value.attempt <= entry.attempt) pendingSendIds.current.delete(key)
      }
    }
    const keys = target.kind === 'direct' ? [`direct:${target.participant.id}`, `thread:${thread.id}`] : [`thread:${thread.id}`]
    setDrafts((prev) => {
      const next = { ...prev }
      for (const key of keys) {
        if (next[key] !== undefined) next[key] = removeSubmittedText(next[key], submitted)
      }
      return next
    })
  }

  function setActiveDraft(action: SetStateAction<string>): void {
    setDrafts((prev) => ({ ...prev, [activeKey]: typeof action === 'function' ? action(prev[activeKey] ?? '') : action }))
  }

  function openThread(thread: MeshThread): void {
    rememberThread(thread)
    setActiveConversation({ kind: 'thread', thread })
  }

  // handleThreadOpened — the selected direct conversation's first send opened this thread: the conversation
  // continues there, and what is left of its draft moves along with it.
  function handleThreadOpened(thread: MeshThread): void {
    const threadKey = targetKey({ kind: 'thread', thread })
    setDrafts(({ [activeKey]: carried = '', ...rest }) => ({ ...rest, [threadKey]: carried }))
    if (sessions.current[activeKey] !== undefined) {
      sessions.current[threadKey] = sessions.current[activeKey]!
      delete sessions.current[activeKey]
    }
    openThread(thread)
  }

  function handleMessageSent(thread: MeshThread, receipt: MeshSendReceipt): void {
    const rosterSummary = rosterSummariesRef.current[thread.id]
    setLocalSummaries((prev) => {
      const current = prev[thread.id]
      if (receipt.seq <= Math.max(current?.last_message?.seq ?? 0, rosterSummary?.last_message?.seq ?? 0)) return prev
      return { ...prev, [thread.id]: {
        last_message: { id: receipt.message_id, seq: receipt.seq, sender_id: 'user', preview: 'Message sent', created_at: new Date().toISOString() },
        last_other_message_seq: Math.max(current?.last_other_message_seq ?? 0, rosterSummary?.last_other_message_seq ?? 0),
        title_seq: Math.max(current?.title_seq ?? 0, rosterSummary?.title_seq ?? 0),
      } }
    })
    // This read is independent of the committed send. A transient failure leaves the safe receipt preview
    // until the serial roster poll supplies the saved message.
    void getMeshThread(thread.id, Math.max(0, receipt.seq - 1)).then(({ messages }) => {
      const saved = messages.find((message) => message.id === receipt.message_id && message.seq === receipt.seq)
      if (!saved) return
      setLocalSummaries((prev) => {
        const current = prev[thread.id]
        const durable = rosterSummariesRef.current[thread.id]
        if (current?.last_message?.id !== receipt.message_id || current.last_message.seq !== receipt.seq ||
            (durable?.last_message?.seq ?? 0) >= receipt.seq) return prev
        return { ...prev, [thread.id]: {
          ...current,
          last_message: { id: saved.id, seq: saved.seq, sender_id: saved.sender_id, preview: savedPreview(saved.body), created_at: saved.created_at },
        } }
      })
    }, () => { /* The send is already committed; the next roster poll can reconcile it. */ })
  }

  function handleSelectThread(t: MeshThread): void {
    setEditingThreadId(null)
    returnToEditTopicRef.current = false
    if (resize.isDrawer) {
      showDetail()
      focusConversationRef.current = true
    }
    setCreateOpen(false)
    const peerId = directThreadPeers.current.get(t.id)
    if (peerId) moveDirectDraft(peerId, t)
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

  function resetCreateForm(returnFocus = false): void {
    if (returnFocus) {
      pendingReturnFocusRef.current = createOpenerRef.current
      if (resize.isDrawer) setMobileDetail(false)
    }
    setCreateOpen(false)
    setCreateTopic('')
    setCreateSelected(new Set())
    setCreateInvalid(false)
    setListView('conversations')
    createOpenerRef.current = null
  }

  function submitCreate(): void {
    const topic = createTopic.trim()
    if (createSelected.size === 0) {
      setCreateInvalid(true)
      return
    }
    setCreateInvalid(false)
    const memberIds = [...createSelected].sort()
    const directParticipantId = topic === '' && memberIds.length === 1 ? memberIds[0] : undefined
    createSubmitter.submit(
      { title: topic, memberIds },
      async (p, requestId) => {
        if (p.title === '' && p.memberIds.length === 1) {
          const participantId = p.memberIds[0]!
          const existing = findExistingDirectThread(threads, participantId, summaries)
          if (existing !== null) {
            handleDirectThreadResolved(participantId, existing)
            return existing
          }
        }
        const pendingDirectId = p.title === '' && p.memberIds.length === 1
          ? pendingDirectOpenIds.current.get(p.memberIds[0]!) : undefined
        const thread = await openMeshThread({ requestId: pendingDirectId ?? requestId, title: p.title, scope: 'run', memberIds: [...p.memberIds] })
        // Known to the roster right away, even when the user has moved on to another conversation meanwhile.
        if (pendingDirectId !== undefined) handleDirectThreadResolved(p.memberIds[0]!, thread)
        else rememberThread(thread)
        return thread
      },
      {
        onSuccess: (thread) => {
          if (directParticipantId) moveDirectDraft(directParticipantId, thread)
          setActiveConversation({ kind: 'thread', thread })
          focusConversationRef.current = true
          resetCreateForm()
        },
      },
    )
  }

  const createError = createInvalid ? 'Choose at least one participant.' : createSubmitter.error

  if (roster.probed && !roster.enabled) {
    return <div className="team-view-disabled empty-hint">Team is not available for this run</div>
  }

  // addressable — who a new conversation can include: a finished stage or helper cannot answer any more.
  const addressable = participants.filter((p) => p.kind !== 'user' && p.reachable)
  const teamStages = teamStagesOf(stages)

  return (
    <div
      className={`team-view${resize.isDrawer ? ' drawer' : ''}${mobileDetail ? ' mobile-detail' : ''}`}
      ref={resize.targetRef}
      style={{ '--team-roster-width': `${resize.effectiveWidth}px` } as CSSProperties}
      onClickCapture={() => { userInteractedRef.current = true }}
      onKeyDownCapture={() => { userInteractedRef.current = true }}
      onInputCapture={() => { userInteractedRef.current = true }}
    >
      <RunTeamList
        participants={participants}
        threads={inboxThreads}
        summaries={summaries}
        unreadThreadIds={unreadThreadIds}
        newOnly={newOnly}
        onNewOnlyChange={setNewOnly}
        stages={teamStages}
        listView={listView}
        onListViewChange={(view) => {
          setListView(view)
          setListSearch('')
        }}
        search={listSearch}
        onSearchChange={setListSearch}
        actionButtonRef={listActionRef}
        onCreateConversation={(opener) => {
          createOpenerRef.current = opener
          setEditingThreadId(null)
          returnToEditTopicRef.current = false
          if (resize.isDrawer) showDetail()
          setListView('conversations')
          setTab('messages')
          setCreateOpen(true)
        }}
        selectedParticipantId={selectedConversation?.kind === 'direct' ? selectedConversation.participant.id : null}
        selectedThreadId={selectedConversation?.kind === 'thread' ? selectedConversation.thread.id : null}
        onSelectParticipant={handleSelectParticipant}
        onSelectThread={handleSelectThread}
        onAddRunAgent={() => {
          pendingReturnFocusRef.current = null
          if (resize.isDrawer) showDetail()
          setAddRunAgentOpen(true)
        }}
        open={resize.isDrawer && !mobileDetail}
      />
      <div
        ref={resize.handleRef}
        className="panel-resizer team-roster-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize run team panel"
        aria-valuemin={200}
        aria-valuemax={resize.maxWidth}
        aria-valuenow={resize.effectiveWidth}
        aria-hidden={resize.isDrawer ? true : undefined}
        tabIndex={resize.isDrawer ? -1 : 0}
        onMouseDown={resize.onMouseDown}
        onKeyDown={resize.onKeyDown}
        onDoubleClick={resize.onDoubleClick}
      ><span className="panel-resizer-grip" aria-hidden="true" /></div>
      <div className="team-main" ref={teamMainRef}>
        <header className="team-header">
          {resize.isDrawer && mobileDetail && <button ref={backButtonRef} type="button" className="team-back-button"
            onClick={() => { returnToListFocusRef.current = true; setTab('messages'); setMobileDetail(false) }}>
            <span aria-hidden="true">←</span> Back to {listView === 'people' ? 'people' : 'conversations'}
          </button>}
          <div className="team-page-heading">
            <h2>Team</h2>
            <p>Conversations with the people in this run</p>
          </div>
          <TabList
            idPrefix={tabsId}
            ariaLabel="Team"
            className="team-tabs"
            tabClassName="team-tab"
            selected={tab}
            onSelect={selectTab}
            tabs={[
              { id: 'messages', label: 'Messages' },
              {
                id: 'board',
                label: (
                  <>
                    Board
                    <span className="team-tab-count" aria-hidden="true">
                      {boardTaskCountOf(board)}
                    </span>
                  </>
                ),
              },
              { id: 'activity', label: 'Activity' },
            ]}
          />
        </header>
        <div className="team-messages-panel" hidden={tab !== 'messages' || (resize.isDrawer && !mobileDetail)} {...tabPanelProps(tabsId, 'messages')}>
          <div className="team-conversation-header">
            <ConversationSymbol target={createOpen ? null : selectedConversation} participants={participants} />
            <div className="team-conversation-heading">
              <h3>{createOpen ? 'New conversation' : selectedConversation === null ? 'No conversation selected' : conversationTitle(selectedConversation, participants)}</h3>
              <p>{createOpen ? 'Choose a topic and participants' : selectedConversation === null ? 'Select a person or a conversation' : conversationSubtitle(selectedConversation, participants)}</p>
            </div>
            {!createOpen && selectedConversation?.kind === 'thread' && editingThreadId !== selectedConversation.thread.id && (
              <button ref={editTopicButtonRef} type="button" className="team-icon-btn team-topic-edit-button"
                aria-label="Edit topic" title="Edit topic" onClick={() => setEditingThreadId(selectedConversation.thread.id)}>
                <PencilIcon />
              </button>
            )}
          </div>
          <div className="team-messages">
            {!createOpen && selectedConversation?.kind === 'thread' && editingThreadId === selectedConversation.thread.id && (
              <TopicEditor key={selectedConversation.thread.id} thread={selectedConversation.thread}
                titleSeq={summaries[selectedConversation.thread.id]?.title_seq ?? 0} onDone={closeTopicEditor} />
            )}
            {createOpen && (
              <div className="team-create-conversation">
                <div className="team-create-conversation-form">
                  {/* The fields are locked while the conversation is being created: a success resets the form,
                      so anything typed meanwhile would be lost. */}
                  <label className="team-create-conversation-topic">
                    <span>Topic (optional)</span>
                    <input
                      ref={createTopicRef}
                      type="text"
                      value={createTopic}
                      disabled={createSubmitter.pending}
                      onChange={(e) => setCreateTopic(e.target.value)}
                      placeholder="What is this about?"
                    />
                  </label>
                  <fieldset className="team-create-conversation-participants" disabled={createSubmitter.pending}>
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
                    <button type="button" className="btn btn-cancel" onClick={() => resetCreateForm(true)}>
                      Cancel
                    </button>
                    <button type="button" className="btn btn-send" disabled={createSubmitter.pending} onClick={submitCreate}>
                      {createSubmitter.pending ? 'Creating…' : 'Create conversation'}
                    </button>
                  </div>
                </div>
              </div>
            )}
            <div hidden={createOpen} style={{ display: createOpen ? 'none' : 'contents' }}>
              <ThreadConversation
                key={activeKey}
                target={selectedConversation}
                session={sessions.current[activeKey] ?? {}}
                onSessionChange={(patch) => {
                  sessions.current[activeKey] = { ...sessions.current[activeKey], ...patch }
                  if ('replyTargetId' in patch) refreshReplyTarget((n) => n + 1)
                }}
                draft={drafts[activeKey] ?? ''}
                onDraftChange={setActiveDraft}
                participants={participants}
                threads={threads}
                summaries={summaries}
                sideFailures={sideAgentFailures(events)}
                onThreadOpened={handleThreadOpened}
                onDirectThreadResolved={handleDirectThreadResolved}
                getDirectOpenRequestId={getDirectOpenRequestId}
                getSendRequestId={getSendRequestId}
                onSendSucceeded={handleSendSucceeded}
                onMessageSent={handleMessageSent}
                readEnabled={active && readyRunId === runId && tab === 'messages' && !createOpen && !addRunAgentOpen &&
                  (!resize.isDrawer || mobileDetail)}
                onMessagesRead={(threadId, lastRenderedOtherSeq) => {
                  if (advanceTeamSeen(runId, threadId, lastRenderedOtherSeq)) setCursorVersion((version) => version + 1)
                }}
              />
            </div>
          </div>
        </div>
        {visitedTabs.current.has('board') && (
          <div className="team-tab-panel" hidden={tab !== 'board'} {...tabPanelProps(tabsId, 'board')}>
            <BoardLoadState board={board}>
              <BoardPanel board={board} participants={participants} stages={teamStages} active={active && tab === 'board' && (!resize.isDrawer || mobileDetail)} />
            </BoardLoadState>
          </div>
        )}
        {visitedTabs.current.has('activity') && (
          <div className="team-tab-panel" hidden={tab !== 'activity'} {...tabPanelProps(tabsId, 'activity')}>
            <ActivityPanel events={events} />
          </div>
        )}
      </div>
      {addRunAgentOpen && (
        <AddRunAgentDialog
          active={active}
          profiles={profiles}
          onAttached={(result, participant) => {
            focusConversationRef.current = true
            setAddRunAgentOpen(false)
            setMobileDetail(true)
            // Show the new agent and open its conversation right away (the next poll confirms both).
            onParticipantAdded(participant)
            openThread({ id: result.thread_id, title: participantLabel(participant), scope: 'run', members: ['user', result.participant_id], status: 'open' })
            setListView('conversations')
            setListSearch('')
            setTab('messages')
          }}
          onCancel={() => {
            pendingReturnFocusRef.current = listActionRef.current
            if (resize.isDrawer) setMobileDetail(false)
            setAddRunAgentOpen(false)
          }}
        />
      )}
    </div>
  )
}

function PencilIcon(): ReactElement {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M4 20h4L19 9a2.1 2.1 0 0 0-4-4L4 16Z" />
      <path d="m13.5 6.5 4 4" />
    </svg>
  )
}

// ConversationSymbol — the conversation header's picture: the person's avatar for a one-to-one conversation
// (picked from the roster or opened from the list), the group symbol otherwise.
function ConversationSymbol({ target, participants }: { target: ThreadConversationTarget | null; participants: MeshParticipant[] }): ReactElement {
  const person = target?.kind === 'direct' ? target.participant : target?.kind === 'thread' ? directPeer(target.thread, participants) : undefined
  if (person !== undefined) return <MeshAvatar name={participantLabel(person)} kind={person.kind} size="large" />
  return <ThreadSymbol />
}

function TopicEditor({ thread, titleSeq, onDone }: { thread: MeshThread; titleSeq: number; onDone: () => void }): ReactElement {
  const [title, setTitle] = useState(thread.title ?? '')
  const [expectedTitleSeq, setExpectedTitleSeq] = useState(titleSeq)
  const [conflicted, setConflicted] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const submitter = useRetryableSubmit<{ title: string; expectedTitleSeq: number }>('mesh-topic')

  useEffect(() => { inputRef.current?.focus() }, [])

  function submit(nextTitle: string): void {
    // A roster poll cannot silently change the intent's CAS revision. Only an explicit 409 permits rebase.
    if (conflicted && titleSeq <= expectedTitleSeq) return
    const revision = conflicted ? titleSeq : expectedTitleSeq
    if (conflicted) {
      // Freeze the reviewed revision before sending. A lost response must retry this exact payload and ID,
      // even if another roster poll reports a newer title in the meantime.
      setExpectedTitleSeq(revision)
      setConflicted(false)
    }
    submitter.submit(
      { title: nextTitle.trim(), expectedTitleSeq: revision },
      async (payload, requestId) => {
        try {
          return await updateMeshThreadTitle(thread.id, { requestId, ...payload })
        } catch (err) {
          if (err instanceof MeshApiError && err.status === 409) {
            setConflicted(true)
            throw new Error('Topic changed elsewhere. Review the latest topic and try again.')
          }
          throw err
        }
      },
      { onSuccess: onDone },
    )
  }

  return (
    <form className="team-topic-editor" aria-busy={submitter.pending}
      onSubmit={(event) => { event.preventDefault(); submit(title) }}>
      <label className="team-create-conversation-topic">
        <span>Conversation topic</span>
        <input ref={inputRef} type="text" value={title} disabled={submitter.pending}
          onChange={(event) => setTitle(event.target.value)} />
      </label>
      {submitter.error !== null && <p className="team-create-conversation-error" role="alert">{submitter.error}</p>}
      {conflicted && (titleSeq > expectedTitleSeq
        ? <p className="team-topic-current empty-hint">Current topic: {thread.title || 'No topic'}</p>
        : <p className="team-topic-current empty-hint" role="status">Waiting for the latest topic…</p>)}
      <div className="team-create-conversation-actions">
        <button type="button" className="btn btn-cancel" onClick={onDone}>Cancel</button>
        <button type="button" className="btn btn-cancel" disabled={submitter.pending || title.trim() === '' || (conflicted && titleSeq <= expectedTitleSeq)}
          onClick={() => submit('')}>Clear topic</button>
        <button type="submit" className="btn btn-send" disabled={submitter.pending || (conflicted && titleSeq <= expectedTitleSeq)}>
          {submitter.pending ? 'Saving…' : 'Save topic'}
        </button>
      </div>
    </form>
  )
}
