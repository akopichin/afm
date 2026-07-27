import { useEffect, useRef, useState, type ReactElement } from 'react'
import type { Stage } from '../../types'
import { ATTENTION_STATUSES } from '../../hooks/use-attention'

type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
  agentSuggestEnabled: boolean
  onAddNote?: (stageId: string) => void // вызывается при клике на пункт меню
}

// Статусы, при которых у стадии доступен кебаб (agent_suggest): агент ещё
// выполняется (running) или ждёт одобрения плана (awaiting_approval) — оба
// случая, когда POST /api/stages/{id}/revise принимает поправку (см. Task 7).
const KEBAB_STATUSES: ReadonlySet<Stage['status']> = new Set(['running', 'awaiting_approval'])

// Левая панель: список стадий с выбором активной. На переходе стадии в done
// показываем one-shot анимацию точки (A1) и «пробегание» импульса по коннектору (D)
// — для этого запоминаем предыдущий статус каждой стадии и держим transient-набор
// just-done, который очищается через 700мс (чуть дольше 600мс-анимаций).
export function StagesList({ stages, selectedStageId, onSelect, agentSuggestEnabled, onAddNote }: StagesListProps): ReactElement {
  const prevStatus = useRef<Record<string, string>>({})
  const timers = useRef<Record<string, number>>({})
  const [justDone, setJustDone] = useState<Set<string>>(new Set())
  // Какая стадия сейчас показывает открытое кебаб-меню (option (a) из брифа —
  // маленькое выпадающее меню с одним пунктом, а не прямое открытие модалки:
  // пользователь описал двухуровневое взаимодействие меню→пункт, рассчитанное
  // на будущие пункты меню).
  const [openMenuStageId, setOpenMenuStageId] = useState<string | null>(null)

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
            {agentSuggestEnabled && KEBAB_STATUSES.has(stage.status) && (
              <span
                className="stage-kebab-wrap"
                onBlur={(e) => {
                  // Закрыть меню, когда фокус уходит за пределы обёртки (клик вне) —
                  // relatedTarget пуст при клике вне документа/скролле, тогда тоже закрываем.
                  if (!e.currentTarget.contains(e.relatedTarget)) setOpenMenuStageId(null)
                }}
              >
                <button
                  type="button"
                  className="stage-kebab"
                  aria-label="More actions"
                  onClick={(e) => {
                    e.stopPropagation() // не триггерить onSelect клика по строке
                    setOpenMenuStageId((current) => (current === stage.id ? null : stage.id))
                  }}
                >
                  ⋮
                </button>
                {openMenuStageId === stage.id && (
                  <ul className="stage-kebab-menu" onClick={(e) => e.stopPropagation()}>
                    <li>
                      <button
                        type="button"
                        onClick={() => {
                          setOpenMenuStageId(null)
                          onAddNote?.(stage.id)
                        }}
                      >
                        Добавить поправку агенту
                      </button>
                    </li>
                  </ul>
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
