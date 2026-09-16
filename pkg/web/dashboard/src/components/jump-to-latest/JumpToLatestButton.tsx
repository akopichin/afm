import type { ReactElement } from 'react'

// Плавающая круглая иконка-кнопка «вниз, к последнему» — единый вид для ленты,
// лога и диалога (раньше в каждом месте была текстовая плашка «↓ Jump to latest»).
// Класс .jump-latest (base/layout.css) делает её floating-иконкой; здесь — только
// разметка + иконка + доступное имя. onClick прокручивает контейнер к низу.
//
// raised — приподнять кнопку выше низа контейнера: в диалоге снизу залипает
// action-бар (SEND/CANCEL/«AGENT IS WAITING», .dialog-actions sticky bottom:0),
// и обычный bottom:10px посадил бы иконку прямо на него. В ленте/логе такого
// бара нет — там raised не нужен.
export function JumpToLatestButton({ onClick, raised = false }: { onClick: () => void; raised?: boolean }): ReactElement {
  return (
    <button
      type="button"
      className={`jump-latest${raised ? ' jump-latest-raised' : ''}`}
      onClick={onClick}
      aria-label="Jump to latest"
      title="Jump to latest"
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M12 5 v14 M5 12 l7 7 l7 -7" />
      </svg>
    </button>
  )
}
