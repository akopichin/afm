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

// Максимизация БЕЗ портала: один и тот же <div> всегда монтирован на своей исходной позиции в дереве —
// meняется только className.
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
