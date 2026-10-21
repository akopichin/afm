import { useEffect, useMemo, useRef, useState, type AnimationEvent as ReactAnimationEvent, type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useEnteringLast } from '../../hooks/use-entering-last'
import { toFeedItems, buildFeedTimeline, feedFilterCounts, isForYou, type FeedActor, type FeedFilter, type FeedGroup } from './feed-view-model'
import { JumpToLatestButton } from '../jump-to-latest'
import { FeedComposer, type ComposerDraft, type FeedComposerMeshProps, type MeshReplyTarget } from './FeedComposer'
import { renderPlainMarkdown } from '../plan-panel/markdown'
import { splitImageMarkers } from './split-image-markers'
import { getVerifyReport } from '../../api/verify-report-client'
import type { MeshMessage, MeshParticipant } from '../../api/mesh-client'
import { useStageConversation, type StageConversation } from '../../hooks/use-mesh'
import { useStageDiscussion } from '../../hooks/use-stage-discussion'
import { withStageDiscussion } from './stage-discussion-feed'
import { ConversationMessage } from '../team/ConversationMessage'
import { mentionHandles } from '../../lib/mesh-roster'
import { messageText } from '../../lib/agent-result'
import { sideAgentOutcome } from './mesh-receipt'
import { MessageTime } from '../message-time/MessageTime'

type FeedWorkspaceProps = {
  events: AfmEvent[]
  // stageId больше НЕ фильтрует events. Список events решает вызывающая сторона (App.tsx): per-stage Feed
  // — уже отфильтрованные useStageEvents, Full feed — весь events флоу.
  stageId: string | null
  // scopeToken — пространство имён текущего скоупа ленты. Не задан → дефолт из stageId.
  scopeToken?: string
  // showStageBadges — показывать бейдж стадии в шапке группы.
  showStageBadges?: boolean
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
  // systemAuthorLabel — подпись пузыря системного автора. Дефолт 'Flow' — обычная лента стадий.
  systemAuthorLabel?: string
  // imagePolicy — как трактовать маркеры [AFM image: …] в прозе агента. 'stage' (дефолт) — разбирать в
  // <FeedImage> с per-stage URL (лента стадий).
  imagePolicy?: 'stage' | 'none'
  // reviseAvailable — with mesh: the composer's stage takes a Revise now (its tab and replies to thoughts).
  // Without mesh the composer is the Revise note itself and is shown only when it is available.
  reviseAvailable?: boolean
  // composerDrafts — each stage's composer draft (ComposerDraft), owned by the caller so it outlives this view too
  // (the Team tab and back). Without it the drafts live as long as this FeedWorkspace.
  composerDrafts?: Map<string, ComposerDraft>
}

const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий.
export function feedItemSignature(item: { stageId: string; kind: string; actor: string; text: string }): string {
  return JSON.stringify([item.stageId, item.kind, item.actor, item.text])
}

