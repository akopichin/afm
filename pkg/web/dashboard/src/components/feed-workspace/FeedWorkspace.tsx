import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type AnimationEvent as ReactAnimationEvent, type CSSProperties, type MouseEvent as ReactMouseEvent, type ReactElement, type ReactNode, type SyntheticEvent as ReactSyntheticEvent } from 'react'
import { flushSync } from 'react-dom'
import type { AfmEvent } from '../../types'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useEnteringLast } from '../../hooks/use-entering-last'
import { toFeedItems, buildFeedTimeline, feedFilterCounts, groupToolRuns, isForYou, toolCall, toolRunSummary, windowFeedTimeline, FEED_WINDOW_STEP, type FeedActor, type FeedFilter, type FeedGroup, type FeedItem } from './feed-view-model'
import { JumpToLatestButton } from '../jump-to-latest'
import { FeedComposer, type FeedComposerMeshProps } from './FeedComposer'
import { selectConversation, selectThought } from './composer-draft'
import { createComposerDraftStore, useComposerContext, type ComposerDraftStore } from './composer-draft-store'
import { renderPlainMarkdown, renderPlainMarkdownCached } from '../plan-panel/markdown'
import { splitImageMarkers } from './split-image-markers'
import { getVerifyReport } from '../../api/verify-report-client'
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
import { MessageTime } from '../message-time/MessageTime'
import { MeshAvatar } from '../team/MeshAvatar'
import { ExecutorBadge } from '../ExecutorBadge'

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

const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}
const STAGE_AGENT_ROLE = 'Stage agent'

