import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Virtuoso, type Components, type VirtuosoHandle } from 'react-virtuoso'
import { headShift, rowSignature, type VirtualFeedRow } from './feed-virtual-model'
import { isNearBottom, type FollowEvent } from './follow-policy'
import { readingElement } from '../../hooks/use-feed-focus/reading-anchor'

// Список строк ленты на react-virtuoso. Единственный владелец скролла: scrollTop напрямую никто не пишет, все
// перемещения идут через scrollToIndex списка с поколением запроса, остаток правит scrollBy.

// База firstItemIndex: запас вниз, чтобы индекс не стал отрицательным при вытеснении с головы.
const BASE_INDEX = 1_000_000
const VIEWPORT_MARGIN_PX = 400
const GESTURE_WINDOW_MS = 500
const SETTLE_DELAYS_MS = [150, 1300]
const RESIDUAL_TOLERANCE_PX = 2
// Строка, видимая у верхнего края меньше этого, — не то, что читают: якорем её не берём.
const MIN_ANCHOR_VISIBLE_PX = 8
const WATCH_BOTTOM_MS = 2000
const UP_KEYS = new Set(['ArrowUp', 'PageUp', 'Home'])
const DOWN_KEYS = new Set(['ArrowDown', 'PageDown', 'End'])
// Пробел на кнопке раскрывает серию, а не прокручивает: жестом он считается только на самой ленте.
const INTERACTIVE = 'button, a, input, textarea, select, summary, [contenteditable]'

// Anchor — строка, которую читатель держит на месте: её ключ, элемент и положение относительно верха скроллера.
type Anchor = { key: string; element: HTMLElement; top: number; rowOffset: number }

export type ListStart = { key: string; align: 'start' | 'center'; offset?: number }

export type FeedListHandle = {
  scrollToLatest: () => void
  scrollToRow: (key: string, align: 'start' | 'center') => void
  // anchor — строка первой реально видимой части и положение её верха: они переносятся при смене фильтра.
  anchor: () => { key: string; offset: number } | null
  // hold — удержать строку на месте при ближайшей смене состава (раскрытие и свёртывание серии).
  hold: (key: string) => void
  // watchBottom — после свёртывания следит за кадрами и возобновляет follow, когда список после раскладки встал к низу.
  watchBottom: () => void
}

type ListContext = { footer: ReactNode }

const List: Components<VirtualFeedRow, ListContext>['List'] = forwardRef(function FeedList({ children, style, context: _context, ...rest }, ref) {
  return <div {...rest} ref={ref} style={style} role="list" aria-label="Feed events">{children}</div>
})

const Item: Components<VirtualFeedRow, ListContext>['Item'] = function FeedListItem({ children, item: _item, context: _context, ...rest }) {
  return <div {...rest} role="listitem">{children}</div>
}

const Header: Components<VirtualFeedRow, ListContext>['Header'] = function FeedListHeader() {
  return <div className="feed-scroll-header" aria-hidden="true" />
}

const Footer: Components<VirtualFeedRow, ListContext>['Footer'] = function FeedListFooter({ context }) {
  return <div className="feed-scroll-footer">{context.footer}</div>
}

const COMPONENTS: Components<VirtualFeedRow, ListContext> = { List, Item, Header, Footer }

type VirtualFeedListProps = {
  rows: VirtualFeedRow[]
  renderRow: (row: VirtualFeedRow, isFirst: boolean) => ReactNode
  following: boolean
  onFollow: (event: FollowEvent) => void
  // start — где открыть список при монтировании (смена фильтра, переход к цитате); без него — хвост.
  start: ListStart | null
  // footer — то, что стоит под последней строкой (WorkingLine); footerKey меняется, когда он появляется или меняется.
  footer: ReactNode
  footerKey: string
  // overlay — то, что лежит поверх списка в его рамке (Jump to latest, замечание о переходе).
  overlay: ReactNode
}

