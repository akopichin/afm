import { useCallback, useEffect, useMemo, useReducer, useRef, useState, type ReactElement, type ReactNode } from 'react'
import type { AfmEvent } from '../../types'
import { useEnteringLast } from '../../hooks/use-entering-last'
import { toFeedItems, buildFeedTimeline, feedFilterCounts, isForYou, type FeedFilter, type FeedItem } from './feed-view-model'
import { buildVirtualFeedRows, openSeriesTails, type VirtualFeedRow } from './feed-virtual-model'
import { followReducer } from './follow-policy'
import { splitImageMarkers } from './split-image-markers'
import { imageUrl, reportKey, isRowStateEmpty, useFeedRowState } from './feed-row-state'
import { createReportCache } from './report-cache'
import { RowFrame, FeedRowView, type RowEnv } from './FeedRowView'
import { VirtualFeedList, type FeedListHandle, type ListStart } from './VirtualFeedList'
import { JumpToLatestButton } from '../jump-to-latest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import { selectConversation, selectThought } from './composer-draft'
import { createComposerDraftStore, useComposerContext, type ComposerDraftStore } from './composer-draft-store'
import type { MeshParticipant } from '../../api/mesh-client'
import { useStageConversation, type StageConversation } from '../../hooks/use-mesh'
import { useStageDiscussion } from '../../hooks/use-stage-discussion'
import { withStageDiscussion } from './stage-discussion-feed'
import { ConversationMessage } from '../team/ConversationMessage'
import { mentionHandles } from '../../lib/mesh-roster'
import { executorLabel, executorTooltip, useUnknownCommandName } from '../../lib/executor-label'
import { messageText } from '../../lib/agent-result'
import { canReplyInConversation, isAgentMessageToUser } from '../../lib/mesh-reply'
import { messageDelivered, sideAgentOutcome } from './mesh-receipt'
import { useBudgetUi } from '../budget-dialog/budget-context'

export { FeedGroupView } from './feed-rows'

type FeedWorkspaceProps = {
  events: AfmEvent[]
  // arrivals — live events accepted for these events so far, cumulative (see ArrivalCounts): the list length stops
  // growing at its cap, the counter does not. Feeds the unseen count.
  arrivals?: number
  // The caller supplies the events for this stage or read-only activity view.
  stageId: string | null
  // emptyHint — текст пустого состояния, когда events пуст.
  emptyHint?: string
  // Клик по navigable-элементу ленты — открыть диалог соответствующей стадии на нужном вопросе. Сама
  // навигация — задача другой таски (T6); здесь только проброс координат из FeedItem.
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // noteTarget — стадия, живому агенту которой можно послать заметку прямо из ленты. null → messenger-поле
  // не показываем.
  noteTarget?: string | null
  onSendNote?: (stageId: string, text: string) => Promise<void>
  // mesh (Agent Mesh Phase 7, T7.4) — проброс в FeedComposer как есть. undefined → легаси-поведение, без
  // Message/Revise табов (см. FeedComposer.tsx).
  mesh?: FeedComposerMeshProps
  // --- Pause on focus — сквозной проброс в FeedComposer (стейт живёт в App). ---
  pauseOnFocusEnabled?: boolean
  onTogglePauseOnFocus?: (v: boolean) => void
  // paused — стадия композера на паузе И поставлена на неё нами (считает App).
  composerPaused?: boolean
  onComposerFocus?: () => void
  onComposerBlur?: (hasDraft: boolean) => void
  onComposerResumeNow?: () => void
  // imagePolicy — как трактовать маркеры [AFM image: …] в прозе агента. 'stage' (дефолт) — разбирать в
  // <FeedImage> с per-stage URL (лента стадий).
  imagePolicy?: 'stage' | 'none'
  // reviseAvailable — with mesh: the composer's stage takes a Restart with note now.
  // Without mesh the composer is the Restart note itself and is shown only when it is available.
  reviseAvailable?: boolean
  // draftStore — the run's composer drafts, owned by the caller so a draft outlives this view too (the Team tab
  // and back). Without it the drafts live as long as this FeedWorkspace.
  draftStore?: ComposerDraftStore
  // holdActive — Hold has an operation on the composer's stage (App's usePauseOnFocus).
  holdActive?: boolean
  paused?: boolean
  onReplyNeededChange?: (stageId: string | null, count: number) => void
  // Filled with a function that goes to the next question awaiting the user's reply.
  replyJumpRef?: { current: (() => void) | null }
  // aboveFeed — a block between the filter row and the feed (the Stage note); it stays outside the scroll area.
  aboveFeed?: ReactNode
}