// The chip sits inline after a row's text: inline-block lets its width cap and ellipsis apply there.
const INLINE_BADGE_STYLE: CSSProperties = { display: 'inline-block', marginLeft: 8, verticalAlign: 'middle' }

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий.
export function feedItemSignature(item: { stageId: string; kind: string; actor: string; text: string }): string {
  return JSON.stringify([item.stageId, item.kind, item.actor, item.text])
}

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
  const feed = useStickToBottom<HTMLDivElement>()
  // Drafts are per stage: switching stages (or the Feed following the run) remounts the composer for the new
  // stage, and coming back restores what was written and picked there.
  const [ownDraftStore] = useState(createComposerDraftStore)
  const drafts = draftStore ?? ownDraftStore
  // Only the reply context is read here: typing in the composer does not re-render the feed.
  const context = useComposerContext(drafts, noteTarget)
  // focusRequest — an action that wants the input focused (↩, Reply in conversation); a restored draft never does.
  const [focusRequest, setFocusRequest] = useState({ stageId: '', seq: 0 })
  const requestFocus = useCallback((id: string): void => setFocusRequest((prev) => ({ stageId: id, seq: prev.seq + 1 })), [])
  const sectionRef = useRef<HTMLElement | null>(null)

  const unknownPrefix = useUnknownCommandName()
  const items = useMemo(() => toFeedItems(events, unknownPrefix), [events, unknownPrefix])
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
  const [filter, setFilter] = useState<FeedFilter>('all')
  useEffect(() => setFilter('all'), [stageId])
  const timeline = useMemo(() => buildFeedTimeline(items, conversationMessages, filter), [items, conversationMessages, filter])
  const filterCounts = useMemo(() => feedFilterCounts(items, conversationMessages), [items, conversationMessages])
  const messageById = useMemo(() => new Map(conversationMessages.map((m) => [m.id, m])), [conversationMessages])

  // The window: only the last rows are mounted. While the reader is away from the bottom its start is pinned to a
  // row key, so new events do not shift what is being read; back at the bottom it shrinks to the default again.
  const windowScope = `${stageId ?? ''}|${filter}`
  const [pin, setPin] = useState<{ scope: string; key: string } | null>(null)
  const startKey = !feed.stick && pin?.scope === windowScope ? pin.key : null
  const win = useMemo(() => windowFeedTimeline(timeline, startKey, FEED_WINDOW_STEP), [timeline, startKey])
  if (feed.stick) {
    if (pin !== null) setPin(null)
  } else if (startKey === null && win.rowKeys.length > 0) {
    setPin({ scope: windowScope, key: win.rowKeys[win.start] as string })
  }
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const setScrollNode = useCallback((node: HTMLDivElement | null) => {
    scrollRef.current = node
    feed.ref(node)
  }, [feed.ref])
  const keepScroll = useScrollAnchor(win.start)
  function showEarlier(): void {
    keepScroll(scrollRef.current)
    setPin({ scope: windowScope, key: win.rowKeys[Math.max(0, win.start - FEED_WINDOW_STEP)] as string })
    feed.release()
  }

  // Jump to a quoted message: show it (Author's work may hide it), scroll to it and flash it.
  const [jump, setJump] = useState<{ id: string; n: number } | null>(null)
  function jumpTo(id: string): void {
    const target = messageById.get(id)
    if (filter === 'author' && target !== undefined && !isForYou(target)) setFilter('all')
    setJump((prev) => ({ id, n: (prev?.n ?? 0) + 1 }))
  }
  // A quoted message above the window widens it to that row first.
  const jumpRow = jump === null ? -1 : win.rowKeys.indexOf(jump.id)
  const jumpHidden = jumpRow >= 0 && jumpRow < win.start
  useLayoutEffect(() => {
    if (!jumpHidden || jump === null) return
    setPin({ scope: windowScope, key: jump.id })
    feed.release()
  }, [jumpHidden, jump, windowScope, feed.release])
  // The request is consumed once the row is shown: a kept one would pull the window back to the quote later.
  const jumpHighlight = useRef<{ el: HTMLElement; timer: ReturnType<typeof setTimeout> } | null>(null)
  useEffect(() => {
    if (jump === null || jumpHidden) return
    const el = [...(sectionRef.current?.querySelectorAll<HTMLElement>('[data-message-id]') ?? [])].find((x) => x.dataset.messageId === jump.id)
    setJump(null)
    if (el === undefined) return
    el.scrollIntoView?.({ block: 'center', behavior: window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    el.classList.add('is-jump-target')
    jumpHighlight.current = { el, timer: setTimeout(() => el.classList.remove('is-jump-target'), 1600) }
  }, [jump, jumpHidden])
  useEffect(() => setJump(null), [windowScope])
  useEffect(() => () => {
    if (jumpHighlight.current === null) return
    clearTimeout(jumpHighlight.current.timer)
    jumpHighlight.current.el.classList.remove('is-jump-target')
  }, [])

  // Анимация появления: только самая свежая строка ленты и только когда её КОНТЕНТ отличается от прошлой
  // свежей строки.
  const lastItem = items[items.length - 1]
  const lastKey = lastItem?.key ?? null
  const lastSig = lastItem ? feedItemSignature(lastItem) : null
  const resetToken = stageId
  const { animate, onEntered } = useEnteringLast(lastSig, resetToken)
  const enteringKey = animate && lastItem?.kind !== 'tool' ? lastKey : null
  // Only the group holding the entering row gets the props: the others stay equal and are not re-rendered.
  const enteringIn = (group: FeedGroup): string | null => (enteringKey !== null && group.items.some((item) => item.key === enteringKey) ? enteringKey : null)
  useEffect(() => {
    if (!animate) return
    const timer = window.setTimeout(onEntered, 2400)
    return () => window.clearTimeout(timer)
  }, [animate, onEntered, lastKey])
  const newestMessage = conversationMessages.at(-1)
  const messageEntrance = useEnteringLast(newestMessage?.id ?? null, resetToken)
  useEffect(() => {
    if (!messageEntrance.animate) return
    const timer = window.setTimeout(messageEntrance.onEntered, 2400)
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
  const unseen = useUnseenCount(feed.stick, stageId, arrivals + conversation.arrived)

  // Раскрытая группа — режим чтения: новые события не уводят её из вида. Свернули — снова решаем по позиции,
  // иначе раскрытая у низа и свёрнутая группа навсегда отпускает ленту.
  function onSummaryClick(event: ReactMouseEvent): void {
    const details = (event.target as HTMLElement).closest('summary')?.parentElement
    if (!(details instanceof HTMLDetailsElement)) return
    if (details.open) details.addEventListener('toggle', feed.recheck, { once: true })
    else feed.release()
  }
  const filterContext = stageIndex >= 0 ? `Stage ${stageIndex + 1} of ${mesh!.stages.length}${stageExecutor.label ? ` · ${stageExecutor.label}` : ''}` : stageExecutor.label

  return (
    <section className="feed-workspace" aria-label="Feed" ref={sectionRef}>
      {(mesh !== undefined || conversationMessages.length > 0) && (
        <div className="feed-filter-row">
          <FeedFilterChips filter={filter} onChange={setFilter} counts={filterCounts} />
          {filterContext && <span className="feed-filter-context" title={executorTooltip(stageExecutor) || undefined}>{filterContext}</span>}
        </div>
      )}
      {/* Фрагмент отделяет ключ слота от key={noteTarget} композера: совпавшие ключи среди детей section осиротят DOM слота. */}
      <>{aboveFeed}</>
      <div id="feed-content" className="feed-scroll" ref={setScrollNode} onClickCapture={onSummaryClick}>
        {timeline.length === 0 ? (
          <div className="empty-hint feed-empty">{items.length + conversationMessages.length === 0 ? (emptyHint ?? 'No events yet') : 'Nothing to show for this filter'}</div>
        ) : (
          <>
          {win.hidden > 0 && <ShowEarlierButton hidden={win.hidden} onClick={showEarlier} />}
          {win.entries.map((entry) =>
            entry.kind === 'conversation' ? (
              <ul key={entry.key} className="team-conversation-message-list feed-conversation" aria-label="Conversation messages">
                {entry.messages.map((m, index) => (
                  <ConversationMessage
                    key={m.id}
                    message={m}
                    presentation="feed"
                    delivery={messageDelivered(m, conversationMessages, mesh?.deliveries ?? EMPTY_DELIVERIES) ? 'Delivered' : undefined}
                    waitingForYou={waitingForYou.has(m.id)}
                    isNew={messageEntrance.animate && m.id === newestMessage?.id}
                    previousCreatedAt={entry.messages[index - 1]?.created_at}
                    participants={participants}
                    answered={m.in_reply_to !== undefined ? messageById.get(m.in_reply_to) : undefined}
                    onJumpToQuote={jumpTo}
                    replyActive={activeMeshReplyId === m.id}
                    notAnswered={unanswered.has(m.id)}
                    sideOutcome={sideAgentOutcome(m, conversationMessages, mesh?.sideFailures ?? new Set())}
                    markdown
                    role={legacy.get(m.id)?.role}
                    evidence={legacy.get(m.id)?.evidence}
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
                ))}
              </ul>
            ) : (
              <FeedGroupView
                key={entry.key}
                group={entry.group}
                showStageBadges={false}
                imagePolicy={imagePolicy}
                enteringKey={enteringIn(entry.group)}
                onEntered={enteringIn(entry.group) === null ? undefined : onEntered}
                toolCountScope={`${stageId ?? ''}|${filter}`}
                onOpenDialog={onOpenDialog}
                // A thought is answerable only where the composer is: the quote becomes its reply context.
                onReplyToThought={canAnswerThought && noteTarget !== null ? replyToThought : undefined}
                quoteRestarts={mesh === undefined}
                activeReplyKey={activeReplyKey}
              />
            ),
          )}
          </>
        )}
        {stageId !== null && !paused && <WorkingLine stageId={stageId} participants={participants} conversation={conversation} />}
        {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} unseen={unseen} />}
      </div>

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

// WorkingLine — "@qa is working": the agents this stage talks to (members of its conversations, its helpers)
// whose turn is running now, from the roster's activity. The stage agent's own work is the feed itself.
function WorkingLine({ stageId, participants, conversation }: { stageId: string; participants: MeshParticipant[]; conversation: StageConversation }): ReactElement | null {
  const members = new Set(Object.values(conversation.threads).flatMap((t) => t.members))
  const working = participants.filter(
    (p) => p.activity === 'working' && p.id !== `stage:${stageId}` && (p.stage_id === stageId || members.has(p.id)),
  )
  if (working.length === 0) return null
  const handles = mentionHandles(participants)
  const names = working.map((p) => `@${handles.get(p.id) ?? p.id}`).join(', ')
  return (
    <div className="feed-working" role="status">
      <span className="feed-working-dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      {names} {working.length === 1 ? 'is' : 'are'} working
    </div>
  )
}

type FeedGroupViewProps = {
  group: FeedGroup
  // Assistant history can label the source stage of a group.
  showStageBadges: boolean
  // systemAuthorLabel — подпись системного автора в карточке истории (messageCard); imagePolicy — см. FeedWorkspaceProps.
  systemAuthorLabel?: string
  imagePolicy?: 'stage' | 'none'
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // onReplyToThought — выбрать мысль агента для ответа. quoteRestarts — без mesh ответ на мысль — это перезапуск
  // с цитатой, и кнопка ↩ называет именно это действие.
  onReplyToThought?: (key: string, text: string) => void
  quoteRestarts?: boolean
  // activeReplyKey — ключ выбранной мысли: ровно одна строка получает
  // is-reply-target (подсветка выбора).
  activeReplyKey?: string | null
  // enteringKey — the feed key of the single newest row that should play the entrance animation.
  enteringKey?: string | null
  onEntered?: () => void
  // toolCountScope — what a tool run's count belongs to (the feed's stage and filter). Another scope starts the
  // count over without a bump: a filter can merge two runs into one without any new call.
  toolCountScope?: string
  messageCard?: boolean
}

function sameItem(a: FeedItem, b: FeedItem): boolean {
  const keys = Object.keys(a) as (keyof FeedItem)[]
  return keys.length === Object.keys(b).length && keys.every((key) => (key === 'executor' ? shallowEqual(a.executor, b.executor) : a[key] === b[key]))
}

function sameItems(a: FeedItem[], b: FeedItem[]): boolean {
  return a.length === b.length && a.every((item, i) => sameItem(item, b[i] as FeedItem))
}

function shallowEqual(a: object | undefined, b: object | undefined): boolean {
  if (a === b) return true
  if (a === undefined || b === undefined) return false
  const keys = Object.keys(a) as (keyof typeof a)[]
  return keys.length === Object.keys(b).length && keys.every((key) => a[key] === (b as typeof a)[key])
}

// Items are rebuilt on every event, so a group is compared by content: only a group that changed re-renders.
function sameGroupProps(prev: FeedGroupViewProps, next: FeedGroupViewProps): boolean {
  const { group: prevGroup, ...prevRest } = prev
  const { group: nextGroup, ...nextRest } = next
  const sameGroup = prevGroup.key === nextGroup.key && prevGroup.side === nextGroup.side && prevGroup.stageId === nextGroup.stageId &&
    prevGroup.actor === nextGroup.actor && prevGroup.authorLabel === nextGroup.authorLabel && sameItems(prevGroup.items, nextGroup.items)
  return sameGroup && shallowEqual(prevRest, nextRest)
}

// FeedGroupView — одна группа: стейдж-агент — строка автора и тело без рамки, события флоу — тихие строки,
// пользователь и карточки истории — пузырь с шапкой.
export const FeedGroupView = memo(FeedGroupViewImpl, sameGroupProps)

function FeedGroupViewImpl({ group, showStageBadges, systemAuthorLabel = 'Flow', imagePolicy = 'stage', onOpenDialog, onReplyToThought, quoteRestarts = false, activeReplyKey = null, enteringKey = null, onEntered, toolCountScope, messageCard = false }: FeedGroupViewProps): ReactElement {
  // Подпись автора: agent/user — фиксированные, system — параметризуемая (боковой
  // чат заменяет «Flow» своей подписью, см. systemAuthorLabel).
  const authorLabel = group.authorLabel ?? (group.actor === 'system' || (messageCard && group.actor === 'agent') ? systemAuthorLabel : ACTOR_LABEL[group.actor])
  const firstItem = group.items[0]
  const isStageAgent = !messageCard && group.actor === 'agent'
  const isFlow = !messageCard && group.actor === 'system'
  // The first row's time sits in the author row, so that row does not repeat it.
  const timeInHead = messageCard || isStageAgent
  const framed = messageCard || group.actor === 'user'
  const firstTime = firstItem && <MessageTime timestamp={firstItem.ts} gap={firstItem.gap} firstEvent={firstItem.firstEvent} focusable />
  const head = isFlow ? null : isStageAgent ? (
    <div className="feed-group-head feed-author-row">
      <strong className="feed-actor">{group.stageId}</strong>
      <span className="feed-role">{STAGE_AGENT_ROLE}</span>
      {firstTime}
    </div>
  ) : (
    <div className="feed-group-head">
      {showStageBadges && group.stageId !== '' && <span className="feed-stage-badge">{group.stageId}</span>}
      <span className="feed-actor">{authorLabel}</span>
      {messageCard && firstTime}
    </div>
  )
  const content = (
    <>
      {head}
      <div className={framed ? 'feed-bubble' : 'feed-rows'}>
        {groupToolRuns(group.items).map((row) => {
          if (row.kind === 'tools') return <ToolRun key={row.key} items={row.items} countScope={toolCountScope} />
          const item = row.item
          const isMd = item.markdown === true
          const isThought = item.kind === 'message' && isMd
          const isInteractiveRow = (item.navigable === true && onOpenDialog !== undefined) || (isThought && onReplyToThought !== undefined)
          // Самая свежая строка играет анимацию появления один раз: класс feed-item--enter + одноразовый
          // обработчик animationend.
          const isEntering = item.key === enteringKey
          const enterCls = isEntering ? ' feed-item--enter' : ''
          const className = `feed-item feed-kind-${item.kind} tone-${item.tone}${item.mono ? ' mono' : ''}${isMd ? ' feed-item-markdown' : ''}${enterCls}`
          // Чистим флаг только когда закончилась анимация САМОЙ строки, а не всплывшая анимация потомка:
          // target===currentTarget надёжно в браузере и в jsdom.
          const onAnimEnd = isEntering
            ? (e: ReactAnimationEvent<HTMLElement>) => { if (e.target === e.currentTarget) onEntered?.() }
            : undefined
          const content = (
            <>
              {isMd ? (
                imagePolicy === 'none' ? (
                  // imagePolicy='none': [AFM image: …]-маркеры не раскрываются в <FeedImage> — у беседы
                  // нет каталога артефактов стадии, строить per-stage URL некуда.
                  <div className="feed-item-text md" dangerouslySetInnerHTML={{ __html: renderPlainMarkdownCached(item.text) }} />
                ) : (
                  // Нарратив агента — markdown. Нейтральный рендерер (без plan-обёрток), html:false → без
                  // XSS.
                  <div className="feed-item-segments">
                    {splitImageMarkers(item.text).map((seg, i) =>
                      seg.type === 'md' ? (
                        <div
                          key={`seg${i}`}
                          className="feed-item-text md"
                          dangerouslySetInnerHTML={{ __html: renderPlainMarkdownCached(seg.text) }}
                        />
                      ) : (
                        <FeedImage key={`seg${i}`} stageId={item.stageId} name={seg.name} />
                      ),
                    )}
                  </div>
                )
              ) : (
                <span className="feed-item-text" title={item.title}>
                  {item.text}
                  {item.executor !== undefined && (
                    <ExecutorBadge command={item.executor.raw} commandName={item.executor.label} className="conversation-command-chip" style={INLINE_BADGE_STYLE} />
                  )}
                </span>
              )}
              {(!timeInHead || item !== firstItem) && <MessageTime timestamp={item.ts} gap={item.gap} firstEvent={item.firstEvent} focusable={!isInteractiveRow} />}
            </>
          )
          if (item.navigable === true && onOpenDialog) {
            return (
              <button
                key={item.key}
                type="button"
                className={`${className} feed-item-navigable`}
                data-feed-item=""
                title="Open in dialog"
                onClick={() => onOpenDialog(item.stageId, item.phase ?? '', item.id ?? '')}
                onAnimationEnd={onAnimEnd}
              >
                {content}
              </button>
            )
          }
          // Мысль агента — репляибл ТОЛЬКО когда есть onReplyToThought. Отдельная реальная кнопка ↩ даёт
          // клавиатурную активацию/фокус-ринг бесплатно (без ручного keydown).
          if (onReplyToThought !== undefined && isThought) {
            const selected = item.key === activeReplyKey
            return (
              <div key={item.key} className="feed-thought-row" data-feed-item="">
                <div
                  className={`${className} feed-item-thought${selected ? ' is-reply-target' : ''}`}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest('a') === null) onReplyToThought(item.key, item.text)
                  }}
                  onAnimationEnd={onAnimEnd}
                >
                  {content}
                </div>
                <button
                  type="button"
                  className="feed-thought-reply"
                  aria-label={quoteRestarts ? 'Restart with quoted note' : 'Reply to this thought'}
                  aria-pressed={selected}
                  title={quoteRestarts ? 'Quote this thought in a restart note' : 'Reply to this thought in the composer'}
                  onClick={() => onReplyToThought(item.key, item.text)}
                >
                  ↩
                </button>
              </div>
            )
          }
          // reportVerificationId — только verify_result-строки с отчётом: рендерим ссылку "Show full
          // report" ПОД самим item, а не внутри него — тот же приём, что и feed-item-segments.
          return (
            <div key={item.key} className="feed-item-with-report" data-feed-item="">
              <div className={className} onAnimationEnd={onAnimEnd}>{content}</div>
              {item.reportVerificationId !== undefined && (
                <VerifyReportLink stageId={item.stageId} verificationId={item.reportVerificationId} />
              )}
            </div>
          )
        })}
      </div>
    </>
  )
  return (
    <div className={`feed-group feed-${group.side}${messageCard ? ' feed-history-message' : ''}${isStageAgent ? ' feed-authored' : ''}${isFlow ? ' feed-flow' : ''}`} data-actor={group.actor}>
      {messageCard ? <>
        <MeshAvatar name={authorLabel} kind={group.actor === 'user' ? 'user' : 'side_agent'} />
        <div className="feed-history-card">{content}</div>
      </> : isStageAgent ? <>
        <MeshAvatar name={group.stageId} kind="stage_agent" />
        <div className="feed-authored-body">{content}</div>
      </> : content}
    </div>
  )
}

// useToolCountBump — номер всплеска счётчика серии: растёт, когда число вызовов выросло внутри той же области.
// Первое значение и смена области (стадия, фильтр) — тихий baseline; уменьшение только обновляет baseline.
function useToolCountBump(count: number, scope: string): { bump: number; playing: boolean; onBumpEnd: () => void } {
  const [state, setState] = useState<{ scope: string | undefined; count: number; bump: number; playing: boolean }>({ scope: undefined, count, bump: 0, playing: false })
  if (state.scope !== scope) {
    setState({ scope, count, bump: 0, playing: false })
  } else if (count !== state.count) {
    const grew = count > state.count
    setState({ scope, count, bump: grew ? state.bump + 1 : state.bump, playing: grew })
  }
  const onBumpEnd = useCallback(() => setState((s) => (s.playing ? { ...s, playing: false } : s)), [])
  return { bump: state.bump, playing: state.playing, onBumpEnd }
}

// ShowEarlierButton — расширяет окно ленты или раскрытой tool-серии на шаг; hidden — сколько строк выше окна.
// useScrollAnchor — the scroll position before rows are inserted above: once `rowsKey` changes, the reader is moved
// down by their height, so what was being read stays in place.
function useScrollAnchor(rowsKey: number): (scroller: HTMLElement | null) => void {
  const anchor = useRef<{ scroller: HTMLElement; height: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const saved = anchor.current
    if (saved !== null) saved.scroller.scrollTop = saved.top + saved.scroller.scrollHeight - saved.height
    anchor.current = null
  }, [rowsKey])
  return useCallback((scroller) => {
    anchor.current = scroller === null ? null : { scroller, height: scroller.scrollHeight, top: scroller.scrollTop }
  }, [])
}

