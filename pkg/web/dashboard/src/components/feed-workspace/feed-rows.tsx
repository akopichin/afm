import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, type AnimationEvent as ReactAnimationEvent, type CSSProperties, type ReactElement, type ReactNode, type SyntheticEvent as ReactSyntheticEvent } from 'react'
import { groupToolRuns, toolCall, toolRunSummary, type FeedActor, type FeedGroup, type FeedItem } from './feed-view-model'
import { renderPlainMarkdown, renderPlainMarkdownCached } from '../plan-panel/markdown'
import { imageUrl, reportKey } from './feed-row-state'
import { splitImageMarkers } from './split-image-markers'
import { getVerifyReport } from '../../api/verify-report-client'
import { MessageTime } from '../message-time/MessageTime'
import { MeshAvatar } from '../team/MeshAvatar'
import { ExecutorBadge } from '../ExecutorBadge'
import type { ReportCache } from './report-cache'

// Листовые рендеры ленты: общие для FeedGroupView (беседа с side-agent, рамочные пузыри) и для строк
// виртуального списка. Состояние раскрытий в них либо локальное, либо приходит от вызывающего.

export const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}
export const STAGE_AGENT_ROLE = 'Stage agent'

// The chip sits inline after a row's text: inline-block lets its width cap and ellipsis apply there.
const INLINE_BADGE_STYLE: CSSProperties = { display: 'inline-block', marginLeft: 8, verticalAlign: 'middle' }

// Число ретраев + бэкофф загрузки картинки. Контракт immutable → ретраить можно тем же URL.
const MAX_IMAGE_RETRIES = 3
const IMAGE_RETRY_BACKOFF_MS = 800

// ImageRetries — счётчик повторов загрузки картинок, который держит вызывающий: он переживает размонтаж строки.
export type ImageRetries = { attempts: ReadonlyMap<string, number>; onRetry: (url: string) => void }

type FeedImageProps = {
  stageId: string
  name: string
  retries?: ImageRetries
}

// FeedImage — настоящий <img> для маркера [AFM image: <name>]. src целиком строит фронт из stageId+name
// через encodeURIComponent (никакой строки из данных агента в атрибуте — инъекции нет).
export function FeedImage({ stageId, name, retries }: FeedImageProps): ReactElement {
  const [localAttempt, setLocalAttempt] = useState(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current)
    },
    [],
  )

  const base = imageUrl(stageId, name)
  const attempt = retries?.attempts.get(base) ?? localAttempt
  const src = attempt === 0 ? base : `${base}?retry=${attempt}`

  const handleError = () => {
    // Исчерпали попытки или ретрай уже запланирован — ничего не делаем.
    if (attempt >= MAX_IMAGE_RETRIES || timer.current !== null) return
    timer.current = setTimeout(() => {
      timer.current = null
      if (retries !== undefined) retries.onRetry(base)
      else setLocalAttempt((a) => Math.min(a + 1, MAX_IMAGE_RETRIES))
    }, IMAGE_RETRY_BACKOFF_MS)
  }

  return <img className="feed-image" src={src} loading="lazy" alt="agent image" onError={handleError} />
}

// ReportView — тело раскрытого отчёта verify: загрузка, ошибка или текст.
function ReportView({ state }: { state: { text: string } | 'failed' | 'loading' }): ReactElement {
  if (state === 'failed') return <div className="verify-report-error">Failed to load report</div>
  if (state === 'loading') return <div className="verify-report-loading">Loading…</div>
  return <div className="verify-report-body md" dangerouslySetInnerHTML={{ __html: renderPlainMarkdown(state.text) }} />
}

// VerifyReportLink — раскрывающаяся ссылка "Show full report" под строкой verify_result; состояние локальное.
export function VerifyReportLink({ stageId, verificationId }: { stageId: string; verificationId: string }): ReactElement {
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
      {open && <ReportView state={failed ? 'failed' : content === null ? 'loading' : { text: content }} />}
    </div>
  )
}

type ReportToggleProps = {
  stageId: string
  verificationId: string
  open: boolean
  onToggle: (open: boolean) => void
  cache: ReportCache
}

// ReportToggle — та же ссылка, но открытость держит вызывающий, а текст — общий кэш отчётов: после ухода строки
// с экрана отчёт открыт и не запрашивается заново. Слишком большой для кэша отчёт живёт только в смонтированной строке.
export function ReportToggle({ stageId, verificationId, open, onToggle, cache }: ReportToggleProps): ReactElement {
  const [fetched, setFetched] = useState<{ key: string; text: string } | null>(null)
  const [failed, setFailed] = useState(false)
  const key = reportKey(stageId, verificationId)
  const text = cache.peek(stageId, verificationId) ?? (fetched?.key === key ? fetched.text : undefined)
  useEffect(() => {
    if (!open || text !== undefined) return
    let live = true
    setFailed(false)
    cache.load(stageId, verificationId).then(
      (loaded) => live && setFetched({ key, text: loaded }),
      () => live && setFailed(true),
    )
    return () => {
      live = false
    }
  }, [open, text, cache, stageId, verificationId, key])

  return (
    <div className="verify-report">
      <button type="button" className="verify-report-toggle" aria-expanded={open} onClick={() => onToggle(!open)}>
        {open ? 'Hide report' : 'Show full report'}
      </button>
      {open && <ReportView state={failed ? 'failed' : text === undefined ? 'loading' : { text }} />}
    </div>
  )
}

