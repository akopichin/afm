import { Fragment, useEffect, useId, useRef, useState, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { STAGE_STATUS_LABELS, type Stage, type StageVerify } from '../../types'
import type { AccountingState, Coverage } from '../../types/cost'
import { attentionKindFor } from '../../hooks/use-attention'
import type { StageAssistantsSummary } from '../../hooks/use-stage-assistants'
import { AssistantTaskRow } from '../assistants'

// Ширина меню — должна совпадать с min-width в .stage-kebab-menu (agent-note-modal.css),
// иначе right-выравнивание относительно кнопки съедет.
const KEBAB_MENU_WIDTH = 200

type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  // Заметку живому агенту шлют теперь из messenger-поля внизу ленты, а не из кебаба — поэтому onAddNote
  // убран. Кебаб оставляет pre-note (pending), кастомные кнопки стадии и Pause.
  onEditPreNote?: (stageId: string) => void // «Add note (before start)» (pending-стадия)
  onPause?: (stageId: string) => void
  // collaborationEnabled — capabilities.collaboration. Гейтит пункт кебаба «Attach helper…»: без включённой
  // фичи прикреплять помощника некуда (сервер всё равно вернул бы 404).
  collaborationEnabled?: boolean
  // onAttachHelper — открыть диалог прикрепления помощника к стадии (App владеет модалкой).
  onAttachHelper?: (stageId: string) => void
  // canAddStage — capabilities.add_stage; показывает кнопку «+ Stage» в шапке рейла. onAddStage открывает
  // диалог добавления динамической стадии (App владеет модалкой).
  canAddStage?: boolean
  onAddStage?: () => void
  // onButton — клик по предопределённой в flow.yaml кнопке стадии: передаём только метку, сервер сам
  // резолвит промпт и доставляет его живому агенту через Revise.
  onButton?: (stageId: string, name: string) => void
  // Прогресс прогона — компактно в шапке рейла. Пришёл сюда из удалённого футера (план §5.2).
  progressDone?: number
  progressTotal?: number
  // accounting — состояние учёта затрат с бэкенда.
  accounting: AccountingState
  // version — версия afm: тихая моно-строка, прибитая к низу рейла. Опциональна, чтобы существующие
  // вызовы/тесты без неё оставались валидными; пустая/undefined → футер не рендерится.
  version?: string
  // assistants — per-stage helper-task tree + aggregate, from useStageAssistants' OWN poll (not /api/status —
  // see AGENTS.md: the status payload stays un-fattened). Absent/undefined stage id → no data yet or
  // capability off; omitting the whole prop (existing callers/tests) renders the rail exactly as before.
  assistants?: Record<string, StageAssistantsSummary>
  selectedTaskId?: string | null
  onSelectTask?: (stageId: string, taskId: string) => void
}

// verifyBadgeGlyph/verifyBadgeTitle — единая точка презентации индикатора: один нейтральный глиф "AI
// verify", цвет несёт data-phase, полный текст — в title.
function verifyPhaseLabel(phase: StageVerify['phase']): string {
  switch (phase) {
    case 'running':
      return 'in progress'
    case 'pass':
      return 'passed'
    case 'accepted_stalemate':
      return 'accepted (stalemate)'
    case 'needs_changes':
      return 'needs changes'
    case 'inconclusive':
      return 'inconclusive'
    case 'error':
      return 'execution error'
  }
}

// isStaleRunningVerify — defensive gate: a terminal (done/failed) stage must never render the pulsing
// "running" verify badge. A real verify pass ends on a terminal phase (pass/accepted_stalemate/…), so a
// `running` phase lingering on an already-terminal stage is stale; hide the badge rather than pulse amber
// beside a green done dot.
function isStaleRunningVerify(status: Stage['status'], phase: StageVerify['phase']): boolean {
  return (status === 'done' || status === 'failed') && phase === 'running'
}

