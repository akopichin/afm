import type { ReactElement } from 'react'

// Совпадает с use-status.ts's FlowStatus['flowPauseState'] (см. её комментарий
// там же для семантики 'none'/'paused'/'resuming') — тип продублирован
// намеренно, тем же приёмом, что и FileViewer.tsx's FlowPauseState: баннер не
// должен тянуть весь модуль use-status ради одного литерала.
export type FlowPauseState = 'none' | 'paused' | 'resuming'

type ReviewBannerProps = {
  flowPauseState: FlowPauseState
  // Число собранных ревью-заметок прямо сейчас (см. Task 19's listNotes()) —
  // показывается в тексте баннера только в состоянии 'paused'. App.tsx решает,
  // как его получить (см. её комментарий про polling listNotes()); баннер сам
  // ничего не запрашивает — чистый презентационный компонент.
  noteCount: number
  // Открывает шаг доставки заметок (Task 23 — модалка выбора стадии +
  // injectNotes). Пока не рендерится в paused, кнопка недоступна.
  onSend: () => void
  // Отменяет раунд ревью без доставки (Task 19's cancelNotes()) — заметки
  // остаются в сторе для следующего раунда.
  onCancel: () => void
}

// Полноширинный баннер флоу-wide review-паузы (Task 22): монтируется в App.tsx
// сразу под шапкой, поверх основного контента. 'none' — обычная работа, баннер
// не рендерится вообще (return null, а не пустой div — иначе он бы отъедал
// место сеткой layout'а). Формулировка в 'paused' — намеренно честная: пауза
// активных агентов best-effort (см. AGENTS.md "Manual pause reuses
// interruptChans" — SIGINT + grace period, не гарантированная мгновенная
// остановка), а не гарантия, что вся работа прямо сейчас стоит.
export function ReviewBanner({ flowPauseState, noteCount, onSend, onCancel }: ReviewBannerProps): ReactElement | null {
  if (flowPauseState === 'none') return null

  if (flowPauseState === 'resuming') {
    return (
      <div className="review-banner" data-state="resuming" role="status">
        <span className="review-banner-text">Resuming…</span>
      </div>
    )
  }

  const noteWord = noteCount === 1 ? 'note' : 'notes'

  return (
    <div className="review-banner" data-state="paused" role="status">
      <span className="review-banner-text">
        ⏸ Review mode — new stages held; active work pausing best-effort ({noteCount} {noteWord})
      </span>
      <div className="review-banner-actions">
        <button type="button" className="btn btn-send" onClick={onSend}>
          Send notes
        </button>
        <button type="button" className="btn btn-cancel" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  )
}
