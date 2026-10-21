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

    // lastTop — где читатель оставил отпущенный контейнер. У скрытого (display: none) нет раскладки и
    // scrollTop = 0, поэтому при новом показе позицию возвращаем сами (не дальше текущего максимума).
    let lastTop = node.scrollTop
    let hidden = node.clientHeight === 0
    const onScroll = () => {
      if (node.clientHeight === 0) return
      lastTop = node.scrollTop
      const near = node.scrollHeight - node.scrollTop - node.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) node.scrollTop = node.scrollHeight
    })
    // Видимый прижатый контейнер держим у низа при любом изменении размера (например, выросло поле ввода);
    // отпущенный возвращаем на lastTop только при переходе «скрыт → виден».
    const resize = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => {
      const wasHidden = hidden
      hidden = node.clientHeight === 0
      if (hidden) return
      if (stickRef.current) node.scrollTop = node.scrollHeight
      else if (wasHidden) node.scrollTop = Math.min(lastTop, node.scrollHeight - node.clientHeight)
    })

    node.addEventListener('scroll', onScroll, { passive: true })
    obs.observe(node, { childList: true, subtree: true, characterData: true })
    resize?.observe(node)
    return () => {
      node.removeEventListener('scroll', onScroll)
      obs.disconnect()
      resize?.disconnect()
    }
  }, [node])

  return { ref: setNode, stick, jumpToBottom, release }
}