function ShowEarlierButton({ hidden, onClick }: { hidden: number; onClick: (event: ReactMouseEvent<HTMLButtonElement>) => void }): ReactElement {
  return (
    <button type="button" className="feed-show-earlier" onClick={onClick}>
      Show earlier ({hidden})
    </button>
  )
}

// ToolRun — подряд идущие вызовы инструментов агента: одна свёрнутая полоса вместо строки на каждый вызов.
// Число вызовов анимируется, когда у показанной серии добавился вызов. Строки монтируются только в раскрытом
// виде и ограничены окном: свёрнутые серии иначе были бы основной массой скрытых DOM-узлов.
const ToolRun = memo(function ToolRun({ items, countScope = '' }: { items: FeedItem[]; countScope?: string }): ReactElement {
  const { count, label, names } = toolRunSummary(items)
  const { bump, playing, onBumpEnd } = useToolCountBump(count, countScope)
  const [open, setOpen] = useState(false)
  const [limit, setLimit] = useState(FEED_WINDOW_STEP)
  const keepScroll = useScrollAnchor(limit)
  // flushSync: the feed's recheck listener on this toggle measures the layout right after, so the rows must be gone.
  const onToggle = (event: ReactSyntheticEvent<HTMLDetailsElement>): void => {
    const nowOpen = event.currentTarget.open
    flushSync(() => {
      setOpen(nowOpen)
      if (!nowOpen) setLimit(FEED_WINDOW_STEP)
    })
  }
  return (
    <details className="feed-tool-group" onToggle={onToggle}>
      <summary data-feed-item="">
        <strong>
          <span key={bump} className={`feed-tool-count${playing ? ' feed-tool-count--bump' : ''}`} onAnimationEnd={onBumpEnd}>{count}</span> {label}
        </strong>
        {names !== '' && <span className="feed-tool-names"> · {names}</span>}
      </summary>
      {open && (
        <div className="feed-tool-rows">
          {items.length > limit && <ShowEarlierButton hidden={items.length - limit} onClick={(event) => {
            keepScroll(event.currentTarget.closest<HTMLElement>('.feed-scroll'))
            setLimit((current) => current + FEED_WINDOW_STEP)
          }} />}
          {items.slice(-limit).map((item) => {
            const { name, detail } = toolCall(item)
            return (
              <div key={item.key} className="feed-item feed-kind-tool tone-neutral mono" data-feed-item="">
                <span className="feed-item-text">
                  <span className="feed-tool-name">{name}</span>
                  {detail !== '' && ` ${detail}`}
                </span>
                <MessageTime timestamp={item.ts} gap={item.gap} firstEvent={item.firstEvent} focusable />
              </div>
            )
          })}
        </div>
      )}
    </details>
  )
}, (prev, next) => prev.countScope === next.countScope && sameItems(prev.items, next.items))

