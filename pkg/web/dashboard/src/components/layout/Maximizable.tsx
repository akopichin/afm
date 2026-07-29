import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

type MaximizeState = { maximizedKey: string | null; toggle: (key: string) => void }
const MaximizeContext = createContext<MaximizeState>({ maximizedKey: null, toggle: () => {} })

export function MaximizeProvider({ children }: { children: ReactNode }) {
  const [maximizedKey, setMaximizedKey] = useState<string | null>(null)
  const toggle = useCallback((key: string) => {
    setMaximizedKey((cur) => (cur === key ? null : key))
  }, [])
  useEffect(() => {
    if (maximizedKey === null) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMaximizedKey(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [maximizedKey])
  return <MaximizeContext.Provider value={{ maximizedKey, toggle }}>{children}</MaximizeContext.Provider>
}

export function useMaximize(): MaximizeState {
  return useContext(MaximizeContext)
}

// Максимизация БЕЗ портала: один и тот же <div> всегда монтирован на своей
// исходной позиции в дереве — meняется только className (просто CSS
// position:fixed на maximize-overlay даёт полноэкранный вид, см. layout.css).
// Раньше здесь был createPortal (сначала Fragment↔Portal, потом
// anchor↔document.body) — в обоих случаях React видел смену идентичности
// узла на этой позиции и размонтировал детей при каждом toggle, сбрасывая их
// состояние (баг: стик-скролл ленты событий и видимость кнопки «↓ latest»
// терялись при maximize/restore). Без портала контейнер вообще не меняется —
// React только обновляет className, дети остаются смонтированными.
export function Maximizable({ id, children }: { id: string; children: ReactNode }) {
  const { maximizedKey } = useMaximize()
  const maximized = maximizedKey === id

  return (
    <div
      className={`maximizable-frame${maximized ? ' maximize-overlay' : ''}`}
      role={maximized ? 'dialog' : undefined}
      aria-modal={maximized ? true : undefined}
    >
      {children}
    </div>
  )
}