// Статусы, для которых имеет смысл плейсхолдер «оценка ожидается» — стадия
// реально гоняет агента ПРЯМО СЕЙЧАС (а не просто ждёт человека/бэкоффа).
const COST_PENDING_STATUSES: ReadonlySet<Stage['status']> = new Set(['planning', 'running', 'revising'])

// Accessible cost description distinguishes full, partial, and absent pricing coverage.
function costA11yText(displayCost: string, coverage: Coverage): string {
  if (coverage === 'none') return 'Estimated cost unavailable'
  const suffix = coverage === 'partial' ? ', partial pricing coverage' : ''
  return `Estimated cost ${displayCost}${suffix}`
}

// railCost показывает стоимость или pending-индикатор оценки, учитывая showMoney, health и статус стадии.
function railCost(stage: Stage, accounting: AccountingState): { text: string; a11y: string; pending?: boolean } | null {
  // show_money выключен → в рейле не показываем ни стоимость, ни плейсхолдер оценки: деньги спрятаны всюду
  // в дашборде (токены в рейле не показываются вовсе, только в шапке/таблице Cost).
  if (!(accounting.supported && accounting.showMoney)) return null
  if (stage.cost != null) {
    return { text: stage.cost.displayCost, a11y: costA11yText(stage.cost.displayCost, stage.cost.coverage) }
  }
  const canEstimate = !stage.isScript && accounting.supported && accounting.health === 'ok' && COST_PENDING_STATUSES.has(stage.status)
  if (!canEstimate) return null
  // pending — маленький спиннер вместо текста (сам символ рисуется в CSS);
  // text пустой, чтобы визуальная ветка не показывала ничего лишнего рядом.
  return { text: '', a11y: 'Estimated cost pending', pending: true }
}

// Статусы, при которых у стадии доступен кебаб хоть с одним пунктом.
// Статусы, из которых можно поставить стадию на паузу вручную (POST /api/stages/{id}/pause). awaiting_peer —
// стадия припаркована в ожидании коллеги: пауза держит её, Continue возвращает в ожидание (и продолжает сама,
// если ожидание тем временем ответили).
const PAUSABLE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'planning', 'revising', 'retrying', 'awaiting_peer'])

// Статусы, при которых у стадии есть ЖИВОЙ агент, которому Revise доставит промпт:
// running/awaiting_approval и никогда скрипт. Гейтит кастомные кнопки стадии (см. hasLiveAgent).
const LIVE_AGENT_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval'])

// Pre-note (заметка ДО старта) доступна только пока стадия pending и только у
// стадий с агентом — у скрипта нет агента, которому вклеить заметку в контекст.
function canPreNote(stage: Stage): boolean {
  return stage.status === 'pending' && !stage.isScript
}

// У стадии есть живой агент (running/awaiting_approval, не скрипт) — под ним и
// живут кастомные кнопки стадии, которые Revise доставляет как промпт.
function hasLiveAgent(stage: Stage): boolean {
  return LIVE_AGENT_STATUSES.has(stage.status) && !stage.isScript
}

// Ручная пауза — из PAUSABLE_STATUSES, но не на running-скрипте (его нельзя
// прервать gracefully). Совпадает с гейтом пункта Pause в JSX.
function canPause(stage: Stage): boolean {
  return PAUSABLE_STATUSES.has(stage.status) && !(stage.isScript && stage.status === 'running')
}

// Прикрепить помощника можно к любой не-скрипт стадии, которая ещё не достигла терминального статуса
// (сервер отвергает done/failed и script — см. orchestrator.Attach). Совпадает с гейтом пункта в JSX.
export function canAttachHelper(stage: Stage, collaborationEnabled: boolean): boolean {
  return collaborationEnabled && !stage.isScript && stage.status !== 'done' && stage.status !== 'failed'
}

// Кебаб показываем ТОЛЬКО если реально есть хоть один пункт меню — считаем из тех же предикатов, что и
// сами пункты.
function hasKebab(stage: Stage, collaborationEnabled: boolean): boolean {
  return (
    (hasLiveAgent(stage) && stage.buttons.length > 0) ||
    canPreNote(stage) ||
    canPause(stage) ||
    canAttachHelper(stage, collaborationEnabled)
  )
}

