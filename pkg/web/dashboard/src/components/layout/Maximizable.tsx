import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

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

// Максимизация через портал: инстанс компонента сохраняется (состояние не теряется).
export function Maximizable({ id, children }: { id: string; children: ReactNode }) {
  const { maximizedKey } = useMaximize()
  if (maximizedKey !== id) return <>{children}</>
  return createPortal(
    <div className="maximize-overlay" role="dialog" aria-modal="true">{children}</div>,
    document.body,
  )
}