export function feedItemSignature(item: { stageId: string; kind: string; actor: string; text: string }): string {
  return JSON.stringify([item.stageId, item.kind, item.actor, item.text])
}

const JUMP_HIGHLIGHT_MS = 1600
const ENTRANCE_MS = 2400

type FilterChoice = { stageId: string | null; filter: FeedFilter }
type PendingStart = ListStart & { listKey: string }

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий.
export function FeedWorkspace({
  events,
  arrivals = 0,
  stageId,
  emptyHint,
  onOpenDialog,
  noteTarget = null,
  onSendNote,
  pauseOnFocusEnabled = false,
  onTogglePauseOnFocus,
  composerPaused = false,
  onComposerFocus,
  onComposerBlur,
  onComposerResumeNow,
  imagePolicy = 'stage',
  mesh,
  reviseAvailable = true,
  draftStore,
  holdActive = false,
  paused = false,
  onReplyNeededChange,
  replyJumpRef,
  aboveFeed,
}: FeedWorkspaceProps): ReactElement {
  // Drafts are per stage: switching stages (or the Feed following the run) remounts the composer for the new
  // stage, and coming back restores what was written and picked there.
  const [ownDraftStore] = useState(createComposerDraftStore)
  const drafts = draftStore ?? ownDraftStore
  // Only the reply context is read here: typing in the composer does not re-render the feed.
  const context = useComposerContext(drafts, noteTarget)
  // focusRequest — an action that wants the input focused (↩, Reply in conversation); a restored draft never does.
  const [focusRequest, setFocusRequest] = useState({ stageId: '', seq: 0 })
  const requestFocus = useCallback((id: string): void => setFocusRequest((prev) => ({ stageId: id, seq: prev.seq + 1 })), [])
  const list = useRef<FeedListHandle>(null)

  const unknownPrefix = useUnknownCommandName()
  const { names: budgetNames } = useBudgetUi()
  const items = useMemo(() => toFeedItems(events, unknownPrefix, budgetNames), [events, unknownPrefix, budgetNames])
  // The stage's conversations, interleaved with its events — only in a stage feed with the mesh on — together
  // with its conversation log with its agent: the "Not answered" marks and the posts only that log has.
  const conversation = useStageConversation(mesh !== undefined ? stageId : null)
  const stageStatus = mesh?.stages.find((s) => s.id === stageId)?.status
  const discussionLog = useStageDiscussion(mesh !== undefined ? stageId : null, stageStatus === 'done' || stageStatus === 'failed')
  const { messages: conversationMessages, unanswered, legacy } = useMemo(
    () => withStageDiscussion(conversation.messages, discussionLog, stageId ?? ''),
    [conversation.messages, discussionLog, stageId],
  )
  const participants = mesh?.roster.participants ?? EMPTY_PARTICIPANTS

  // A stage starts with every filter on: the choice belongs to the stage it was made in, decided while rendering.
  const [choice, setChoice] = useState<FilterChoice>({ stageId, filter: 'all' })
  if (choice.stageId !== stageId) setChoice({ stageId, filter: 'all' })
  const filter = choice.stageId === stageId ? choice.filter : 'all'
  const listKey = `${stageId ?? ''}|${filter}`
  const fullTimeline = useMemo(() => buildFeedTimeline(items, conversationMessages, 'all'), [items, conversationMessages])
  const timeline = useMemo(() => (filter === 'all' ? fullTimeline : buildFeedTimeline(items, conversationMessages, filter)), [fullTimeline, items, conversationMessages, filter])
  const filterCounts = useMemo(() => feedFilterCounts(items, conversationMessages), [items, conversationMessages])
  const messageById = useMemo(() => new Map(conversationMessages.map((m) => [m.id, m])), [conversationMessages])

  // What outlives an unmounted row: the open series, reports and Raw sections and the image retries, per stage.
  const { state: rowState, actions: rowActions } = useFeedRowState(stageId ?? '')
  const [reports] = useState(createReportCache)
  useEffect(() => () => reports.clear(), [reports, stageId])
  const model = useMemo(() => buildVirtualFeedRows(timeline, rowState.openTools), [timeline, rowState.openTools])
  useEffect(() => {
    if (isRowStateEmpty(rowState)) return
    rowActions.sync({
      itemKeys: new Set(items.map((item) => item.key)),
      reportKeys: new Set(items.flatMap((item) => (item.reportVerificationId === undefined ? [] : [reportKey(item.stageId, item.reportVerificationId)]))),
      messageIds: new Set(conversationMessages.map((m) => m.id)),
      imageUrls: liveImageUrls(items),
      seriesTails: openSeriesTails(fullTimeline, rowState.openTools),
    })
  }, [rowState, rowActions, items, conversationMessages, fullTimeline])

  // follow: the feed leads the bottom until the reader scrolls up; a new stage opens at the bottom.
  const [following, dispatchFollow] = useReducer(followReducer, true)
  const [followStage, setFollowStage] = useState(stageId)
  if (followStage !== stageId) {
    setFollowStage(stageId)
    dispatchFollow({ type: 'reset' })
  }
  // start — where the list opens when it is remounted by another filter: at the reader's row, if it is still there.
  const [pendingStart, setPendingStart] = useState<PendingStart | null>(null)
  const start = pendingStart?.listKey === listKey ? pendingStart : null
  function changeFilter(next: FeedFilter): void {
    const anchor = following ? null : (list.current?.anchor() ?? null)
    const nextKey = `${stageId ?? ''}|${next}`
    setPendingStart(anchor === null ? null : { listKey: nextKey, key: anchor.key, align: 'start', offset: -anchor.offset })
    // Без якоря список откроется на хвосте: и follow должен вести его, а не остаться снятым.
    if (anchor === null) dispatchFollow({ type: 'reset' })
    setChoice({ stageId, filter: next })
  }

  // Jump to a quoted message: show it (Author's work may hide it), scroll to it and flash it. A message that is not
  // in the loaded data is said so and the position stays.
  const [highlight, setHighlight] = useState<{ id: string } | null>(null)
  const [outsideHistory, setOutsideHistory] = useState(false)
  function jumpTo(id: string): void {
    const target = messageById.get(id)
    setOutsideHistory(target === undefined)
    if (target === undefined) return
    dispatchFollow({ type: 'navigate' })
    setHighlight({ id })
    if (filter === 'author' && !isForYou(target)) {
      setPendingStart({ listKey: `${stageId ?? ''}|all`, key: id, align: 'center' })
      setChoice({ stageId, filter: 'all' })
    } else {
      list.current?.scrollToRow(id, 'center')
    }
  }
  useEffect(() => {
    if (highlight === null) return
    const timer = setTimeout(() => setHighlight(null), JUMP_HIGHLIGHT_MS)
    return () => clearTimeout(timer)
  }, [highlight])
  function jumpToLatest(): void {
    setHighlight(null)
    setOutsideHistory(false)
    setPendingStart(null)
    dispatchFollow({ type: 'jump-to-latest' })
    list.current?.scrollToLatest()
  }
  const onToggleRow = useCallback((rowKey: string, open: boolean): void => {
    list.current?.hold(rowKey)
    // Свёртывание follow не трогает: геометрия в момент клика ещё старая, решает watchBottom после раскладки.
    if (open) dispatchFollow({ type: 'expanded' })
    else list.current?.watchBottom()
  }, [])

  // Анимация появления: только самая свежая строка ленты и только когда её КОНТЕНТ отличается от прошлой
  // свежей строки.
  const lastItem = items[items.length - 1]
  const lastKey = lastItem?.key ?? null
  const lastSig = lastItem ? feedItemSignature(lastItem) : null
  const resetToken = stageId
  const { animate, onEntered } = useEnteringLast(lastSig, resetToken)
  const enteringKey = animate && lastItem?.kind !== 'tool' ? lastKey : null
  useEffect(() => {
    if (!animate) return
    const timer = window.setTimeout(onEntered, ENTRANCE_MS)
    return () => window.clearTimeout(timer)
  }, [animate, onEntered, lastKey])
  const newestMessage = conversationMessages.at(-1)
  const messageEntrance = useEnteringLast(newestMessage?.id ?? null, resetToken)
  useEffect(() => {
    if (!messageEntrance.animate) return
    const timer = window.setTimeout(messageEntrance.onEntered, ENTRANCE_MS)
    return () => window.clearTimeout(timer)
  }, [messageEntrance.animate, messageEntrance.onEntered, newestMessage?.id])

  const activeReplyKey = context?.kind === 'thought' ? context.key : null
  const activeMeshReplyId = context?.kind === 'conversation' ? context.target.messageId : null
  // A conversation closed while its reply is written keeps the reply and the input; the composer says so.
  const conversationClosed = context?.kind === 'conversation' && conversation.threads[context.target.threadId]?.status === 'closed'
  const waitingForYou = new Set(conversationMessages.filter((m) => m.kind === 'request' && isAgentMessageToUser(m) && canReplyInConversation(m, conversation.threads[m.thread_id], participants) && !conversationMessages.some((answer) => m.recipient_ids.includes(answer.sender_id) && answer.in_reply_to === m.id)).map((m) => m.id))
  const replyNeeded = waitingForYou.size
  useEffect(() => { onReplyNeededChange?.(stageId, replyNeeded) }, [stageId, replyNeeded, onReplyNeededChange])
  const lastReplyJump = useRef<string | null>(null)
  useEffect(() => {
    if (replyJumpRef === undefined) return
    replyJumpRef.current = () => {
      const pending = [...waitingForYou]
      if (pending.length === 0) return
      const next = pending[pending.indexOf(lastReplyJump.current ?? '') + 1] ?? (pending[0] as string)
      lastReplyJump.current = next
      jumpTo(next)
    }
    return () => { replyJumpRef.current = null }
  })

  const composerMounted = noteTarget !== null && onSendNote !== undefined
  // A reply is a context of the composer's one input: it needs the composer, and Chat for a conversation.
  const canAnswerInConversation = composerMounted && mesh !== undefined
  const canAnswerThought = composerMounted
  const meshOn = mesh !== undefined
  const replyToThought = useCallback((key: string, text: string): void => {
    if (noteTarget === null) return
    drafts.update(noteTarget, (d) => selectThought(d, { key, text }, meshOn))
    requestFocus(noteTarget)
  }, [drafts, noteTarget, meshOn, requestFocus])
  const stageIndex = mesh?.stages.findIndex((stage) => stage.id === stageId) ?? -1
  const stageAgent = participants.find((person) => person.id === `stage:${stageId}`)
  const stageExecutor = executorLabel(stageAgent?.command ?? '', stageAgent?.command_name, useUnknownCommandName())
  const unseen = useUnseenCount(following, stageId, arrivals + conversation.arrived)
  const filterContext = stageIndex >= 0 ? `Stage ${stageIndex + 1} of ${mesh!.stages.length}${stageExecutor.label ? ` · ${stageExecutor.label}` : ''}` : stageExecutor.label

  const quoteRestarts = mesh === undefined
  const rowEnv = useMemo<RowEnv>(() => ({
    imagePolicy,
    onOpenDialog,
    onReplyToThought: canAnswerThought && noteTarget !== null ? replyToThought : undefined,
    quoteRestarts,
    activeReplyKey,
    onEntered,
    state: rowState,
    actions: rowActions,
    imageRetries: { attempts: rowState.imageAttempts, onRetry: rowActions.retryImage },
    reports,
    countScope: listKey,
    onToggleRow,
  }), [imagePolicy, onOpenDialog, canAnswerThought, noteTarget, replyToThought, quoteRestarts, activeReplyKey, onEntered, rowState, rowActions, reports, listKey, onToggleRow])

  function renderRow(row: VirtualFeedRow, isFirst: boolean): ReactNode {
    if (row.kind !== 'conversation') {
      const entering = enteringKey !== null && (row.kind === 'framed' ? row.group.items.some((item) => item.key === enteringKey) : row.kind === 'tool-summary' ? false : row.item.key === enteringKey)
      return <FeedRowView row={row} env={rowEnv} isFirst={isFirst} enteringKey={entering ? enteringKey : null} />
    }
    const m = row.message
    return (
      <RowFrame row={row} isFirst={isFirst}>
        <ConversationMessage
          message={m}
          presentation="feed"
          rootTag="div"
          delivery={messageDelivered(m, conversationMessages, mesh?.deliveries ?? EMPTY_DELIVERIES) ? 'Delivered' : undefined}
          waitingForYou={waitingForYou.has(m.id)}
          isNew={messageEntrance.animate && m.id === newestMessage?.id}
          jumpTarget={highlight?.id === m.id}
          previousCreatedAt={row.previousCreatedAt}
          participants={participants}
          answered={m.in_reply_to !== undefined ? messageById.get(m.in_reply_to) : undefined}
          onJumpToQuote={jumpTo}
          replyActive={activeMeshReplyId === m.id}
          notAnswered={unanswered.has(m.id)}
          sideOutcome={sideAgentOutcome(m, conversationMessages, mesh?.sideFailures ?? new Set())}
          markdown
          role={legacy.get(m.id)?.role}
          evidence={legacy.get(m.id)?.evidence}
          raw={{
            open: rowState.openRaw.has(m.id),
            onToggle: (open) => {
              onToggleRow(m.id, open)
              rowActions.toggleRaw(m.id)
            },
          }}
          onReply={
            canAnswerInConversation && noteTarget !== null && canReplyInConversation(m, conversation.threads[m.thread_id], participants)
              ? () => {
                  drafts.update(noteTarget, (d) => selectConversation(d, {
                    messageId: m.id,
                    threadId: m.thread_id,
                    senderId: m.sender_id,
                    preview: messageText(m.body, true).slice(0, 120),
                    members: conversation.threads[m.thread_id]?.members ?? [],
                  }))
                  requestFocus(noteTarget)
                }
              : undefined
          }
        />
      </RowFrame>
    )
  }

  const working = stageId !== null && !paused ? workingLabel(stageId, participants, conversation) : null
  const footer = useMemo(() => (working === null ? null : <WorkingLine label={working} />), [working])
  const overlay = (
    <>
      {outsideHistory && (
        <div className="feed-jump-notice" role="status">
          Message is outside the loaded history
        </div>
      )}
      {!following && <JumpToLatestButton onClick={jumpToLatest} unseen={unseen} overlay />}
    </>
  )

  return (
    <section className="feed-workspace" aria-label="Feed">
      {(mesh !== undefined || conversationMessages.length > 0) && (
        <div className="feed-filter-row">
          <FeedFilterChips filter={filter} onChange={changeFilter} counts={filterCounts} />
          {filterContext && <span className="feed-filter-context" title={executorTooltip(stageExecutor) || undefined}>{filterContext}</span>}
        </div>
      )}
      {/* Фрагмент отделяет ключ слота от key={noteTarget} композера: совпавшие ключи среди детей section осиротят DOM слота. */}
      <>{aboveFeed}</>
      {model.rows.length === 0 ? (
        <div className="feed-scroll-frame">
          <div id="feed-content" className="feed-scroll feed-scroll--empty">
            <div className="empty-hint feed-empty">{items.length + conversationMessages.length === 0 ? (emptyHint ?? 'No events yet') : 'Nothing to show for this filter'}</div>
            {footer}
          </div>
          {overlay}
        </div>
      ) : (
        <VirtualFeedList
          key={listKey}
          ref={list}
          rows={model.rows}
          renderRow={renderRow}
          following={following}
          onFollow={dispatchFollow}
          start={start}
          footer={footer}
          footerKey={working ?? ''}
          overlay={overlay}
        />
      )}

      {/* The composer is pinned under the feed while the stage takes notes. key=noteTarget: each stage has its own
          composer over its own draft, so a draft never moves to another stage. */}
      {composerMounted && (
        <FeedComposer
          key={noteTarget}
          stageId={noteTarget}
          onSend={(text) => onSendNote(noteTarget, text)}
          draftStore={drafts}
          focusRequest={focusRequest.stageId === noteTarget ? focusRequest.seq : 0}
          conversationClosed={conversationClosed}
          holdActive={holdActive}
          pauseOnFocusEnabled={pauseOnFocusEnabled}
          onTogglePauseOnFocus={onTogglePauseOnFocus}
          paused={composerPaused}
          onFocus={onComposerFocus}
          onBlur={onComposerBlur}
          onResumeNow={onComposerResumeNow}
          mesh={mesh}
          reviseAvailable={reviseAvailable}
          conversationMessages={conversationMessages}
        />
      )}
    </section>
  )
}

