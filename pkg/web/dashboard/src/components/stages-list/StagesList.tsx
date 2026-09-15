import { useEffect, useId, useRef, useState, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import { STAGE_STATUS_LABELS, type Stage } from '../../types'
import type { AccountingState, Coverage } from '../../types/cost'
import { ATTENTION_STATUSES } from '../../hooks/use-attention'

// Ширина меню — должна совпадать с min-width в .stage-kebab-menu (agent-note-modal.css),
// иначе right-выравнивание относительно кнопки съедет.
const KEBAB_MENU_WIDTH = 200

type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  onAddNote?: (stageId: string) => void // «Add note for agent» (revise, живой агент)
  onEditPreNote?: (stageId: string) => void // «Add note (before start)» (pending-стадия)
  onPause?: (stageId: string) => void
  // onButton — клик по предопределённой в flow.yaml кнопке стадии: передаём
  // только метку (= имя кнопки), сервер сам резолвит промпт и доставляет его
  // живому агенту через Revise (тот же путь, что и «Add note for agent»).
  onButton?: (stageId: string, name: string) => void
  // Прогресс прогона (доля done) — компактно в шапке рейла. Пришёл сюда из
  // удалённого футера (план §5.2). Оба undefined — прогресс не рендерится
  // (совместимость со старыми тестами StagesList без этих пропсов).
  progressDone?: number
  progressTotal?: number
  // accounting — состояние учёта затрат с бэкенда (GET /api/status). Нужно
  // railCost, чтобы решить, показывать ли плейсхолдер «…» для ещё не
  // оценённой (cost==null) активной стадии — сама по себе стадия не знает,
  // включён ли учёт затрат и жив ли прайсер.
  accounting: AccountingState
}

// Статусы, для которых имеет смысл плейсхолдер «оценка ожидается» — стадия
// реально гоняет агента ПРЯМО СЕЙЧАС (а не просто ждёт человека/бэкоффа).
const COST_PENDING_STATUSES: ReadonlySet<Stage['status']> = new Set(['planning', 'running', 'revising'])

// Доступный (a11y) хвост описания стоимости по покрытию прайс-листа: полное
// покрытие ничего не добавляет к «Estimated cost $X», частичное — уточняет,
// что часть вызовов не покрыта прайсом, отсутствие покрытия — заменяет всю
// фразу на «недоступно» (при coverage:'none' displayCost сам по себе '—').
function costA11yText(displayCost: string, coverage: Coverage): string {
  if (coverage === 'none') return 'Estimated cost unavailable'
  const suffix = coverage === 'partial' ? ', partial pricing coverage' : ''
  return `Estimated cost ${displayCost}${suffix}`
}

// railCost — единая точка принятия решения «что показать в рейле для этой
// стадии» (см. бриф increment2/task-10). Приоритет:
//   1. stage.cost есть → ВСЕГДА показываем displayCost (даже на ещё бегущей
//      стадии — запись уже посчитана и покрытие уже известно).
//   2. cost==null, но стадия реально гоняет агента прямо сейчас, не скрипт, и
//      бэкенд поддерживает учёт затрат и он жив (health:'ok') → плейсхолдер
//      «…» (оценка ожидается).
//   3. иначе — ничего (pending, скрипт, unsupported/unavailable — включая
//      unavailable с историческими данными, retrying).
function railCost(stage: Stage, accounting: AccountingState): { text: string; a11y: string } | null {
  if (stage.cost != null) {
    return { text: stage.cost.displayCost, a11y: costA11yText(stage.cost.displayCost, stage.cost.coverage) }
  }
  const canEstimate = !stage.isScript && accounting.supported && accounting.health === 'ok' && COST_PENDING_STATUSES.has(stage.status)
  if (!canEstimate) return null
  return { text: '…', a11y: 'Estimated cost pending' }
}

// Статусы, при которых у стадии доступен кебаб хоть с одним пунктом.
// Статусы, из которых можно поставить стадию на паузу вручную.
const PAUSABLE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'planning', 'revising', 'retrying'])

// "Add note for agent" (Revise) — только для running/awaiting_approval, и
// НИКОГДА для скриптовых стадий (see `!stage.isScript` в JSX): у скрипта нет
// агента, которому можно что-то сказать, а RunScript даже не принимает
// interrupt-канал, так что заметка на running-скрипте была бы no-op.
const ADD_NOTE_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval'])

// Pre-note (заметка ДО старта) доступна только пока стадия pending и только у
// стадий с агентом — у скрипта нет агента, которому вклеить заметку в контекст
// (симметрично !isScript-гейту "Add note for agent").
function canPreNote(stage: Stage): boolean {
  return stage.status === 'pending' && !stage.isScript
}

// "Add note for agent" (Revise) — только running/awaiting_approval и никогда на
// скрипте (у скрипта нет агента; RunScript не принимает interrupt-канал).
function canAddNote(stage: Stage): boolean {
  return ADD_NOTE_STATUSES.has(stage.status) && !stage.isScript
}

// Ручная пауза — из PAUSABLE_STATUSES, но не на running-скрипте (его нельзя
// прервать gracefully). Совпадает с гейтом пункта Pause в JSX.
function canPause(stage: Stage): boolean {
  return PAUSABLE_STATUSES.has(stage.status) && !(stage.isScript && stage.status === 'running')
}

// Кебаб показываем ТОЛЬКО если реально есть хоть один пункт меню — считаем из
// тех же предикатов, что и сами пункты (canAddNote/canPreNote/canPause, они же
// используются в JSX ниже). Иначе для стадии, где все пункты отфильтрованы
// (напр. running-скрипт: не add-note, не pre-note, не pause), рисовался пустой ⋮.
function hasKebab(stage: Stage): boolean {
  return canAddNote(stage) || canPreNote(stage) || canPause(stage)
}

