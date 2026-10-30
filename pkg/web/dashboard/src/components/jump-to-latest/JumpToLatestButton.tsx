import type { ReactElement } from 'react'

// Плавающая круглая иконка-кнопка «вниз, к последнему» — единый вид для ленты, лога и диалога. onClick
// прокручивает контейнер к низу; unseen — сколько новых событий пришло ниже видимой части.
export function JumpToLatestButton({ onClick, raised = false, unseen = 0 }: { onClick: () => void; raised?: boolean; unseen?: number }): ReactElement {
  const label = unseen > 0 ? `Jump to latest, ${unseen} new` : 'Jump to latest'
  return (
    <button
      type="button"
      className={`jump-latest${raised ? ' jump-latest-raised' : ''}`}
      onClick={onClick}
      aria-label={label}
      title={label}
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M12 5 v14 M5 12 l7 7 l7 -7" />
      </svg>
      {unseen > 0 && <span className="jump-latest-count" aria-hidden="true">{unseen > 99 ? '99+' : unseen}</span>}
    </button>
  )
}
