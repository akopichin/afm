import { useCallback, useState } from 'react'

export type FeedScope = 'stage' | 'all'

const STORAGE_KEY = 'afm-feed-scope'

function readInitialScope(): FeedScope {
  return window.localStorage.getItem(STORAGE_KEY) === 'all' ? 'all' : 'stage'
}

// Область ленты: 'stage' (события только выбранной стадии + flow-level) или 'all'
// (все события флоу). Глобальный, как и useFeedMode: переживает смену выбранной
// стадии и reload страницы, пока пользователь не переключит сам. Дефолт 'stage' —
// лента по умолчанию сфокусирована на текущей стадии.
export function useFeedScope(): { scope: FeedScope; toggle: () => void } {
  const [scope, setScope] = useState<FeedScope>(readInitialScope)

  const toggle = useCallback(() => {
    setScope((prev) => {
      const next: FeedScope = prev === 'stage' ? 'all' : 'stage'
      window.localStorage.setItem(STORAGE_KEY, next)
      return next
    })
  }, [])

  return { scope, toggle }
}