// Левая панель: список стадий с выбором активной. На переходе стадии в done
// показываем one-shot анимацию точки (A1) и «пробегание» импульса по коннектору (D)
// — для этого запоминаем предыдущий статус каждой стадии и держим transient-набор
// just-done, который очищается через 700мс (чуть дольше 600мс-анимаций).
export function StagesList({ stages, selectedStageId, onSelect, onAddNote, onEditPreNote, onPause, onButton, progressDone, progressTotal, accounting }: StagesListProps): ReactElement {
  // useId() нельзя звать внутри stages.map (правила хуков запрещают хук в
  // цикле) — берём одну базу на компонент и добавляем к ней индекс строки,
  // чтобы id стоимостного спана оставался уникальным и стабильным для React.
  const costIdBase = useId()
  const prevStatus = useRef<Record<string, string>>({})
  const timers = useRef<Record<string, number>>({})
  const [justDone, setJustDone] = useState<Set<string>>(new Set())
  // Какая стадия сейчас показывает открытое кебаб-меню (option (a) из брифа —
  // маленькое выпадающее меню с одним пунктом, а не прямое открытие модалки:
  // пользователь описал двухуровневое взаимодействие меню→пункт, рассчитанное
  // на будущие пункты меню).
  const [openMenuStageId, setOpenMenuStageId] = useState<string | null>(null)
  // #stages-panel скроллится (overflow-y: auto, layout.css) — абсолютно
  // спозиционированное меню внутри него обрезалось бы краем панели, если
  // строка стадии оказывается ближе к низу видимой области (реальный баг,
  // замечен на живом флоу). Меню рендерится порталом в document.body с
  // координатами, посчитанными от кнопки — тем же приёмом, что и полноэкранный
  // Maximizable. menuPos живёт, пока меню открыто; закрывается кликом вне
  // (mousedown-слушатель на document) или скроллом (иначе меню зависает
  // визуально не на своём месте — перепозиционировать при скролле избыточно
  // для меню с одним пунктом).
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
    // Меню спозиционировано fixed относительно кнопки внутри #stages-panel.
    // Если прокручивается САМА панель — кнопка уезжает, а меню нет: закрываем.
    // Слушаем скролл ТОЛЬКО панели, а не window с capture:true — иначе любой
    // посторонний скролл в документе (например автоскролл ленты событий вниз
    // при новом событии, useStickToBottom дёргает scrollTop) ложно закрывал бы
    // меню сразу после открытия. Скролл не всплывает, поэтому чужие контейнеры
    // до этого слушателя не дойдут.
    const panel = document.getElementById('stages-panel')
    panel?.addEventListener('scroll', close)
    return () => {
      document.removeEventListener('mousedown', onMouseDown)
      panel?.removeEventListener('scroll', close)
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
    // Отдельный таймер на каждую стадию (в ref), чтобы повторный прогон эффекта
    // от обычного поллинга (новый массив stages каждые 3с) не отменял отложенную
    // очистку другой стадии — иначе just-done залипал бы навсегда.
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

  return (
    <aside id="stages-panel">
      <div className="rail-head">
        <h2>Stages</h2>
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
      <ul id="stages-list" className="stages-list">
        {stages.map((stage, index) => {
          // railCost — null означает «пустой рейл»: ни фигуры, ни описания
          // (см. документацию функции выше). costId существует, только пока
          // есть что описывать — иначе aria-describedby указывал бы в никуда.
          const cost = railCost(stage, accounting)
          const costId = cost !== null ? `${costIdBase}-cost-${index}` : undefined
          return (
          <li
            key={stage.id}
            className={`stage-item${stage.id === selectedStageId ? ' active' : ''}${justDone.has(stage.id) ? ' just-done' : ''}`}
            data-stage-id={stage.id}
            data-status={stage.status}
            data-attention={ATTENTION_STATUSES.has(stage.status) ? 'true' : undefined}
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
              <span className="status-dot" data-status={stage.status}>
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
            {stage.preNote !== '' && <span className="prenote-badge" title="Note attached for agent">📝</span>}
            {/* Стоимость — тихий моно-спан ПЕРЕД кебабом. Видимая цифра
                помечена aria-hidden (тон/ellipsis не несут собственного
                смысла для скринридера); полное предложение — в спрятанном
                узле, на который указывает aria-describedby строки. */}
            {cost !== null && (
              <span className="stage-cost">
                <span aria-hidden="true">{cost.text}</span>
                <span id={costId} className="stage-cost-sr">{cost.a11y}</span>
              </span>
            )}
            {hasKebab(stage) && (
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
                      {canAddNote(stage) && (
                        <li>
                          <button
                            type="button"
                            onClick={() => {
                              setOpenMenuStageId(null)
                              setMenuPos(null)
                              onAddNote?.(stage.id)
                            }}
                          >
                            Add note for agent
                          </button>
                        </li>
                      )}
                      {/* Кастомные кнопки стадии (flow.yaml). Тот же гейт, что и
                          «Add note for agent» (running/awaiting_approval, не
                          скрипт) — под ним и живёт живой агент, которому Revise
                          доставит промпт. Сабблок с разделителем показывается,
                          только если кнопки реально объявлены. */}
                      {canAddNote(stage) && stage.buttons.length > 0 && (
                        <>
                          <li className="stage-kebab-divider" role="separator" aria-hidden="true" />
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
                    </ul>,
                    document.body,
                  )}
              </span>
            )}
            </span>
            {index < stages.length - 1 && <span className="stage-connector" aria-hidden="true" />}
          </li>
          )
        })}
      </ul>
    </aside>
  )
}
