import { useCallback, useState } from 'react'

export type FeedMode = 'feed' | 'log'

const STORAGE_KEY = 'afm-feed-mode'

function readInitialMode(): FeedMode {
  return window.localStorage.getItem(STORAGE_KEY) === 'log' ? 'log' : 'feed'
}

// Режим правой панели: 'feed' (лента событий флоу) или 'log' (лог выбранной
// стадии). Глобальный, не привязан к конкретной стадии — переживает и смену
// выбранной стадии, и reload страницы, пока пользователь не переключит сам.
export function useFeedMode(): { mode: FeedMode; toggle: () => void } {
  const [mode, setMode] = useState<FeedMode>(readInitialMode)

  const toggle = useCallback(() => {
    setMode((prev) => {
      const next: FeedMode = prev === 'feed' ? 'log' : 'feed'
      window.localStorage.setItem(STORAGE_KEY, next)
      return next
    })
  }, [])

  return { mode, toggle }
}
