import { useEffect } from 'react'
import { compositeAttentionBadge } from './composite-attention-badge'

const PULSE_INTERVAL_MS = 700

// Пульсирует favicon амберным бейджем, пока вкладка в фоне и active=true — тот же паттерн, что
// useTitleFlash использует для document.title, но для <link rel="icon">.href.
export function useFaviconPulse(active: boolean): void {
  useEffect(() => {
    if (!active) return
    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (link === null) return

    const original = link.href
    let toggle = false
    let timer: ReturnType<typeof setInterval> | undefined
    let cancelled = false

    const stop = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
      link.href = original
    }

    const onVisibility = () => {
      if (!document.hidden) {
        stop()
        return
      }
      void compositeAttentionBadge(original)
        .then((badgeHref) => {
          // Не запускаем второй interval после общего async-промиса: иначе ссылка на первый таймер
          // потеряется.
          if (cancelled || !document.hidden || timer !== undefined) return
          timer = setInterval(() => {
            toggle = !toggle
            link.href = toggle ? badgeHref : original
          }, PULSE_INTERVAL_MS)
        })
        .catch(() => {
          // Не удалось построить бейдж (ошибка загрузки иконки в canvas) —
          // просто не пульсируем, без падения остального UI.
        })
    }

    document.addEventListener('visibilitychange', onVisibility)
    if (document.hidden) onVisibility()

    return () => {
      cancelled = true
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [active])
}