// liveImageUrls — адреса картинок, на которые ссылаются сообщения ленты.
function liveImageUrls(items: FeedItem[]): Set<string> {
  const urls = new Set<string>()
  for (const item of items) {
    if (!item.text.includes('[AFM image:')) continue
    for (const segment of splitImageMarkers(item.text)) if (segment.type === 'img') urls.add(imageUrl(item.stageId, segment.name))
  }
  return urls
}

// useUnseenCount — сколько событий пришло, пока лента отпущена от низа: без счётчика она выглядит замершей.
// arrived растёт только вверх и не зависит от порядка и капа списка, поэтому счёт — разница с моментом
// отпускания; он сбрасывается при возврате вниз и при смене стадии.
function useUnseenCount(stick: boolean, stageId: string | null, arrived: number): number {
  const [released, setReleased] = useState<{ stageId: string | null; arrived: number } | null>(null)
  if (stick) {
    if (released !== null) setReleased(null)
    return 0
  }
  if (released === null || released.stageId !== stageId || arrived < released.arrived) {
    setReleased({ stageId, arrived })
    return 0
  }
  return arrived - released.arrived
}

const EMPTY_PARTICIPANTS: MeshParticipant[] = []
const EMPTY_DELIVERIES: ReadonlySet<string> = new Set()

