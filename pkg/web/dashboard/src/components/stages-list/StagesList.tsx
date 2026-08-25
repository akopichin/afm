import { useEffect, useRef, useState, type ReactElement } from 'react'
import { createPortal } from 'react-dom'
import type { Stage } from '../../types'
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
}

// Статусы, при которых у стадии доступен кебаб хоть с одним пунктом.
// "Add note for agent" остаётся ограничен running/awaiting_approval (см.
// ниже, отдельное условие на сам пункт) — "Pause" доступен на остальных.
const KEBAB_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval', 'planning', 'revising', 'retrying'])

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

// Кебаб показываем, если у стадии есть хоть один пункт: обычные действия
// (KEBAB_STATUSES) ИЛИ pre-note на pending-стадии. Без этого добавление
// pending в KEBAB_STATUSES показывало бы пустое меню на pending-скрипте.
function hasKebab(stage: Stage): boolean {
  return KEBAB_STATUSES.has(stage.status) || canPreNote(stage)
}

// Левая панель: список стадий с выбором активной. На переходе стадии в done
// показываем one-shot анимацию точки (A1) и «пробегание» импульса по коннектору (D)
// — для этого запоминаем предыдущий статус каждой стадии и держим transient-набор
// just-done, который очищается через 700мс (чуть дольше 600мс-анимаций).
export function StagesList({ stages, selectedStageId, onSelect, onAddNote, onEditPreNote, onPause }: StagesListProps): ReactElement {
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
    // capture:true — скролл не всплывает у скроллящихся контейнеров, но
    // capture-фаза проходит через них независимо от bubbles.
    window.addEventListener('scroll', close, true)
    return () => {
      document.removeEventListener('mousedown', onMouseDown)
      window.removeEventListener('scroll', close, true)
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
      <h2>Stages</h2>
      <ul id="stages-list" className="stages-list">
        {stages.map((stage, index) => (
          <li
            key={stage.id}
            className={`stage-item${stage.id === selectedStageId ? ' active' : ''}${justDone.has(stage.id) ? ' just-done' : ''}`}
            data-stage-id={stage.id}
            data-status={stage.status}
            data-attention={ATTENTION_STATUSES.has(stage.status) ? 'true' : undefined}
            onClick={() => onSelect(stage.id)}
          >
            <span className="status-dot" data-status={stage.status}>
              <span className="dot-check" aria-hidden="true">✓</span>
            </span>
            <span className="stage-label">
              <span className="stage-id">{stage.id}</span>
              {stage.name !== '' && <span className="stage-name">{stage.name}</span>}
            </span>
            {stage.status === 'awaiting_user_input' && <span className="dialog-badge" title="Awaiting your reply">💬</span>}
            {stage.status === 'awaiting_approval' && <span className="approval-badge" title="Awaiting plan approval">📋</span>}
            {stage.preNote !== '' && <span className="prenote-badge" title="Note attached for agent">📝</span>}
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
                      {ADD_NOTE_STATUSES.has(stage.status) && !stage.isScript && (
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
                      {PAUSABLE_STATUSES.has(stage.status) && !(stage.isScript && stage.status === 'running') && (
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
            {index < stages.length - 1 && <span className="stage-connector" aria-hidden="true" />}
          </li>
        ))}
      </ul>
    </aside>
  )
}
