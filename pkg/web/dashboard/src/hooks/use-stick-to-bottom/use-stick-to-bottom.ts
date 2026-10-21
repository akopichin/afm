import { useCallback, useLayoutEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// ScrollPosition — где читатель оставил контейнер: прижат к низу или стоит на scrollTop.
export type ScrollPosition = { top: number; stick: boolean }

// ScrollMemory — внешнее хранилище позиции, чтобы контейнер вернулся на неё после перемонтирования.
export type ScrollMemory = {
  load: () => ScrollPosition | undefined
  save: (position: ScrollPosition) => void
}

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх. stick=true →
// MutationObserver докручивает вниз при росте контента. memory — необязательная память позиции: новый
// экземпляр открывается там, где читатель оставил прежний, а не внизу.
export function useStickToBottom<T extends HTMLElement>(memory?: ScrollMemory): {
  ref: (node: T | null) => void
  stick: boolean
  jumpToBottom: () => void
  release: () => void
} {
  const [node, setNode] = useState<T | null>(null)
  // Память читается один раз при монтировании; последующие объекты memory от ре-рендеров не важны.
  const memoryRef = useRef(memory)
  const restoredRef = useRef(memory?.load())
  const [stick, setStick] = useState(restoredRef.current?.stick ?? true)
  const stickRef = useRef(stick)
  stickRef.current = stick

  const jumpToBottom = useCallback(() => {
    if (node === null) return
    node.scrollTop = node.scrollHeight
    setStick(true)
    memoryRef.current?.save({ top: node.scrollTop, stick: true })
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

    // pendingTop — сохранённая позиция, которую ещё не удалось выставить: контент догружается после
    // монтирования, и до этого браузер прижмёт scrollTop к меньшему значению.
    let pendingTop = !stickRef.current && restoredRef.current !== undefined ? restoredRef.current.top : null
    const restore = () => {
      if (pendingTop === null || node.scrollHeight - node.clientHeight < pendingTop) return
      node.scrollTop = pendingTop
      pendingTop = null
    }

    // Узел мог примонтироваться уже с готовым контентом — без явного скролла здесь MutationObserver ниже
    // реагирует только на БУДУЩИЙ рост контента, и список остаётся наверху.
    if (stickRef.current) node.scrollTop = node.scrollHeight
    else restore()

    const onScroll = () => {
      pendingTop = null
      const near = node.scrollHeight - node.scrollTop - node.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
      memoryRef.current?.save({ top: node.scrollTop, stick: near })
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) node.scrollTop = node.scrollHeight
      else restore()
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