// rowElementOf — строка списка, в которой лежит элемент.
const rowElementOf = (element: Element): HTMLElement | null => element.closest<HTMLElement>('[data-virtual-row-key]')

export const VirtualFeedList = forwardRef<FeedListHandle, VirtualFeedListProps>(function VirtualFeedList({ rows, renderRow, following, onFollow, start, footer, footerKey, overlay }, ref) {
  const virtuoso = useRef<VirtuosoHandle>(null)
  const frame = useRef<HTMLDivElement>(null)
  const scroller = useRef<HTMLElement | null>(null)
  const keys = useMemo(() => rows.map((row) => row.key), [rows])
  const tail = rows.length === 0 ? '' : rowSignature(rows[rows.length - 1] as VirtualFeedRow)

  // Голова: вытеснение строк с начала — это сдвиг firstItemIndex, остальное решает якорь.
  const [head, setHead] = useState({ base: BASE_INDEX, keys })
  let firstItemIndex = head.base
  if (head.keys !== keys) {
    firstItemIndex = head.base + (headShift(head.keys, keys) ?? 0)
    setHead({ base: firstItemIndex, keys })
  }

  const [initial] = useState(() => {
    const index = start === null ? -1 : keys.indexOf(start.key)
    return start !== null && index >= 0 ? { index, align: start.align, offset: start.offset } : { index: 'LAST' as const, align: 'end' as const }
  })
  // Фильтр открыл список, а строки читателя в нём нет: он открылся на хвосте.
  const [startMissing] = useState(() => start !== null && !keys.includes(start.key))

  const latest = useRef({ keys, following, onFollow })
  const anchors = useRef<{ first: Anchor | null; reading: Anchor | null }>({ first: null, reading: null })
  // held — строка, которую читатель только что раскрыл или свернул: её место важнее первой видимой строки.
  const held = useRef<Anchor | null>(null)
  const generation = useRef(0)
  // Жест и программный запрос прокрутки нумеруются общим счётчиком: scroll-событие принадлежит жесту, только если он новее
  // последнего запроса ленты (иначе это отклик на наш scrollToIndex/scrollBy).
  const order = useRef(0)
  const inputOrder = useRef(0)
  const requestOrder = useRef(0)
  const inputAt = useRef(-Infinity)
  const lastTop = useRef(0)
  const dragging = useRef(false)
  const settleTimers = useRef<number[]>([])
  const watchFrame = useRef(0)
  const focusedElement = useRef<Element | null>(null)
  // pendingTail — хвост, который уступил жесту: его доводит первый кадр после жеста, даже если до низа меньше 40px.
  const pendingTail = useRef(false)
  // deferredRestore — восстановление якоря, которое ждёт конца жеста; compositions — составы, сменившиеся за жест,
  // от нового к старому: якорь мог быть снят в любом из них.
  const deferredRestore = useRef<{ timer: number; compositions: readonly (readonly string[])[] } | null>(null)

  // cancelCorrections — снимает отложенные поправки якоря: после движения пользователя они вернули бы его назад.
  const cancelCorrections = useCallback((): void => {
    generation.current++
    settleTimers.current.forEach(clearTimeout)
    settleTimers.current = []
  }, [])

  // Во время жеста пользователя лента сама не двигается: scroll-событие идёт раньше rAF, а follow в замыкании ещё не снят.
  const userGestureActive = useCallback((): boolean => dragging.current || performance.now() - inputAt.current < GESTURE_WINDOW_MS, [])

  const markRequest = useCallback((): void => {
    requestOrder.current = ++order.current
  }, [])

  const scrollToLatest = useCallback((): void => {
    cancelCorrections()
    markRequest()
    virtuoso.current?.scrollToIndex({ index: 'LAST', align: 'end', behavior: 'auto' })
  }, [cancelCorrections, markRequest])

  // followTail — автоматическая прокрутка к хвосту: во время жеста она уступает, не помечая запрос, иначе продолжение
  // прокрутки пользователя прочиталось бы как наша. Follow перечитывается в момент исполнения: отложенный вызов мог устареть.
  const followTail = useCallback((): void => {
    if (!latest.current.following) return
    pendingTail.current = userGestureActive()
    if (!pendingTail.current) scrollToLatest()
  }, [scrollToLatest, userGestureActive])

  // captureAnchors запоминает первую реально видимую строку (не сливер у края) и строку на линии чтения: их возвращает смена состава
  // и смена размеров. Скрытый (display:none) список не измерить.
  const captureAnchors = useCallback((): void => {
    const el = scroller.current
    if (el === null || el.clientHeight === 0) return
    const top = el.getBoundingClientRect().top
    const toAnchor = (element: Element): Anchor | null => {
      const row = rowElementOf(element)
      const key = row?.dataset.virtualRowKey
      if (row === null || key === undefined) return null
      const rect = element.getBoundingClientRect()
      return { key, element: element as HTMLElement, top: rect.top - top, rowOffset: rect.top - row.getBoundingClientRect().top }
    }
    const rowEls = [...el.querySelectorAll<HTMLElement>('[data-virtual-row-key]')]
    const firstVisible = rowEls.find((row) => row.getBoundingClientRect().bottom > top + MIN_ANCHOR_VISIBLE_PX)
    const reading = readingElement(el)
    anchors.current = { first: firstVisible === undefined ? null : toAnchor(firstVisible), reading: reading === null ? null : toAnchor(reading) }
  }, [])

  const cancelDeferredRestore = useCallback((): void => {
    window.clearTimeout(deferredRestore.current?.timer)
    deferredRestore.current = null
  }, [])

  // restoreAnchor ставит строку на прежнее место: абсолютный scrollToIndex, затем поправка остатка, когда размеры
  // устоялись. Вытесненный якорь заменяет ближайшая следующая строка.
  const restoreAnchor = useCallback((anchor: Anchor | null, previousKeys: readonly string[]): void => {
    if (anchor === null) return
    const nextKeys = latest.current.keys
    const present = new Set(nextKeys)
    let key = anchor.key
    if (!present.has(key)) {
      // Якорь, которого не было и в прошлом составе, устарел: заменять его нечем, а «первая живая строка» — голова списка.
      if (!previousKeys.includes(key)) return
      const next = previousKeys.slice(previousKeys.indexOf(key) + 1).find((k) => present.has(k))
      if (next === undefined) return
      key = next
    }
    const index = nextKeys.indexOf(key)
    cancelCorrections()
    cancelDeferredRestore()
    const own = generation.current
    const exact = key === anchor.key
    const wanted = exact ? anchor.top - anchor.rowOffset : 0
    markRequest()
    virtuoso.current?.scrollToIndex({ index, align: 'start', offset: -wanted, behavior: 'auto' })
    for (const delay of SETTLE_DELAYS_MS) {
      const timer = window.setTimeout(() => {
        const el = scroller.current
        // Скрытый (display:none) список не измерить: поправка по нулевым прямоугольникам сдвинула бы чтение.
        if (own !== generation.current || el === null || el.clientHeight === 0) return
        // Якорь перечитывается после поправки: следующее вытеснение не должно опираться на уже снятую строку.
        captureAnchors()
        if (!exact || !anchor.element.isConnected) return
        const delta = anchor.element.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.top
        if (Math.abs(delta) > RESIDUAL_TOLERANCE_PX) {
          markRequest()
          virtuoso.current?.scrollBy({ top: delta, behavior: 'auto' })
        }
      }, delay)
      settleTimers.current.push(timer)
    }
  }, [cancelCorrections, cancelDeferredRestore, captureAnchors, markRequest])

  // restoreWhenIdle — автоматическое восстановление якоря уступает жесту так же, как хвост: оно ждёт конца жеста и
  // берёт якорь, снятый последним, — иначе вернуло бы читателя назад и его markRequest отнял бы жест у пользователя.
  const restoreWhenIdle = useCallback((pick: () => Anchor | null, previousKeys: readonly string[]): void => {
    const compositions = [previousKeys, ...(deferredRestore.current?.compositions ?? [])]
    cancelDeferredRestore()
    const attempt = (): void => {
      if (userGestureActive()) {
        deferredRestore.current = { timer: window.setTimeout(attempt, GESTURE_WINDOW_MS), compositions }
        return
      }
      deferredRestore.current = null
      // Жест, вернувший follow, отдал ленту хвосту: возвращать строку уже некуда.
      if (latest.current.following) return
      const anchor = pick()
      // Преемника ищем в последнем составе, где якорь ещё был: в нём порядок строк перед его вытеснением.
      restoreAnchor(anchor, compositions.find((keys) => anchor !== null && keys.includes(anchor.key)) ?? [])
    }
    attempt()
  }, [cancelDeferredRestore, restoreAnchor, userGestureActive])

  useImperativeHandle(ref, () => ({
    scrollToLatest,
    scrollToRow(key, align) {
      const index = latest.current.keys.indexOf(key)
      if (index < 0) return
      cancelCorrections()
      cancelDeferredRestore()
      // Переход к цитате отменяет наблюдение за низом от свёртывания: иначе оно перебило бы его возобновлением follow.
      cancelAnimationFrame(watchFrame.current)
      markRequest()
      virtuoso.current?.scrollToIndex({ index, align, behavior: 'auto' })
    },
    anchor() {
      const first = anchors.current.first
      return first === null ? null : { key: first.key, offset: first.top - first.rowOffset }
    },
    hold(key) {
      // Новое раскрытие или свёртывание отменяет наблюдение за низом от прошлого свёртывания.
      cancelAnimationFrame(watchFrame.current)
      const el = scroller.current
      const row = el?.querySelector<HTMLElement>(`[data-virtual-row-key="${CSS.escape(key)}"]`)
      if (el === null || el === undefined || row === null || row === undefined) return
      // Действие читателя новее отложенного восстановления: оно не должно потом сдвинуть строку, по которой кликнули.
      cancelDeferredRestore()
      held.current = { key, element: row, top: row.getBoundingClientRect().top - el.getBoundingClientRect().top, rowOffset: 0 }
    },
    // Свёртывание у низа не даёт scroll-события, а отложенные поправки якоря может отменить любая смена состава:
    // возврат к низу ловим по кадрам, пока читатель не пошевелился.
    watchBottom() {
      cancelAnimationFrame(watchFrame.current)
      const until = performance.now() + WATCH_BOTTOM_MS
      // Наблюдение отменяет только ввод новее свёртывания: жест до него уже учтён раскладкой.
      const since = ++order.current
      const watch = (): void => {
        const el = scroller.current
        if (el === null || el.clientHeight === 0 || dragging.current || inputOrder.current > since || performance.now() > until) return
        if (isNearBottom(el)) {
          latest.current.onFollow({ type: 'collapsed', nearBottom: true })
          return
        }
        watchFrame.current = requestAnimationFrame(watch)
      }
      watchFrame.current = requestAnimationFrame(watch)
    },
  }), [scrollToLatest, cancelCorrections, cancelDeferredRestore, markRequest])

  // Смена состава: голову держит firstItemIndex, слияние/перенумерацию — якорь. Следующий за вытеснением
  // append якорь не трогает: у чистой головы headShift не null. Эффект идёт в каждом коммите: удержание гаснет в коммите
  // своего раскрытия, даже если состав не сменился (отчёт, Raw), и не достаётся следующему жесту.
  const previousKeys = useRef(keys)
  useLayoutEffect(() => {
    const previous = previousKeys.current
    previousKeys.current = keys
    latest.current = { keys, following, onFollow }
    const pinned = held.current
    held.current = null
    // hold() отменил отложенное восстановление вместе с историей составов: без свежих якорей следующая смена состава
    // искала бы строку, переименованную до клика, и ничего не вернула бы.
    if (pinned !== null && previous === keys) captureAnchors()
    if (previous === keys || following) return
    // Раскрытие и свёртывание — действие читателя, а не автоматика: его строка встаёт на место сразу.
    if (pinned !== null) restoreAnchor(pinned, previous)
    else if (headShift(previous, keys) === null) restoreWhenIdle(() => anchors.current.first, previous)
  })

  // В follow лента стоит у низа: после новой строки, роста последней строки и появления footer.
  useLayoutEffect(() => {
    if (following) followTail()
  }, [following, keys.length, tail, footerKey, followTail])

  // Размеры строк уточняются после коммита: высота списка и scrollTop расходятся, а поправка списка приходит
  // через несколько кадров. Пока лента ведёт хвост, каждый кадр возвращает её к низу, если она отстала.
  useEffect(() => {
    if (!following) return
    let frameId = 0
    const pin = (): void => {
      const el = scroller.current
      if (el !== null && el.clientHeight > 0 && (pendingTail.current || !isNearBottom(el))) followTail()
      frameId = requestAnimationFrame(pin)
    }
    frameId = requestAnimationFrame(pin)
    return () => cancelAnimationFrame(frameId)
  }, [following, followTail])

  // Размер или видимость рамки (ширина, режим фокуса, Team скрыт → показан): в follow — к низу, иначе назад к строке
  // на линии чтения.
  useEffect(() => {
    const target = frame.current
    if (target === null) return
    let hidden = false
    let width = target.clientWidth
    let height = target.clientHeight
    const observer = new ResizeObserver(() => {
      if (target.clientWidth === 0 || target.clientHeight === 0) {
        hidden = true
        // Пока рамка скрыта, поправки и наблюдение за низом ничего не меряют: при показе строку чтения вернёт restoreAnchor.
        cancelCorrections()
        cancelAnimationFrame(watchFrame.current)
        return
      }
      if (!hidden && target.clientWidth === width && target.clientHeight === height) return
      hidden = false
      width = target.clientWidth
      height = target.clientHeight
      if (latest.current.following) followTail()
      else restoreWhenIdle(() => anchors.current.reading, latest.current.keys)
    })
    observer.observe(target)
    return () => observer.disconnect()
  }, [restoreWhenIdle, followTail, cancelCorrections])

  useEffect(() => cancelCorrections, [cancelCorrections])
  useEffect(() => cancelDeferredRestore, [cancelDeferredRestore])
  useEffect(() => {
    if (startMissing) latest.current.onFollow({ type: 'reset' })
  }, [startMissing])
  useEffect(() => () => cancelAnimationFrame(watchFrame.current), [])

  // Скролл-события: follow меняет только жест пользователя — вниз до низа возобновляет, вверх от низа снимает.
  const attach = useCallback((node: HTMLElement | Window | null): void => {
    scroller.current = node instanceof HTMLElement ? node : null
    // Программно фокусируемая лента: сюда уходит фокус снятой строки.
    if (scroller.current !== null && !scroller.current.hasAttribute('tabindex')) scroller.current.tabIndex = -1
  }, [])
  useEffect(() => {
    const el = scroller.current
    if (el === null) return
    // Любое движение пользователя отменяет отложенные поправки и наблюдение за низом. Жест вниз у низа возвращает follow сразу:
    // если прокручивать уже некуда, scroll-события не будет.
    const userInput = (down: boolean): void => {
      inputOrder.current = ++order.current
      inputAt.current = performance.now()
      cancelCorrections()
      cancelAnimationFrame(watchFrame.current)
      if (down && isNearBottom(el)) latest.current.onFollow({ type: 'scroll', nearBottom: true, user: 'down' })
    }
    const onWheel = (e: WheelEvent): void => userInput(e.deltaY > 0)
    const onTouchMove = (): void => userInput(false)
    const onKey = (e: KeyboardEvent): void => {
      const space = e.key === ' '
      if (space && e.target instanceof Element && e.target !== el && e.target.closest(INTERACTIVE) !== null) return
      if (UP_KEYS.has(e.key) || (space && e.shiftKey)) userInput(false)
      else if (DOWN_KEYS.has(e.key) || space) userInput(true)
    }
    // Перетаскивание ползунка: нажатие на сам скроллер, а не на строку.
    const onPointerDown = (e: PointerEvent): void => {
      if (e.target !== el) return
      dragging.current = true
      userInput(false)
    }
    // Последнее scroll-событие после отпускания ползунка приходит позже pointerup: оно ещё принадлежит перетаскиванию.
    const onPointerUp = (): void => {
      if (!dragging.current) return
      dragging.current = false
      inputOrder.current = ++order.current
      inputAt.current = performance.now()
    }
    let captureFrame = 0
    const onScroll = (): void => {
      // Скрытый список читается как «у низа» (все размеры нулевые): follow он возобновлять не должен.
      if (el.clientHeight === 0) return
      const attributed = dragging.current || (inputOrder.current > requestOrder.current && userGestureActive())
      const moved = el.scrollTop - lastTop.current
      lastTop.current = el.scrollTop
      const user = attributed && moved !== 0 ? (moved > 0 ? 'down' : 'up') : null
      latest.current.onFollow({ type: 'scroll', nearBottom: isNearBottom(el), user })
      cancelAnimationFrame(captureFrame)
      captureFrame = requestAnimationFrame(captureAnchors)
    }
    const onFocusIn = (e: FocusEvent): void => {
      focusedElement.current = e.target instanceof Element ? e.target : null
    }
    // Фокус ушёл на другой элемент — отслеживать нечего; без relatedTarget это либо снятие строки, либо клик в пустоту.
    const onFocusOut = (e: FocusEvent): void => {
      if (e.relatedTarget !== null) focusedElement.current = null
    }
    // Сфокусированный элемент размонтирован виртуализатором: после коммита DOM фокус остаётся у ленты, а не в body.
    const handOffFocus = new MutationObserver(() => {
      const gone = focusedElement.current
      if (gone === null || gone.isConnected || document.activeElement !== document.body) return
      focusedElement.current = null
      el.focus({ preventScroll: true })
    })
    handOffFocus.observe(el, { childList: true, subtree: true })
    el.addEventListener('wheel', onWheel, { passive: true })
    el.addEventListener('touchmove', onTouchMove, { passive: true })
    el.addEventListener('keydown', onKey)
    el.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('pointerup', onPointerUp)
    el.addEventListener('scroll', onScroll, { passive: true })
    el.addEventListener('focusin', onFocusIn)
    el.addEventListener('focusout', onFocusOut)
    lastTop.current = el.scrollTop
    captureAnchors()
    return () => {
      cancelAnimationFrame(captureFrame)
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('touchmove', onTouchMove)
      el.removeEventListener('keydown', onKey)
      el.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('pointerup', onPointerUp)
      el.removeEventListener('scroll', onScroll)
      el.removeEventListener('focusin', onFocusIn)
      el.removeEventListener('focusout', onFocusOut)
      handOffFocus.disconnect()
    }
  }, [captureAnchors, cancelCorrections, userGestureActive])

  const context = useMemo<ListContext>(() => ({ footer }), [footer])

  return (
    <div className="feed-scroll-frame" ref={frame}>
      <Virtuoso<VirtualFeedRow, ListContext>
        ref={virtuoso}
        id="feed-content"
        className="feed-scroll"
        scrollerRef={attach}
        data={rows}
        context={context}
        components={COMPONENTS}
        firstItemIndex={firstItemIndex}
        initialTopMostItemIndex={initial}
        computeItemKey={(_, row) => row.key}
        itemContent={(index, row) => renderRow(row, index === firstItemIndex)}
        // Хвост ведёт только followTail: собственный followOutput списка двигал бы его и во время жеста пользователя.
        followOutput={false}
        atBottomThreshold={40}
        increaseViewportBy={{ top: VIEWPORT_MARGIN_PX, bottom: VIEWPORT_MARGIN_PX }}
        totalListHeightChanged={followTail}
      />
      {overlay}
    </div>
  )
})