const FILTER_LABEL: Record<FeedFilter, string> = { all: 'All activity', conversations: 'Conversations', author: "Author's work" }

// FeedFilterChips — «All / Conversations / Author's work» with counts. Messages addressed to you stay visible
// under every filter.
function FeedFilterChips({ filter, onChange, counts }: { filter: FeedFilter; onChange: (f: FeedFilter) => void; counts: Record<FeedFilter, number> }): ReactElement {
  return (
    <div className="feed-filter-chips" role="group" aria-label="Show in the feed">
      {(['all', 'conversations', 'author'] as const).map((f) => (
        <button key={f} type="button" className="feed-filter-chip" aria-pressed={filter === f} onClick={() => onChange(f)}>
          {FILTER_LABEL[f]}
          <span className="feed-filter-count">{counts[f]}</span>
        </button>
      ))}
    </div>
  )
}

// workingLabel — "@qa is working": the agents this stage talks to (members of its conversations, its helpers)
// whose turn is running now, from the roster's activity. The stage agent's own work is the feed itself.
function workingLabel(stageId: string, participants: MeshParticipant[], conversation: StageConversation): string | null {
  const members = new Set(Object.values(conversation.threads).flatMap((t) => t.members))
  const working = participants.filter(
    (p) => p.activity === 'working' && p.id !== `stage:${stageId}` && (p.stage_id === stageId || members.has(p.id)),
  )
  if (working.length === 0) return null
  const handles = mentionHandles(participants)
  const names = working.map((p) => `@${handles.get(p.id) ?? p.id}`).join(', ')
  return `${names} ${working.length === 1 ? 'is' : 'are'} working`
}

// WorkingLine — the working label under the last row.
function WorkingLine({ label }: { label: string }): ReactElement {
  return (
    <div className="feed-working" role="status">
      <span className="feed-working-dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      {label}
    </div>
  )
}