type VerifyReportLinkProps = {
  stageId: string
  verificationId: string
}

// VerifyReportLink — раскрывающаяся ссылка "Show full report" под строкой verify_result.
function VerifyReportLink({ stageId, verificationId }: VerifyReportLinkProps): ReactElement {
  const [open, setOpen] = useState(false)
  const [content, setContent] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  const requested = useRef(false)

  const toggle = () => {
    if (!open && !requested.current) {
      requested.current = true
      getVerifyReport(stageId, verificationId)
        .then((text) => setContent(text))
        .catch(() => setFailed(true))
    }
    setOpen((v) => !v)
  }

  return (
    <div className="verify-report">
      <button type="button" className="verify-report-toggle" onClick={toggle}>
        {open ? 'Hide report' : 'Show full report'}
      </button>
      {open &&
        (failed ? (
          <div className="verify-report-error">Failed to load report</div>
        ) : content === null ? (
          <div className="verify-report-loading">Loading…</div>
        ) : (
          <div className="verify-report-body md" dangerouslySetInnerHTML={{ __html: renderPlainMarkdown(content) }} />
        ))}
    </div>
  )
}

// Число ретраев + бэкофф загрузки картинки. Контракт immutable → ретраить можно тем же URL.
const MAX_IMAGE_RETRIES = 3
const IMAGE_RETRY_BACKOFF_MS = 800

type FeedImageProps = {
  stageId: string
  name: string
}

// FeedImage — настоящий <img> для маркера [AFM image: <name>]. src целиком строит фронт из stageId+name
// через encodeURIComponent (никакой строки из данных агента в атрибуте — инъекции нет).
function FeedImage({ stageId, name }: FeedImageProps): ReactElement {
  const [attempt, setAttempt] = useState(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current)
    },
    [],
  )

  const base = `/api/stages/${encodeURIComponent(stageId)}/artifacts/${encodeURIComponent(name)}`
  const src = attempt === 0 ? base : `${base}?retry=${attempt}`

  const handleError = () => {
    // Исчерпали попытки или ретрай уже запланирован — ничего не делаем.
    if (attempt >= MAX_IMAGE_RETRIES || timer.current !== null) return
    timer.current = setTimeout(() => {
      timer.current = null
      setAttempt((a) => Math.min(a + 1, MAX_IMAGE_RETRIES))
    }, IMAGE_RETRY_BACKOFF_MS)
  }

  return <img className="feed-image" src={src} loading="lazy" alt="agent image" onError={handleError} />
}
