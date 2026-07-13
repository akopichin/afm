import { useEffect } from 'react'

const FLASH_TITLE = '⚠ Нужно действие — afm Dashboard'
const FLASH_INTERVAL_MS = 1500

// Мигает document.title когда вкладка в фоне и active=true (стадия ждёт юзера).
// При возврате на вкладку (visibilitychange) — восстанавливает исходный title.
export function useTitleFlash(active: boolean): void {
  useEffect(() => {
    if (!active) return
    const original = document.title
    let toggle = false
    let timer: ReturnType<typeof setInterval> | undefined

    const stop = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
      document.title = original
    }
    const onVisibility = () => {
      if (document.hidden) {
        timer = setInterval(() => {
          toggle = !toggle
          document.title = toggle ? FLASH_TITLE : original
        }, FLASH_INTERVAL_MS)
      } else {
        stop()
      }
    }

    document.addEventListener('visibilitychange', onVisibility)
    if (document.hidden) onVisibility()
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [active])
}
