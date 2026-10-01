import { useEffect, useMemo, useRef, useState, type AnimationEvent as ReactAnimationEvent, type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'
import { useEnteringLast } from '../../hooks/use-entering-last'
import { toFeedItems, groupFeedItems, type FeedActor, type FeedGroup } from './feed-view-model'
import { JumpToLatestButton } from '../jump-to-latest'
import { FeedComposer } from './FeedComposer'
import { renderPlainMarkdown } from '../plan-panel/markdown'
import { splitImageMarkers } from './split-image-markers'
import { getVerifyReport } from '../../api/verify-report-client'

type FeedWorkspaceProps = {
  events: AfmEvent[]
  // stageId больше НЕ фильтрует events (FeedWorkspace — тупой рендерер: рисует
  // ровно то, что получил, никакой внутренней фильтрации по стадии). Список
  // events решает вызывающая сторона (App.tsx): per-stage Feed — уже
  // отфильтрованные useStageEvents, Full feed — весь events флоу. Оставлен в
  // сигнатуре компонента для параллелизма с вызывающим кодом (координаты
  // «какая стадия сейчас выбрана») — сам компонент это поле не читает.
  stageId: string | null
  // scopeToken — пространство имён текущего скоупа ленты (reset-токен для
  // useEnteringLast). App передаёт `scope:full` / `scope:stage:<id>` / `scope:none`,
  // чтобы стадия, легитимно названная `full`, не схлопнулась с Full-feed скоупом
  // (у обеих stageId может быть null). Не задан → дефолт из stageId.
  scopeToken?: string
  // showStageBadges — показывать бейдж стадии в шапке группы. false (дефолт) для
  // per-stage Feed (все строки — одна и та же стадия, бейдж — лишний шум); true
  // для Full feed (несколько стадий вперемешку — бейдж нужен для ориентации).
  showStageBadges?: boolean
  // emptyHint — текст пустого состояния, когда events пуст. Дефолт — 'No events
  // yet'; вызывающая сторона может передать более специфичный текст (например
  // «No events for this stage yet» для Feed конкретной стадии).
  emptyHint?: string
  // Клик по navigable-элементу ленты (dialog_question/dialog_answer) — открыть
  // диалог соответствующей стадии на нужном вопросе. Сама навигация — задача
  // другой таски (T6); здесь только проброс координат из FeedItem. Без пропа
  // navigable-элементы рендерятся как обычные (не интерактивные) строки.
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // noteTarget — стадия, живому агенту которой можно послать заметку прямо из
  // ленты (running и не скрипт; вычисляется в App). null → messenger-поле не
  // показываем. onSendNote доставляет заметку (через Revise) и ДОЛЖЕН
  // отклоняться при неудаче, чтобы FeedComposer сохранил текст.
  noteTarget?: string | null
  onSendNote?: (stageId: string, text: string) => Promise<void>
  // --- Pause on focus — сквозной проброс в FeedComposer (стейт живёт в App). ---
  pauseOnFocusEnabled?: boolean
  onTogglePauseOnFocus?: (v: boolean) => void
  // paused — стадия композера на паузе И поставлена на неё нами (считает App).
  composerPaused?: boolean
  onComposerFocus?: () => void
  onComposerBlur?: (hasDraft: boolean) => void
  onComposerResumeNow?: () => void
}

const ACTOR_LABEL: Record<FeedActor, string> = {
  agent: 'Agent',
  user: 'You',
  system: 'Flow',
}

// FeedWorkspace — единый воркспейс ленты: мессенджер-презентация РЕАЛЬНЫХ событий
// (агент/флоу слева, пользователь справа; tool/script — компактные mono-строки;
// success/warning/failure — семантические тональные поверхности). Пришёл на смену
// EventFeedPanel: без PanelFrame/Maximizable (воркспейс и так на всю ширину),
// stick-to-bottom + Jump to latest. ТУПОЙ рендерер: рендерит РОВНО те events, что
// получил — никакой внутренней фильтрации по стадии (её решает вызывающая сторона,
// App.tsx: per-stage Feed передаёт уже отфильтрованный список, Full feed — весь
// флоу). Мысли агента (agent_action tool="text") рендерятся здесь как проза — это
// событие ленты, отдельного лог-режима больше нет.
// Collision-free content signature of a feed row, used by useEnteringLast to decide
// whether the newest message changed. JSON.stringify of the tuple (not a '|'-join)
// so fields containing '|' can never collapse two different rows to one signature.
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
}: FeedWorkspaceProps): ReactElement {
  const feed = useStickToBottom<HTMLDivElement>()

  const groups = useMemo(() => groupFeedItems(toFeedItems(events)), [events])

  // Анимация появления: только самая свежая строка ленты и только когда её
  // КОНТЕНТ (а не timestamp-ключ) отличается от прошлой свежей строки (см.
  // useEnteringLast — реконнект/бэкфилл с тем же содержимым не анимируются).
  const lastItem = useMemo(() => {
    const g = groups[groups.length - 1]
    return g ? g.items[g.items.length - 1] : undefined
  }, [groups])
  const lastKey = lastItem?.key ?? null
  const lastSig = lastItem ? feedItemSignature(lastItem) : null
  // Namespaced reset token so a stage legitimately named "full" can never collide
  // with the Full-feed scope (stage-id validation allows "full"; pkg/flow/flow.go).
  const resetToken = scopeToken ?? (stageId !== null ? `scope:stage:${stageId}` : 'scope:none')
  const { animate, onEntered } = useEnteringLast(lastSig, resetToken)
  const enteringKey = animate ? lastKey : null

  // Ответ на мысль агента: цель хранится ВМЕСТЕ со стадией (stageId), чтобы
  // асинхронная отправка/смена стадии не «протащили» чужую цитату. text —
  // сырой Markdown мысли (для чипа и доставляемой цитаты), key — стабильный
  // ключ строки (маркирует выбранную строку + гейтит race-safe сброс).
  const [replyTo, setReplyTo] = useState<{ stageId: string; key: string; text: string } | null>(null)
  // Дерив: чип/выбор показываем только для стадии, на которой стоит композер —
  // даже до срабатывания эффекта сброса кадр со stale-цитатой невозможен.
  const activeReply = replyTo !== null && replyTo.stageId === noteTarget ? replyTo : null
  const activeReplyKey = activeReply?.key ?? null

  // Смена стадии — сбрасываем reply-таргет (композер и так ремаунтится по key).
  useEffect(() => setReplyTo(null), [noteTarget])

  return (
    <section className="feed-workspace" aria-label="Feed">
      <div id="feed-content" className="feed-scroll" ref={feed.ref}>
        {groups.length === 0 ? (
          <div className="empty-hint feed-empty">{emptyHint ?? 'No events yet'}</div>
        ) : (
          groups.map((g) => (
            <FeedGroupView
              key={g.key}
              group={g}
              showStageBadges={showStageBadges}
              enteringKey={enteringKey}
              onEntered={onEntered}
              onOpenDialog={onOpenDialog}
              // Репляибл только per-stage Feed с живым композером: мысль выбирается
              // ТОЛЬКО когда есть куда доставлять ответ (noteTarget + onSendNote).
              onReplyToThought={
                noteTarget !== null && onSendNote !== undefined
                  ? (key, text) => setReplyTo({ stageId: noteTarget, key, text })
                  : undefined
              }
              activeReplyKey={activeReplyKey}
            />
          ))
        )}
        {!feed.stick && <JumpToLatestButton onClick={feed.jumpToBottom} />}
      </div>

      {/* Messenger-поле заметки живому агенту — прибито к низу ленты, только когда
          стадия принимает заметки (noteTarget != null: running и не скрипт).
          key=noteTarget: смена стадии даёт свежий пустой черновик (черновик не
          «переедет» в другую стадию). */}
      {noteTarget !== null && onSendNote !== undefined && (
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
        />
      )}
    </section>
  )
}

