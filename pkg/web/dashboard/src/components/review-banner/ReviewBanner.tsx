import type { ReactElement } from 'react'

// Локальный FlowPauseState совпадает с use-status, сохраняя независимость баннера от хука.
export type FlowPauseState = 'none' | 'paused' | 'resuming'

type ReviewBannerProps = {
  flowPauseState: FlowPauseState
  // Число собранных ревью-заметок прямо сейчас — показывается в тексте баннера только в состоянии
  // 'paused'.
  noteCount: number
  // Открывает выбор стадии для injectNotes; доступно только при paused.
  onSend: () => void
  // Отменяет раунд ревью без доставки заметок.
  onCancel: () => void
}

// Полноширинный баннер флоу-wide review-паузы: монтируется в App.tsx сразу под шапкой, поверх основного
// контента.
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