// Левая панель: список стадий с выбором активной.
export function StagesList({
  stages, selectedStageId, onSelect, onEditPreNote, onPause, onButton, progressDone, progressTotal, accounting,
  version, assistants, selectedTaskId, onSelectTask, collaborationEnabled = false, onAttachHelper,
  canAddStage = false, onAddStage,
}: StagesListProps): ReactElement {
  // useId нельзя звать внутри stages.map — берём одну базу на компонент и добавляем к ней индекс строки,
  // чтобы id стоимостного спана оставался уникальным и стабильным для React.
  const costIdBase = useId()
  const prevStatus = useRef<Record<string, string>>({})
  const timers = useRef<Record<string, number>>({})
  const [justDone, setJustDone] = useState<Set<string>>(new Set())
  // Какая стадия сейчас показывает открытое кебаб-меню.
  const [openMenuStageId, setOpenMenuStageId] = useState<string | null>(null)
  // .stages-scroll скроллится — абсолютно спозиционированное меню внутри него обрезалось бы краем панели,
  // если строка стадии оказывается ближе к низу видимой области.
  const [menuPos, setMenuPos] = useState<{ top: number; left: number } | null>(null)
  const menuRef = useRef<HTMLUListElement | null>(null)
  const openButtonRef = useRef<HTMLButtonElement | null>(null)

  useEffect(() => {
    if (openMenuStageId === null) return

    const close = () => {
      setOpenMenuStageId(null)
      setMenuPos(null)
    }
    const onMouseDown = (e: MouseEvent) => {
      const target = e.target as Node
      if (openButtonRef.current?.contains(target)) return
      if (menuRef.current?.contains(target)) return
      close()
    }

    document.addEventListener('mousedown', onMouseDown)
    // Меню спозиционировано fixed относительно кнопки внутри списка стадий. Скролл не всплывает, поэтому
    // чужие контейнеры до этого слушателя не дойдут.
    const scroller = document.querySelector('.stages-scroll')
    scroller?.addEventListener('scroll', close)
    return () => {
      document.removeEventListener('mousedown', onMouseDown)
      scroller?.removeEventListener('scroll', close)
    }
  }, [openMenuStageId])

  useEffect(() => {
    const newly: string[] = []
    for (const stage of stages) {
      const prev = prevStatus.current[stage.id]
      if (prev !== undefined && prev !== 'done' && stage.status === 'done') {
        newly.push(stage.id)
      }
      prevStatus.current[stage.id] = stage.status
    }
    if (newly.length === 0) return

    setJustDone((prev) => {
      const next = new Set(prev)
      newly.forEach((id) => next.add(id))
      return next
    })
    // Отдельный таймер на каждую стадию, чтобы повторный прогон эффекта от обычного поллинга не отменял
    // отложенную очистку другой стадии — иначе just-done залипал бы навсегда.
    newly.forEach((id) => {
      if (timers.current[id] !== undefined) window.clearTimeout(timers.current[id])
      timers.current[id] = window.setTimeout(() => {
        delete timers.current[id]
        setJustDone((prev) => {
          const next = new Set(prev)
          next.delete(id)
          return next
        })
      }, 700)
    })
  }, [stages])

  // Чистим все висящие таймеры только при размонтировании.
  useEffect(() => {
    const t = timers.current
    return () => {
      Object.values(t).forEach((id) => window.clearTimeout(id))
    }
  }, [])

  // expandedBranches — какие стадии (по id стадии) и/или задачи (по task_id, для будущей рекурсии) сейчас
  // показывают свою ветку помощников развёрнутой. Один Set на оба уровня: узел с детьми сам решает,
  // развёрнут ли ОН, по собственному id — глубина не зашита числом (dev-plan.md T3.9).
  const [expandedBranches, setExpandedBranches] = useState<Set<string>>(new Set())
  // autoExpandedStages — стадии, чья ветка уже получила свой ОДИН автоматический авто-разворот при первом
  // появлении помощников. Без этой памяти ручное сворачивание пользователем откатывалось бы назад на
  // каждом следующем поллинге (useStageAssistants обновляет assistants каждые 3с).
  const autoExpandedStages = useRef<Set<string>>(new Set())
  useEffect(() => {
    if (assistants === undefined) return
    const newlySeen = Object.entries(assistants).filter(
      ([stageId, summary]) => summary.count > 0 && !autoExpandedStages.current.has(stageId),
    )
    if (newlySeen.length === 0) return
    newlySeen.forEach(([stageId]) => autoExpandedStages.current.add(stageId))
    setExpandedBranches((prev) => {
      const next = new Set(prev)
      newlySeen.forEach(([stageId]) => next.add(stageId))
      return next
    })
  }, [assistants])

  function toggleBranch(id: string): void {
    setExpandedBranches((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <aside id="stages-panel">
      <div className="rail-head">
        <h2>Stages</h2>
        {canAddStage && (
          <button type="button" className="rail-add-stage" onClick={() => onAddStage?.()} title="Add a dynamic stage">
            + Stage
          </button>
        )}
        {progressTotal !== undefined && progressTotal > 0 && (
          <div className="rail-progress" title={`${progressDone ?? 0} of ${progressTotal} stages done`}>
            <span id="progress-text" className="rail-progress-text">{progressDone ?? 0} / {progressTotal}</span>
            <span className="rail-progress-bar" aria-hidden="true">
              <span
                id="progress-fill"
                className="rail-progress-fill"
                style={{ width: `${Math.round(((progressDone ?? 0) / progressTotal) * 100)}%` }}
              />
            </span>
          </div>
        )}
      </div>
      <div className="stages-scroll">
        <ul id="stages-list" className="stages-list">
        {stages.map((stage, index) => {
          // railCost — null означает «пустой рейл»: ни фигуры, ни описания. costId существует, только пока
          // есть что описывать — иначе aria-describedby указывал бы в никуда.
          const cost = railCost(stage, accounting)
          const costId = cost !== null ? `${costIdBase}-cost-${index}` : undefined
          // summary — this stage's helper-task tree from its OWN poll (useStageAssistants), not /api/status.
          // null (prop never passed, or stage not in the map yet) and {count:0} both render no branch.
          const summary = assistants?.[stage.id] ?? null
          const hasBranch = summary !== null && summary.count > 0
          const isExpandedBranch = hasBranch && expandedBranches.has(stage.id)
          // awaitingAssistantsBlocked (T7.9) — true only when awaiting_assistants' own required gate needs a
          // human decision, not merely "a helper is still working" (see attentionKindFor's own doc comment).
          const awaitingAssistantsBlocked = stage.status === 'awaiting_assistants' && stage.awaitingAssistantsBlocked === true
          return (
          <Fragment key={stage.id}>
          <li
            className={`stage-item${stage.id === selectedStageId ? ' active' : ''}${justDone.has(stage.id) ? ' just-done' : ''}`}
            data-stage-id={stage.id}
            data-status={stage.status}
            data-attention={attentionKindFor(stage) !== null ? 'true' : undefined}
          >
            {/* Сама строка — настоящая <button> (Enter/Space/focus-visible из
                коробки): рейл стал основным способом открыть стадию, поэтому он
                обязан быть доступен с клавиатуры. Кебаб — ОТДЕЛЬНАЯ соседняя
                кнопка (вложенные интерактивные элементы недопустимы), поэтому
                живёт в .stage-actions рядом, а не внутри .stage-row. */}
            {/* title — статус во всплывающей подсказке: текстовой строки статуса
                больше нет, а по цвету точки running/revising/retrying не различить
                (все амбер) — так статус доступен наведением, не занимая места. */}
            <button
              type="button"
              className="stage-row"
              title={STAGE_STATUS_LABELS[stage.status]}
              onClick={() => onSelect(stage.id)}
              aria-describedby={costId}
            >
              <span
                className="status-dot"
                data-status={stage.status}
                data-blocked={awaitingAssistantsBlocked ? 'true' : undefined}
              >
                <span className="dot-check" aria-hidden="true">✓</span>
                <span className="dot-num" aria-hidden="true">{index + 1}</span>
              </span>
              {/* Две строки: id из flow.yaml жирным (основная), длинный name —
                  приглушённой второй строкой. «Stage N» и текстовый статус убраны
                  (статус несёт сама нода-точка: цвет/анимация/галочка + бейджи).
                  Статус для скринридеров — visually-hidden строкой в accessible
                  name кнопки (сама точка помечена aria-hidden). */}
              <span className="stage-label">
                <span className="stage-name">{stage.id}</span>
                {stage.name !== '' && stage.name !== stage.id && (
                  <span className="stage-subname">{stage.name}</span>
                )}
                <span className="stage-status-sr">{STAGE_STATUS_LABELS[stage.status]}</span>
              </span>
            </button>
            {/* Единый трейлинг-слот: бейджи + кебаб. Отдельная от .stage-row
                ячейка строки, чтобы кебаб не оказался вложен в кнопку строки. */}
            <span className="stage-actions">
            {stage.status === 'awaiting_user_input' && <span className="dialog-badge" title="Awaiting your reply">💬</span>}
            {stage.status === 'awaiting_approval' && (
              <span className="approval-badge" title="Awaiting plan approval">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <circle cx="8" cy="8" r="7" fill="currentColor" />
                  <rect x="7" y="3.5" width="2" height="6" rx="1" fill="var(--surface-canvas)" />
                  <circle cx="8" cy="11.5" r="1.15" fill="var(--surface-canvas)" />
                </svg>
              </span>
            )}
            {awaitingAssistantsBlocked && (
              <span className="assistants-blocked-badge" title="A required helper task needs your decision">⚠</span>
            )}
            {stage.preNote !== '' && <span className="prenote-badge" title="Note attached for agent">📝</span>}
            {stage.verify !== undefined && !isStaleRunningVerify(stage.status, stage.verify.phase) && (
              <span
                className="verify-badge"
                data-phase={stage.verify.phase}
                title={`AI verify step ${stage.verify.step} · ${stage.verify.command}: ${verifyPhaseLabel(stage.verify.phase)}`}
              >
                🔎
              </span>
            )}
            {/* Стоимость — тихий моно-спан ПЕРЕД кебабом. Видимая цифра
                помечена aria-hidden (тон/ellipsis не несут собственного
                смысла для скринридера); полное предложение — в спрятанном
                узле, на который указывает aria-describedby строки. */}
            {cost !== null && (
              <span className="stage-cost">
                {cost.pending
                  ? <span className="stage-cost-spinner" aria-hidden="true" />
                  : <span aria-hidden="true">{cost.text}</span>}
                <span id={costId} className="stage-cost-sr">{cost.a11y}</span>
              </span>
            )}
            {summary !== null && hasBranch && (
              <span className="assistant-branch-toggle-wrap">
                <button
                  type="button"
                  className="assistant-toggle"
                  aria-label={isExpandedBranch ? `Collapse helper tasks for ${stage.id}` : `Expand helper tasks for ${stage.id}`}
                  aria-expanded={isExpandedBranch}
                  onClick={(e) => {
                    e.stopPropagation() // не триггерить onSelect клика по строке
                    toggleBranch(stage.id)
                  }}
                >
                  <span aria-hidden="true">{isExpandedBranch ? '▾' : '▸'}</span>
                  {/* Свёрнутая ветка показывает агрегат: счётчик задач + флаг внимания (кто-то на паузе,
                      ждёт решения). Развёрнутая ветка уже показывает сами строки ниже — агрегат ей не нужен. */}
                  {!isExpandedBranch && (
                    <span
                      className="assistant-branch-aggregate"
                      data-attention={summary.hasAttention ? 'true' : undefined}
                      title={summary.hasAttention ? `${summary.count} helper task(s), needs attention` : `${summary.count} helper task(s)`}
                    >
                      {summary.count}
                    </span>
                  )}
                </button>
              </span>
            )}
            {hasKebab(stage, collaborationEnabled) && (
              <span className="stage-kebab-wrap">
                <button
                  type="button"
                  className="stage-kebab"
                  aria-label="More actions"
                  onClick={(e) => {
                    e.stopPropagation() // не триггерить onSelect клика по строке
                    if (openMenuStageId === stage.id) {
                      setOpenMenuStageId(null)
                      setMenuPos(null)
                      return
                    }
                    const rect = e.currentTarget.getBoundingClientRect()
                    openButtonRef.current = e.currentTarget
                    setMenuPos({ top: rect.bottom + 4, left: Math.max(8, rect.right - KEBAB_MENU_WIDTH) })
                    setOpenMenuStageId(stage.id)
                  }}
                >
                  ⋮
                </button>
                {openMenuStageId === stage.id &&
                  menuPos !== null &&
                  createPortal(
                    <ul
                      className="stage-kebab-menu"
                      ref={menuRef}
                      style={{ position: 'fixed', top: menuPos.top, left: menuPos.left }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      {/* Кастомные кнопки стадии (flow.yaml) — при живом агенте
                          (running/awaiting_approval, не скрипт), которому Revise
                          доставит промпт. Показываем, только если кнопки реально
                          объявлены. Разделитель больше не нужен: это верхний
                          блок меню (пункт «Add note for agent» убран). */}
                      {hasLiveAgent(stage) && stage.buttons.length > 0 && (
                        <>
                          {stage.buttons.map((label) => (
                            <li key={`btn-${label}`} className="stage-kebab-buttons">
                              <button
                                type="button"
                                onClick={() => {
                                  setOpenMenuStageId(null)
                                  setMenuPos(null)
                                  onButton?.(stage.id, label)
                                }}
                              >
                                {label}
                              </button>
                            </li>
                          ))}
                        </>
                      )}
                      {canPreNote(stage) && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onEditPreNote?.(stage.id)
                            }}
                          >
                            {stage.preNote === '' ? 'Add note (before start)' : 'Edit note (before start)'}
                          </button>
                        </li>
                      )}
                      {canPause(stage) && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onPause?.(stage.id)
                            }}
                          >
                            Pause
                          </button>
                        </li>
                      )}
                      {canAttachHelper(stage, collaborationEnabled) && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onAttachHelper?.(stage.id)
                            }}
                          >
                            Attach helper…
                          </button>
                        </li>
                      )}
                    </ul>,
                    document.body,
                  )}
              </span>
            )}
            </span>
            {/* Коннектор — абсолютно спозиционированная линия к НОДЕ следующей стадии, её пиксельная
                математика (stages-list.css) считает ровно одну строку на этот <li>. Развёрнутая ветка
                добавляет строки помощников между этой стадией и следующей — коннектор бы просто проткнул их
                насквозь, поэтому рисуем его только пока ветка свёрнута (или её нет). */}
            {index < stages.length - 1 && !isExpandedBranch && <span className="stage-connector" aria-hidden="true" />}
          </li>
          {summary !== null && isExpandedBranch &&
            summary.roots.map((node) => (
              <AssistantTaskRow
                key={node.id}
                node={node}
                depth={1}
                selectedTaskId={selectedTaskId ?? null}
                expanded={expandedBranches}
                onToggleExpand={toggleBranch}
                onSelect={(taskId) => onSelectTask?.(stage.id, taskId)}
                allTasks={summary?.tasks ?? []}
              />
            ))}
          </Fragment>
          )
        })}
        </ul>
      </div>
      {version !== undefined && version.trim() !== '' && (
        <div className="rail-foot">afm {version}</div>
      )}
    </aside>
  )
}
