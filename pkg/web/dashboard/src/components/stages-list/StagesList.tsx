import { Fragment, useEffect, useId, useRef, useState, type ReactElement } from 'react'
import { STAGE_STATUS_LABELS, type Stage, type StageVerify } from '../../types'
import type { AccountingState, Coverage } from '../../types/cost'
import { attentionKindFor } from '../../hooks/use-attention'
import { formatStageDuration, type StageTimes } from '../../lib/stage-timing'
import type { StageAssistantsSummary } from '../../hooks/use-stage-assistants'
import { AssistantTaskRow } from '../assistants'
import { QuotaCountdown } from './QuotaCountdown'

type StagesListProps = {
  stages: Stage[]
  times?: StageTimes
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  // canAddStage — capabilities.add_stage; показывает кнопку «Add stage» внизу рейла. onAddStage открывает
  // диалог добавления динамической стадии (App владеет модалкой).
  canAddStage?: boolean
  onAddStage?: () => void
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
  // statusAvailable — свежесть /api/status из useStatus: пока он устарел, счётчик ожидания лимита замирает.
  statusAvailable?: boolean
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

// isStaleRunningVerify — defensive gate: the pulsing "running" verify badge is only true while the stage
// itself is running. A verify pass that is interrupted (terminal stage, usage-limit wait, pause, retry)
// leaves `running` in the durable verify state until the next verify event, so on any other status the badge
// is stale; hide it rather than pulse amber beside a done/parked dot.
function isStaleRunningVerify(status: Stage['status'], phase: StageVerify['phase']): boolean {
  return status !== 'running' && phase === 'running'
}

// progressState — цвет сегмента полоски прогресса: готово / идёт сейчас / остальное.
function progressState(status: Stage['status']): 'done' | 'active' | '' {
  if (status === 'done') return 'done'
  return status === 'running' || status === 'planning' || status === 'revising' || status === 'retrying' ? 'active' : ''
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

// Прикрепить помощника можно к любой не-скрипт стадии, которая ещё не достигла терминального статуса
// (сервер отвергает done/failed и script — см. orchestrator.Attach). Кнопка живёт в шапке стадии.
export function canAttachHelper(stage: Stage, collaborationEnabled: boolean): boolean {
  return collaborationEnabled && !stage.isScript && stage.status !== 'done' && stage.status !== 'failed'
}

// Левая панель: список стадий с выбором активной.
export function StagesList({
  stages, times, selectedStageId, onSelect, progressDone, progressTotal, accounting,
  version, assistants, selectedTaskId, onSelectTask, canAddStage = false, onAddStage, statusAvailable = true,
}: StagesListProps): ReactElement {
  // useId нельзя звать внутри stages.map — берём одну базу на компонент и добавляем к ней индекс строки,
  // чтобы id стоимостного спана оставался уникальным и стабильным для React.
  const costIdBase = useId()
  const prevStatus = useRef<Record<string, string>>({})
  const timers = useRef<Record<string, number>>({})
  const [justDone, setJustDone] = useState<Set<string>>(new Set())
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
        {progressTotal !== undefined && progressTotal > 0 && (
          <span id="progress-text" className="rail-progress-text">{progressDone ?? 0} of {progressTotal} done</span>
        )}
      </div>
      {progressTotal !== undefined && progressTotal > 0 && (
        <div className="rail-progress" role="img" aria-label={`${progressDone ?? 0} of ${progressTotal} stages done`}>
          {stages.map((stage) => (
            <span key={stage.id} className="rail-progress-seg" data-state={progressState(stage.status)} />
          ))}
        </div>
      )}
      <div className="stages-scroll">
        <ul id="stages-list" className="stages-list">
        {stages.map((stage, index) => {
          // railCost — null означает «пустой рейл»: ни фигуры, ни описания. costId существует, только пока
          // есть что описывать — иначе aria-describedby указывал бы в никуда.
          const time = formatStageDuration(times?.byStage.get(stage.id)?.timeSec ?? null)
          const timeId = `${costIdBase}-time-${index}`
          const timeDescription = times?.incompleteIds.has(stage.id) ? 'Stage time unavailable: incomplete timing' : `Stage time ${time}`
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
                коробки): рейл — основной способ открыть стадию, поэтому он
                обязан быть доступен с клавиатуры. Бейджи и переключатель helper-ветки —
                соседи в .stage-actions: вложенные интерактивные элементы недопустимы. */}
            <button
              type="button"
              className="stage-row"
              onClick={() => onSelect(stage.id)}
              aria-describedby={[timeId, costId].filter(Boolean).join(' ')}
            >
              <span
                className="status-dot"
                data-status={stage.status}
                data-blocked={awaitingAssistantsBlocked ? 'true' : undefined}
              >
                <span className="dot-check" aria-hidden="true">
                  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M3.5 8.5 6.7 11.6 12.5 4.6" />
                  </svg>
                </span>
                <span className="dot-num" aria-hidden="true">{index + 1}</span>
              </span>
              {/* Две строки: имя стадии жирным (id — только если имени нет) и слово статуса под ним; статус
                  читается и скринридером из accessible name кнопки. */}
              <span className="stage-label">
                <span className="stage-name">{stage.name !== '' ? stage.name : stage.id}</span>
                <span className="stage-status-line">
                  <span className="stage-status" data-status={stage.status}>{STAGE_STATUS_LABELS[stage.status]}</span>
                  <span className="stage-time" aria-hidden="true">{time}</span>
                </span>
              </span>
            </button>
            {/* Единый трейлинг-слот: бейджи и переключатель ветки. Отдельная от .stage-row
                ячейка, чтобы индикаторы не меняли accessible name кнопки строки. */}
            <span id={timeId} className="stage-time-sr">{timeDescription}</span>
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
            {stage.status === 'awaiting_quota' && stage.quota !== undefined && (
              <QuotaCountdown quota={stage.quota} live={statusAvailable} compact />
            )}
            {awaitingAssistantsBlocked && (
              <span className="assistants-blocked-badge" title="A required helper task needs your decision">⚠</span>
            )}
            {stage.preNote !== '' && (
              <span className="prenote-badge" role="img" aria-label="Stage note attached" title="Stage note attached · select stage to read">📝</span>
            )}
            {stage.verify !== undefined && !isStaleRunningVerify(stage.status, stage.verify.phase) && (
              <span
                className="verify-badge"
                data-phase={stage.verify.phase}
                title={`AI verify step ${stage.verify.step} · ${stage.verify.command}: ${verifyPhaseLabel(stage.verify.phase)}`}
              >
                🔎
              </span>
            )}
            {/* Стоимость — тихий моно-спан. Видимая цифра
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
            </span>
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
      <div className="rail-bottom">
        {canAddStage && (
          <button type="button" className="rail-add-stage" onClick={() => onAddStage?.()} title="Add a dynamic stage">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
              <path d="M12 5v14M5 12h14" />
            </svg>
            Add stage
          </button>
        )}
        {version !== undefined && version.trim() !== '' && <div className="rail-foot">afm {version}</div>}
      </div>
    </aside>
  )
}