export function FeedWorkspace({
  events,
  stageId,
  scopeToken,
  showStageBadges = false,
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
  systemAuthorLabel = 'Flow',
  imagePolicy = 'stage',
  mesh,
  reviseAvailable = true,
  composerDrafts,
}: FeedWorkspaceProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()
  // Drafts are per stage: switching stages (or the Feed following the run) remounts the composer for the new
  // stage, and coming back restores what was written and picked there.
  const ownDrafts = useRef(new Map<string, ComposerDraft>())
  const drafts = composerDrafts ?? ownDrafts.current
  const sectionRef = useRef<HTMLElement | null>(null)

  const items = useMemo(() => toFeedItems(events), [events])
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

  // Jump to a quoted message: show it (Author's work may hide it), scroll to it and flash it.
  const [jump, setJump] = useState<{ id: string; n: number } | null>(null)
  function jumpTo(id: string): void {
    const target = messageById.get(id)
    if (filter === 'author' && target !== undefined && !isForYou(target)) setFilter('all')
    setJump((prev) => ({ id, n: (prev?.n ?? 0) + 1 }))
  }
  useEffect(() => {
    if (jump === null) return
    const el = [...(sectionRef.current?.querySelectorAll<HTMLElement>('[data-message-id]') ?? [])].find((x) => x.dataset.messageId === jump.id)
    if (el === undefined) return
    el.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
    el.classList.add('is-jump-target')
    const t = setTimeout(() => el.classList.remove('is-jump-target'), 1600)
    return () => clearTimeout(t)
  }, [jump])

  // Анимация появления: только самая свежая строка ленты и только когда её КОНТЕНТ отличается от прошлой
  // свежей строки.
  const lastItem = items[items.length - 1]
  const lastKey = lastItem?.key ?? null
  const lastSig = lastItem ? feedItemSignature(lastItem) : null
  // Namespaced reset token so a stage legitimately named "full" can never collide
  // with the Full-feed scope (stage-id validation allows "full"; pkg/flow/flow.go).
  const resetToken = scopeToken ?? (stageId !== null ? `scope:stage:${stageId}` : 'scope:none')
  const { animate, onEntered } = useEnteringLast(lastSig, resetToken)
  const enteringKey = animate ? lastKey : null

  // Ответ на мысль агента: цель хранится ВМЕСТЕ со стадией, чтобы асинхронная отправка/смена стадии не
  // «протащили» чужую цитату.
  const [replyTo, setReplyTo] = useState<{ stageId: string; key: string; text: string } | null>(null)
  // Дерив: чип/выбор показываем только для стадии, на которой стоит композер —
  // даже до срабатывания эффекта сброса кадр со stale-цитатой невозможен.
  const activeReply = replyTo !== null && replyTo.stageId === noteTarget ? replyTo : null
  const activeReplyKey = activeReply?.key ?? null

  // The one conversation message being answered ("↩ Reply in conversation", Message mode) — separate from the
  // thought answered with Revise above, and stored with its stage the same way.
  const [meshReplyTo, setMeshReplyTo] = useState<{ stageId: string; target: MeshReplyTarget } | null>(null)
  // A reply whose conversation has since closed is over: nothing can be added to that thread any more.
  const replyThreadClosed = meshReplyTo !== null && conversation.threads[meshReplyTo.target.threadId]?.status === 'closed'
  const activeMeshReply = meshReplyTo !== null && meshReplyTo.stageId === noteTarget && !replyThreadClosed ? meshReplyTo.target : null
  useEffect(() => {
    if (replyThreadClosed) setMeshReplyTo(null)
  }, [replyThreadClosed])

  // Смена стадии — сбрасываем reply-таргеты (композер и так ремаунтится по key).
  useEffect(() => {
    setReplyTo(null)
    setMeshReplyTo(null)
  }, [noteTarget])

  const composerMounted = noteTarget !== null && onSendNote !== undefined
  // Reply in conversation needs the Message composer; a reply to a thought needs Revise.
  const canAnswerInConversation = composerMounted && mesh !== undefined
  const canAnswerThought = composerMounted && (mesh === undefined || reviseAvailable)

  return (
    <section className="feed-workspace" aria-label="Feed" ref={sectionRef}>
      {conversationMessages.length > 0 && (
        <FeedFilterChips filter={filter} onChange={setFilter} counts={filterCounts} />
      )}
      <div id="feed-content" className="feed-scroll" ref={feed.ref}>
        {timeline.length === 0 ? (
          <div className="empty-hint feed-empty">{items.length + conversationMessages.length === 0 ? (emptyHint ?? 'No events yet') : 'Nothing to show for this filter'}</div>
        ) : (
          timeline.map((entry) =>
            entry.kind === 'conversation' ? (
              <ul key={entry.key} className="team-conversation-message-list feed-conversation" aria-label="Conversation messages">
                {entry.messages.map((m, index) => (
                  <ConversationMessage
                    key={m.id}
                    message={m}
                    previousCreatedAt={entry.messages[index - 1]?.created_at}
                    participants={participants}
                    answered={m.in_reply_to !== undefined ? messageById.get(m.in_reply_to) : undefined}
                    onJumpToQuote={jumpTo}
                    replyActive={activeMeshReply?.messageId === m.id}
                    notAnswered={unanswered.has(m.id)}
                    sideOutcome={sideAgentOutcome(m, conversationMessages, mesh?.sideFailures ?? new Set())}
                    markdown
                    role={legacy.get(m.id)?.role}
                    evidence={legacy.get(m.id)?.evidence}
                    onReply={
                      canAnswerInConversation && noteTarget !== null && canReplyInConversation(m, conversation, participants)
                        ? () =>
                            setMeshReplyTo({
                              stageId: noteTarget,
                              target: {
                                messageId: m.id,
                                threadId: m.thread_id,
                                senderId: m.sender_id,
                                preview: messageText(m.body, true).slice(0, 120),
                                members: conversation.threads[m.thread_id]?.members ?? [],
                              },
                            })
                        : undefined
                    }
                  />
                ))}
              </ul>
            ) : (
              <FeedGroupView
                key={entry.key}
                group={entry.group}
                showStageBadges={showStageBadges}
                systemAuthorLabel={systemAuthorLabel}
                imagePolicy={imagePolicy}
                enteringKey={enteringKey}
                onEntered={onEntered}
                onOpenDialog={onOpenDialog}
                // Репляибл только per-stage Feed с живым композером: мысль выбирается
                // ТОЛЬКО когда есть куда доставлять ответ (noteTarget + onSendNote).
                onReplyToThought={
                  canAnswerThought && noteTarget !== null
                    ? (key, text) => setReplyTo({ stageId: noteTarget, key, text })
                    : undefined
                }
                activeReplyKey={activeReplyKey}
              />
            ),
          )
        )}
        {stageId !== null && <WorkingLine stageId={stageId} participants={participants} conversation={conversation} />}
        {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
      </div>

      {/* Messenger-поле заметки живому агенту — прибито к низу ленты, только когда
          стадия принимает заметки (noteTarget != null: running и не скрипт).
          key=noteTarget: смена стадии даёт свежий пустой черновик (черновик не
          «переедет» в другую стадию). */}
      {composerMounted && (
        <FeedComposer
          key={noteTarget}
          stageId={noteTarget}
          onSend={(text) => onSendNote(noteTarget, text)}
          replyQuote={activeReply?.text ?? null}
          replyKey={activeReply?.key}
          onCancelReply={() => setReplyTo(null)}
          // Сброс только совпадающего ключа: если пользователь выбрал другую
          // мысль, пока шла отправка, свежая цель переживёт (race-safe).
          onSent={(key) => setReplyTo((cur) => (cur?.key === key ? null : cur))}
          pauseOnFocusEnabled={pauseOnFocusEnabled}
          onTogglePauseOnFocus={onTogglePauseOnFocus}
          paused={composerPaused}
          onFocus={onComposerFocus}
          onBlur={onComposerBlur}
          onResumeNow={onComposerResumeNow}
          mesh={mesh}
          reviseAvailable={reviseAvailable}
          conversationMessages={conversationMessages}
          meshReply={activeMeshReply}
          onCancelMeshReply={() => setMeshReplyTo(null)}
          // Reset only if the same message is still being answered (another may have been picked meanwhile).
          onMeshReplySent={(id) => setMeshReplyTo((cur) => (cur?.target.messageId === id ? null : cur))}
          draft={drafts.get(noteTarget)}
          onDraftChange={(d) => drafts.set(noteTarget, d)}
        />
      )}
    </section>
  )
}

const EMPTY_PARTICIPANTS: MeshParticipant[] = []

// canReplyInConversation — the human may answer a message in its thread: someone else wrote it, the thread is
// still open and the human is a member of it (older threads agents opened among themselves are read-only), and
// the author can still read it.
function canReplyInConversation(m: MeshMessage, conversation: StageConversation, participants: MeshParticipant[]): boolean {
  if (m.sender_id === 'user') return false
  const thread = conversation.threads[m.thread_id]
  if (thread === undefined || thread.status === 'closed' || !thread.members.includes('user')) return false
  return participants.find((p) => p.id === m.sender_id)?.reachable === true
}

const FILTER_LABEL: Record<FeedFilter, string> = { all: 'All', conversations: 'Conversations', author: "Author's work" }

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
  // showStageBadges — см. FeedWorkspaceProps: пробрасывается явно с единственной
  // точки вызова в FeedWorkspace (codex #4 — было безусловным рендером бейджа).
  showStageBadges: boolean
  // systemAuthorLabel/imagePolicy — см. FeedWorkspaceProps.
  systemAuthorLabel?: string
  imagePolicy?: 'stage' | 'none'
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // onReplyToThought — выбрать мысль агента для ответа.
  onReplyToThought?: (key: string, text: string) => void
  // activeReplyKey — ключ выбранной мысли: ровно одна строка получает
  // is-reply-target (подсветка выбора).
  activeReplyKey?: string | null
  // enteringKey — the feed key of the single newest row that should play the entrance animation.
  enteringKey?: string | null
  onEntered?: () => void
}