type FeedGroupViewProps = {
  group: FeedGroup
  // showStageBadges — см. FeedWorkspaceProps: пробрасывается явно с единственной
  // точки вызова в FeedWorkspace (codex #4 — было безусловным рендером бейджа).
  showStageBadges: boolean
  onOpenDialog?: (stageId: string, phase: string, id: string) => void
  // onReplyToThought — выбрать мысль агента (kind message + markdown) для ответа.
  // Присутствует ТОЛЬКО у per-stage Feed с живым композером (см. FeedWorkspace):
  // где его нет (Full feed), мысли рендерятся обычной непереключаемой прозой.
  onReplyToThought?: (key: string, text: string) => void
  // activeReplyKey — ключ выбранной мысли: ровно одна строка получает
  // is-reply-target (подсветка выбора).
  activeReplyKey?: string | null
  // enteringKey — the feed key of the single newest row that should play the
  // entrance animation (null = none). onEntered clears it on that row's
  // animationend (one-shot; superseded by a newer last, so no replay).
  enteringKey?: string | null
  onEntered?: () => void
}

// FeedGroupView — один пузырь: шапка (стадия + актор) + стопка item-строк.
// navigable-элементы (dialog_question/dialog_answer, см. feed-view-model) при
// наличии onOpenDialog рендерятся кнопкой — переход к вопросу диалога стадии
// (сама навигация — задача T6, здесь только клик → проброс координат); без
// onOpenDialog или для остальных item — неизменный <div> как раньше.
// Мысли агента (kind message + markdown) при наличии onReplyToThought
// становятся репляиблыми (клик по строке/кнопке ↩ выбирает мысль для ответа).
export function FeedGroupView({ group, showStageBadges, onOpenDialog, onReplyToThought, activeReplyKey = null, enteringKey = null, onEntered }: FeedGroupViewProps): ReactElement {
  return (
    <div className={`feed-group feed-${group.side}`} data-actor={group.actor}>
      <div className="feed-group-head">
        {showStageBadges && group.stageId !== '' && <span className="feed-stage-badge">{group.stageId}</span>}
        <span className="feed-actor">{ACTOR_LABEL[group.actor]}</span>
      </div>
      <div className="feed-bubble">
        {group.items.map((item) => {
          const isMd = item.markdown === true
          // Самая свежая строка играет анимацию появления один раз: класс
          // feed-item--enter + одноразовый обработчик animationend (чистит флаг
          // через onEntered). Новое сообщение переназначит enteringKey на новую
          // последнюю строку — старая теряет класс по позиции, реплея нет.
          const isEntering = item.key === enteringKey
          const enterCls = isEntering ? ' feed-item--enter' : ''
          const className = `feed-item feed-kind-${item.kind} tone-${item.tone}${item.mono ? ' mono' : ''}${isMd ? ' feed-item-markdown' : ''}${enterCls}`
          // Чистим флаг только когда закончилась анимация САМОЙ строки (её
          // единственная анимация — feedItemIn), а не всплывшая анимация потомка:
          // target===currentTarget надёжно в браузере и в jsdom (где animationName
          // не проставляется, поэтому фильтр по имени здесь не годится).
          const onAnimEnd = isEntering
            ? (e: ReactAnimationEvent<HTMLElement>) => { if (e.target === e.currentTarget) onEntered?.() }
            : undefined
          const content = (
            <>
              {isMd ? (
                // Нарратив агента — markdown (заголовки/таблицы/жирный/код). Нейтральный
                // рендерер (без plan-обёрток), html:false → без XSS. Текст сегментируется
                // по standalone-маркерам [AFM image: <name>]: md-куски рендерятся как
                // раньше, маркеры — настоящим <img> (FeedImage), src которого фронт строит
                // сам из stageId+name (никакой строки агента в атрибуте).
                // .feed-item — flex-РЯД (текст слева, timestamp справа), поэтому сегменты
                // (несколько md/img) заворачиваем в колоночный контейнер, иначе при
                // тексте вокруг картинки или нескольких картинках они встали бы в ряд и
                // сжались/переполнились вместо вертикального порядка. Timestamp остаётся
                // отдельным flex-элементом ряда.
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
              ) : (
                <span className="feed-item-text">{item.text}</span>
              )}
              {item.gap !== '—' && <span className="feed-item-gap">{item.gap}</span>}
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
          // Мысль агента (kind message + markdown) — репляибл ТОЛЬКО когда есть
          // onReplyToThought (per-stage Feed с живым композером). Контейнер прозы
          // остаётся НЕинтерактивным <div> (может содержать <a> из linkify —
          // вкладывать его в role=button нельзя): клик по строке выбирает мысль,
          // но не по ссылке (closest('a') !== null → это клик по ссылке,
          // пропускаем). Отдельная реальная кнопка ↩ даёт клавиатурную
          // активацию/фокус-ринг бесплатно (без ручного keydown).
          const isThought = item.kind === 'message' && item.markdown === true
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
          // reportVerificationId — только verify_result-строки с отчётом
          // (см. feed-view-model.ts): рендерим ссылку "Show full report" ПОД
          // самим item, а не внутри него (item — flex-РЯД, отчёт — блок
          // произвольной высоты) — тот же приём, что и feed-item-segments.
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

// VerifyReportLink — раскрывающаяся ссылка "Show full report" под строкой
// verify_result (V5b.1). Отчёт запрашивается ЛЕНИВО, по первому клику (не при
// монтировании ленты — desятки verify-результатов не должны бить по сети
// сразу), и кэшируется в локальном состоянии: повторное сворачивание/
// разворачивание не перезапрашивает (см. requested-ref). Адресация —
// ТОЛЬКО stageId + verificationId (оба уже известны фронту из самого
// события) — сервер сам резолвит их в report.md (V5b.3); клиентский
// filesystem-путь из payload'а (report_path) сюда никогда не попадает.
// Контент untrusted (текст модели) — рендерится ТЕМ ЖЕ санитайзером, что и
// агентская проза (renderPlainMarkdown, markdown-it html:false → без XSS).
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

// Число ретраев + бэкофф загрузки картинки. Defense-in-depth поверх атомарного
// контракта публикации артефакта (temp→rename→маркер): на случай live-гонки, когда
// маркер прочитан на миг раньше, чем файл виден серверу, короткий бэкофф даёт файлу
// «доехать». Контракт immutable → ретраить можно тем же URL.
const MAX_IMAGE_RETRIES = 3
const IMAGE_RETRY_BACKOFF_MS = 800

type FeedImageProps = {
  stageId: string
  name: string
}

// FeedImage — настоящий <img> для маркера [AFM image: <name>]. src целиком строит
// фронт из stageId+name через encodeURIComponent (никакой строки из данных агента в
// атрибуте — инъекции нет). При ошибке загрузки — бэкофф-ретрай (до MAX_IMAGE_RETRIES)
// с cache-busting-параметром ?retry=N, чтобы браузер перезапросил тот же артефакт.
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
