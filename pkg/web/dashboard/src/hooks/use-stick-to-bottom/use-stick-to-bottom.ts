import { useCallback, useEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх.
// stick=true → MutationObserver докручивает вниз при росте контента.
// stick=false → не трогаем скролл; jumpToBottom() возвращается к хвосту.
//
// ref — callback (не RefObject): некоторые контейнеры (DialogChannel/#dialog-scroll)
// монтируются не сразу, а только когда появляется контент (hasContent). Callback
// ref гарантирует, что эффект ниже переустановит наблюдатели именно в момент
// реального появления DOM-узла, а не только один раз при первом рендере хука.
export function useStickToBottom<T extends HTMLElement>(): {
  ref: (node: T | null) => void
  stick: boolean
  jumpToBottom: () => void
} {
  const [node, setNode] = useState<T | null>(null)
  const [stick, setStick] = useState(true)
  const stickRef = useRef(true)
  stickRef.current = stick

  const jumpToBottom = useCallback(() => {
    if (node === null) return
    node.scrollTop = node.scrollHeight
    setStick(true)
  }, [node])

  useEffect(() => {
    if (node === null) return

    // Узел мог примонтироваться уже с готовым контентом (та самая задержка
    // DialogChannel выше) — без явного скролла здесь MutationObserver ниже
    // реагирует только на БУДУЩИЙ рост контента, и список остаётся наверху.
    if (stickRef.current) node.scrollTop = node.scrollHeight

    const onScroll = () => {
      const near = node.scrollHeight - node.scrollTop - node.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) node.scrollTop = node.scrollHeight
    })

    node.addEventListener('scroll', onScroll, { passive: true })
    obs.observe(node, { childList: true, subtree: true, characterData: true })
    return () => {
      node.removeEventListener('scroll', onScroll)
      obs.disconnect()
    }
  }, [node])

  return { ref: setNode, stick, jumpToBottom }
}
