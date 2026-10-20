import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent } from 'react'

const MIN_WIDTH = 200
const MAX_WIDTH = 560
const CONTENT_WIDTH = 320
const STEP = 16

type Options = {
  storageKey: string
  defaultWidth: number
  // The People panel is the last child of its layout; Team owns its layout node.
  container: 'parent' | 'self'
  direction: -1 | 1
  drawerQuery: string
  drawerBelow?: number
  toggleSelector: string
}

function clamp(width: number, max: number): number {
  return Math.max(MIN_WIDTH, Math.min(width, max))
}

function loadWidth(key: string, fallback: number): number {
  try {
    const raw = window.localStorage.getItem(key)
    if (raw !== null) {
      const width = Number(raw)
      if (Number.isFinite(width) && width >= MIN_WIDTH && width <= MAX_WIDTH) return width
    }
  } catch {
    // A blocked storage API leaves resizing available for this visit.
  }
  return fallback
}

export function usePanelResize({ storageKey, defaultWidth, container, direction, drawerQuery, drawerBelow, toggleSelector }: Options) {
  const targetRef = useRef<HTMLDivElement | null>(null)
  const handleRef = useRef<HTMLDivElement | null>(null)
  const focusedHandleRef = useRef(false)
  const cleanupDrag = useRef<(() => void) | null>(null)
  const [preferredWidth, setPreferredWidth] = useState(() => loadWidth(storageKey, defaultWidth))
  const [containerWidth, setContainerWidth] = useState<number | null>(null)
  const [viewportDrawer, setViewportDrawer] = useState(() => window.matchMedia?.(drawerQuery).matches ?? false)

  useLayoutEffect(() => {
    const target = targetRef.current
    const layout = container === 'parent' ? target?.parentElement : target
    if (layout === undefined || layout === null) return
    const measure = (): void => {
      const width = layout.getBoundingClientRect().width || layout.clientWidth
      if (width > 0) setContainerWidth(width)
    }
    measure()
    if (typeof ResizeObserver !== 'undefined') {
      const observer = new ResizeObserver(measure)
      observer.observe(layout)
      return () => observer.disconnect()
    }
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [container])

  useEffect(() => {
    const query = window.matchMedia?.(drawerQuery)
    if (query === undefined) return
    const update = (): void => setViewportDrawer(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [drawerQuery])

  const maxWidth = containerWidth === null ? MAX_WIDTH : Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, containerWidth - CONTENT_WIDTH))
  const maxWidthRef = useRef(maxWidth)
  maxWidthRef.current = maxWidth
  const effectiveWidth = clamp(preferredWidth, maxWidth)
  const effectiveWidthRef = useRef(effectiveWidth)
  effectiveWidthRef.current = effectiveWidth
  const isDrawer = viewportDrawer || (drawerBelow !== undefined && containerWidth !== null && containerWidth < drawerBelow)

  useEffect(() => {
    const onFocusIn = (event: FocusEvent): void => {
      focusedHandleRef.current = event.target === handleRef.current
    }
    const onFocusOut = (event: FocusEvent): void => {
      const handle = handleRef.current
      if (event.target === handle && handle !== null && getComputedStyle(handle).display !== 'none') {
        focusedHandleRef.current = false
      }
    }
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('focusout', onFocusOut)
    return () => {
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('focusout', onFocusOut)
    }
  }, [])

  useEffect(() => {
    if (isDrawer && (document.activeElement === handleRef.current ||
      (document.activeElement === document.body && focusedHandleRef.current))) {
      focusedHandleRef.current = false
      document.querySelector<HTMLElement>(toggleSelector)?.focus()
    }
  }, [isDrawer, toggleSelector])

  useEffect(() => () => cleanupDrag.current?.(), [])

  function save(width: number): void {
    const next = clamp(width, MAX_WIDTH)
    setPreferredWidth(next)
    try {
      window.localStorage.setItem(storageKey, String(next))
    } catch {
      // Resizing still works when storage is unavailable.
    }
  }

  function commit(width: number): void {
    const next = clamp(width, maxWidthRef.current)
    if (next === effectiveWidthRef.current) return
    effectiveWidthRef.current = next
    save(next)
  }

  function onMouseDown(event: MouseEvent<HTMLDivElement>): void {
    if (isDrawer || event.button !== 0) return
    event.preventDefault()
    cleanupDrag.current?.()
    const startX = event.clientX
    const startWidth = effectiveWidth
    const oldCursor = document.body.style.cursor
    const oldUserSelect = document.body.style.userSelect
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    const onMove = (move: globalThis.MouseEvent): void => commit(startWidth + direction * (move.clientX - startX))
    const onUp = (): void => cleanupDrag.current?.()
    const cleanup = (): void => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      window.removeEventListener('blur', onUp)
      document.body.style.cursor = oldCursor
      document.body.style.userSelect = oldUserSelect
      cleanupDrag.current = null
    }
    cleanupDrag.current = cleanup
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    window.addEventListener('blur', onUp)
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (isDrawer) return
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault()
      commit(effectiveWidth + direction * (event.key === 'ArrowRight' ? STEP : -STEP))
    }
  }

  return {
    targetRef, handleRef, effectiveWidth, maxWidth, isDrawer, onMouseDown, onKeyDown,
    onDoubleClick: () => save(defaultWidth),
  }
}