type FeedItemRowProps = {
  item: FeedItem
  // timeInHead — время первой строки стоит в шапке автора, строка его не повторяет.
  timeInHead: boolean
  imagePolicy: 'stage' | 'none'
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // onReplyToThought — выбрать мысль агента для ответа. quoteRestarts — без mesh ответ на мысль — это перезапуск
  // с цитатой, и кнопка ↩ называет именно это действие.
  onReplyToThought?: (key: string, text: string) => void
  quoteRestarts: boolean
  // activeReplyKey — ключ выбранной мысли: ровно одна строка получает is-reply-target.
  activeReplyKey: string | null
  // entering — самая свежая строка играет анимацию появления один раз.
  entering: boolean
  onEntered?: () => void
  imageRetries?: ImageRetries
  // report — ссылка на отчёт verify под самой строкой.
  report?: ReactNode
}

// FeedItemRow — одно событие ленты: кнопка перехода к диалогу, мысль агента с ответом или обычная строка.
export function FeedItemRow({ item, timeInHead, imagePolicy, onOpenDialog, onReplyToThought, quoteRestarts, activeReplyKey, entering, onEntered, imageRetries, report }: FeedItemRowProps): ReactElement {
  const isMd = item.markdown === true
  const isThought = item.kind === 'message' && isMd
  const isInteractiveRow = (item.navigable === true && onOpenDialog !== undefined) || (isThought && onReplyToThought !== undefined)
  // Класс feed-item--enter + одноразовый обработчик animationend.
  const enterCls = entering ? ' feed-item--enter' : ''
  const className = `feed-item feed-kind-${item.kind} tone-${item.tone}${item.mono ? ' mono' : ''}${isMd ? ' feed-item-markdown' : ''}${enterCls}`
  // Чистим флаг только когда закончилась анимация САМОЙ строки, а не всплывшая анимация потомка:
  // target===currentTarget надёжно в браузере и в jsdom.
  const onAnimEnd = entering
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
          // Нарратив агента — markdown. Нейтральный рендерер (без plan-обёрток), html:false → без XSS.
          <div className="feed-item-segments">
            {splitImageMarkers(item.text).map((seg, i) =>
              seg.type === 'md' ? (
                <div
                  key={`seg${i}`}
                  className="feed-item-text md"
                  dangerouslySetInnerHTML={{ __html: renderPlainMarkdownCached(seg.text) }}
                />
              ) : (
                <FeedImage key={`seg${i}`} stageId={item.stageId} name={seg.name} retries={imageRetries} />
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
      {!timeInHead && <MessageTime timestamp={item.ts} gap={item.gap} firstEvent={item.firstEvent} focusable={!isInteractiveRow} />}
    </>
  )
  if (item.navigable === true && onOpenDialog) {
    return (
      <button
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
      <div className="feed-thought-row" data-feed-item="">
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
  // Отчёт verify — ПОД самой строкой, а не внутри неё: тот же приём, что и feed-item-segments.
  return (
    <div className="feed-item-with-report" data-feed-item="">
      <div className={className} onAnimationEnd={onAnimEnd}>{content}</div>
      {report}
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

// ToolSummaryLabel — подпись свёрнутой серии: число вызовов (анимируется, когда серия выросла) и имена инструментов.
export function ToolSummaryLabel({ items, countScope = '' }: { items: FeedItem[]; countScope?: string }): ReactElement {
  const { count, label, names } = toolRunSummary(items)
  const { bump, playing, onBumpEnd } = useToolCountBump(count, countScope)
  return (
    <>
      <strong>
        <span key={bump} className={`feed-tool-count${playing ? ' feed-tool-count--bump' : ''}`} onAnimationEnd={onBumpEnd}>{count}</span> {label}
      </strong>
      {names !== '' && <span className="feed-tool-names"> · {names}</span>}
    </>
  )
}

// ToolCallLine — строка одного вызова инструмента внутри раскрытой серии.
export function ToolCallLine({ item }: { item: FeedItem }): ReactElement {
  const { name, detail } = toolCall(item)
  return (
    <div className="feed-item feed-kind-tool tone-neutral mono" data-feed-item="">
      <span className="feed-item-text">
        <span className="feed-tool-name">{name}</span>
        {detail !== '' && ` ${detail}`}
      </span>
      <MessageTime timestamp={item.ts} gap={item.gap} firstEvent={item.firstEvent} focusable />
    </div>
  )
}

export function sameItem(a: FeedItem, b: FeedItem): boolean {
  const keys = Object.keys(a) as (keyof FeedItem)[]
  return keys.length === Object.keys(b).length && keys.every((key) => (key === 'executor' ? shallowEqual(a.executor, b.executor) : a[key] === b[key]))
}

export function sameItems(a: FeedItem[], b: FeedItem[]): boolean {
  return a.length === b.length && a.every((item, i) => sameItem(item, b[i] as FeedItem))
}

export function shallowEqual(a: object | undefined, b: object | undefined): boolean {
  if (a === b) return true
  if (a === undefined || b === undefined) return false
  const keys = Object.keys(a) as (keyof typeof a)[]
  return keys.length === Object.keys(b).length && keys.every((key) => a[key] === (b as typeof a)[key])
}

// TOOL_RUN_WINDOW — сколько вызовов раскрытой серии side-agent монтируется сразу; остальные открывает «Show earlier».
const TOOL_RUN_WINDOW = 150

// ToolRun — подряд идущие вызовы инструментов в беседе side-agent: одна свёрнутая полоса вместо строки на
// каждый вызов; строки монтируются только в раскрытом виде и ограничены окном (виртуальной ленты тут нет).
const ToolRun = memo(function ToolRun({ items, countScope = '' }: { items: FeedItem[]; countScope?: string }): ReactElement {
  const [open, setOpen] = useState(false)
  const [limit, setLimit] = useState(TOOL_RUN_WINDOW)
  // Строки добавляются выше читаемого: после показа более ранних вызовов скролл сдвигается на их высоту.
  const scrollBefore = useRef<{ scroller: HTMLElement; height: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const saved = scrollBefore.current
    scrollBefore.current = null
    if (saved !== null) saved.scroller.scrollTop = saved.top + saved.scroller.scrollHeight - saved.height
  }, [limit])
  const onToggle = (event: ReactSyntheticEvent<HTMLDetailsElement>): void => {
    const nowOpen = event.currentTarget.open
    setOpen(nowOpen)
    if (!nowOpen) setLimit(TOOL_RUN_WINDOW)
  }
  const showEarlier = (event: ReactSyntheticEvent<HTMLButtonElement>): void => {
    const scroller = event.currentTarget.closest<HTMLElement>('.feed-scroll')
    scrollBefore.current = scroller === null ? null : { scroller, height: scroller.scrollHeight, top: scroller.scrollTop }
    setLimit((current) => current + TOOL_RUN_WINDOW)
  }
  return (
    <details className="feed-tool-group" onToggle={onToggle}>
      <summary data-feed-item="">
        <ToolSummaryLabel items={items} countScope={countScope} />
      </summary>
      {open && (
        <div className="feed-tool-rows">
          {items.length > limit && (
            <button type="button" className="feed-show-earlier" onClick={showEarlier}>
              Show earlier ({items.length - limit})
            </button>
          )}
          {items.slice(-limit).map((item) => <ToolCallLine key={item.key} item={item} />)}
        </div>
      )}
    </details>
  )
}, (prev, next) => prev.countScope === next.countScope && sameItems(prev.items, next.items))

type FeedGroupViewProps = {
  group: FeedGroup
  // Assistant history can label the source stage of a group.
  showStageBadges: boolean
  // systemAuthorLabel — подпись системного автора в карточке истории (messageCard); imagePolicy — см. FeedWorkspaceProps.
  systemAuthorLabel?: string
  imagePolicy?: 'stage' | 'none'
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  onReplyToThought?: (key: string, text: string) => void
  quoteRestarts?: boolean
  activeReplyKey?: string | null
  // enteringKey — the feed key of the single newest row that should play the entrance animation.
  enteringKey?: string | null
  onEntered?: () => void
  // toolCountScope — what a tool run's count belongs to (the feed's stage and filter). Another scope starts the
  // count over without a bump: a filter can merge two runs into one without any new call.
  toolCountScope?: string
  messageCard?: boolean
}

// Items are rebuilt on every event, so a group is compared by content: only a group that changed re-renders.
function sameGroupProps(prev: FeedGroupViewProps, next: FeedGroupViewProps): boolean {
  const { group: prevGroup, ...prevRest } = prev
  const { group: nextGroup, ...nextRest } = next
  const sameGroup = prevGroup.key === nextGroup.key && prevGroup.side === nextGroup.side && prevGroup.stageId === nextGroup.stageId &&
    prevGroup.actor === nextGroup.actor && prevGroup.authorLabel === nextGroup.authorLabel && sameItems(prevGroup.items, nextGroup.items)
  return sameGroup && shallowEqual(prevRest, nextRest)
}

// FeedGroupView — одна группа целиком: стейдж-агент — строка автора и тело без рамки, события флоу — тихие строки,
// пользователь и карточки истории — пузырь с шапкой. Лента стадии режет группы на строки сама, группу целиком
// отдаёт рамочный пузырь пользователя и беседа side-agent.
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
          return (
            <FeedItemRow
              key={item.key}
              item={item}
              timeInHead={timeInHead && item === firstItem}
              imagePolicy={imagePolicy}
              onOpenDialog={onOpenDialog}
              onReplyToThought={onReplyToThought}
              quoteRestarts={quoteRestarts}
              activeReplyKey={activeReplyKey}
              entering={item.key === enteringKey}
              onEntered={onEntered}
              report={item.reportVerificationId !== undefined ? <VerifyReportLink stageId={item.stageId} verificationId={item.reportVerificationId} /> : undefined}
            />
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