// FeedGroupView — один пузырь: шапка + стопка item-строк.
export function FeedGroupView({ group, showStageBadges, systemAuthorLabel = 'Flow', imagePolicy = 'stage', onOpenDialog, onReplyToThought, activeReplyKey = null, enteringKey = null, onEntered }: FeedGroupViewProps): ReactElement {
  // Подпись автора: agent/user — фиксированные, system — параметризуемая (боковой
  // чат заменяет «Flow» своей подписью, см. systemAuthorLabel).
  const authorLabel = group.authorLabel ?? (group.actor === 'system' ? systemAuthorLabel : ACTOR_LABEL[group.actor])
  return (
    <div className={`feed-group feed-${group.side}`} data-actor={group.actor}>
      <div className="feed-group-head">
        {showStageBadges && group.stageId !== '' && <span className="feed-stage-badge">{group.stageId}</span>}
        <span className="feed-actor">{authorLabel}</span>
      </div>
      <div className="feed-bubble">
        {group.items.map((item) => {
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
                  <div className="feed-item-text md" dangerouslySetInnerHTML={{ __html: renderPlainMarkdown(item.text) }} />
                ) : (
                  // Нарратив агента — markdown. Нейтральный рендерер (без plan-обёрток), html:false → без
                  // XSS.
                  <div className="feed-item-segments">
                    {splitImageMarkers(item.text).map((seg, i) =>
                      seg.type === 'md' ? (
                        <div
                          key={`seg${i}`}
                          className="feed-item-text md"
                          dangerouslySetInnerHTML={{ __html: renderPlainMarkdown(seg.text) }}
                        />
                      ) : (
                        <FeedImage key={`seg${i}`} stageId={item.stageId} name={seg.name} />
                      ),
                    )}
                  </div>
                )
              ) : (
                <span className="feed-item-text">{item.text}</span>
              )}
              <MessageTime timestamp={item.ts} gap={item.gap} firstEvent={item.firstEvent} focusable={!isInteractiveRow} />
            </>
          )
          if (item.navigable === true && onOpenDialog) {
            return (
              <button
                key={item.key}
                type="button"
                className={`${className} feed-item-navigable`}
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
              <div key={item.key} className="feed-thought-row">
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
                  aria-label="Reply to this thought"
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
            <div key={item.key} className="feed-item-with-report">
              <div className={className} onAnimationEnd={onAnimEnd}>{content}</div>
              {item.reportVerificationId !== undefined && (
                <VerifyReportLink stageId={item.stageId} verificationId={item.reportVerificationId} />
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

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
