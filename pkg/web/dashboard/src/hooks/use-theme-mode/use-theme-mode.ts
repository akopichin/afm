import { useCallback, useState } from 'react'

export type ThemeMode = 'dark' | 'light'

const STORAGE_KEY = 'afm-mode'

function readInitialMode(): ThemeMode {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

// Переключатель dark/light, независимый от активного скина (novacorps/goga/custom).
// Начальное значение уже выставлено инлайн-скриптом в index.html (до отрисовки
// CSS, без FOUC) — хук лишь читает его из data-theme и даёт toggle() для UI.
export function useThemeMode(): { mode: ThemeMode; toggle: () => void } {
  const [mode, setMode] = useState<ThemeMode>(readInitialMode)

  const toggle = useCallback(() => {
    setMode((prev) => {
      const next: ThemeMode = prev === 'dark' ? 'light' : 'dark'
      document.documentElement.dataset.theme = next
      window.localStorage.setItem(STORAGE_KEY, next)
      return next
    })
  }, [])

  return { mode, toggle }
}
