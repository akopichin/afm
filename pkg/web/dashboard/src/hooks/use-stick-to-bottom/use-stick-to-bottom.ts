import { useCallback, useLayoutEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх. stick=true →
// MutationObserver докручивает вниз при росте контента.
export function useStickToBottom<T extends HTMLElement>(): {
  ref: (node: T | null) => void
  stick: boolean
  jumpToBottom: () => void
  release: () => void
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

  // release — императивно отпускает «прилипание» к низу. Пишем и stickRef.current сразу (не только
  // setStick), чтобы наблюдатель перестал докручивать в ТОМ ЖЕ кадре, до асинхронного ре-рендера.
  const release = useCallback(() => {
    stickRef.current = false
    setStick(false)
  }, [])

  // useLayoutEffect: начальную позицию скролла выставляем ДО отрисовки кадра. Наблюдатели ниже — тоже
  // здесь, разницы нет.
  useLayoutEffect(() => {
    if (node === null) return

    // Узел мог примонтироваться уже с готовым контентом — без явного скролла здесь MutationObserver ниже
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

  return { ref: setNode, stick, jumpToBottom, release }
}
