import { useCallback, useEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх.
// stick=true → MutationObserver докручивает вниз при росте контента.
// stick=false → не трогаем скролл; jumpToBottom() возвращается к хвосту.
export function useStickToBottom<T extends HTMLElement>(): {
  ref: React.RefObject<T>
  stick: boolean
  jumpToBottom: () => void
} {
  const ref = useRef<T>(null)
  const [stick, setStick] = useState(true)
  const stickRef = useRef(true)
  stickRef.current = stick

  const jumpToBottom = useCallback(() => {
    const el = ref.current
    if (el === null) return
    el.scrollTop = el.scrollHeight
    setStick(true)
  }, [])

  useEffect(() => {
    const el = ref.current
    if (el === null) return

    const onScroll = () => {
      const near = el.scrollHeight - el.scrollTop - el.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) el.scrollTop = el.scrollHeight
    })

    el.addEventListener('scroll', onScroll, { passive: true })
    obs.observe(el, { childList: true, subtree: true, characterData: true })
    return () => {
      el.removeEventListener('scroll', onScroll)
      obs.disconnect()
    }
  }, [])

  return { ref, stick, jumpToBottom }
}
