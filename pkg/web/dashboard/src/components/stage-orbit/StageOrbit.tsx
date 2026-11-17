import {
  useCallback, useEffect, useId, useLayoutEffect, useRef, useState,
  type CSSProperties, type KeyboardEvent, type PointerEvent, type ReactElement,
} from 'react'
import type { DeclaredAssistant } from '../../types/stage'
import { useMediaQuery } from '../../hooks/use-media-query'
import {
  ORBIT_SATELLITE_SIZE, orbitHubLabel, orbitInitials, orbitMoreLabel, orbitMoreText, orbitPageAnnouncement,
  orbitPages, orbitSlotOffsets, orbitTooltip, type OrbitPoint,
} from './orbit-model'

// Задержки hover-намерения: проход курсора по rail не раскрывает орбиту, короткий увод курсора не закрывает.
const HOVER_OPEN_MS = 150
const HOVER_CLOSE_MS = 250
const TOOLTIP_MAX_WIDTH = 220
const TOOLTIP_FLIP_HEIGHT = 72
const VIEWPORT_MARGIN = 8

type StageOrbitProps = {
  stageId: string
  assistants: DeclaredAssistant[]
  // selectedStageId — любая смена выбранной стадии закрывает орбиту, чтобы она не «зависала» над другой строкой.
  selectedStageId?: string | null
}

type Anchor = { x: number; y: number }
// Точка подсказки — середина её края, ближнего к дуге; below — подсказка под дугой.
type TooltipPlacement = OrbitPoint & { below: boolean }

const HUB_CENTRE: OrbitPoint = { x: 0, y: 0 }

// Хаб с орбитой объявленных помощников. Слой орбиты — DOM-потомок обёртки (не portal): порядок чтения,
// Tab-trap drawer и inert остаются как у соседних кнопок rail.
export function StageOrbit({ stageId, assistants, selectedStageId = null }: StageOrbitProps): ReactElement {
  const baseId = useId()
  const layerId = `${baseId}-layer`
  const summaryId = `${baseId}-summary`
  const wrapRef = useRef<HTMLSpanElement>(null)
  const hubRef = useRef<HTMLButtonElement>(null)
  const layerRef = useRef<HTMLSpanElement>(null)
  const satelliteRefs = useRef<(HTMLButtonElement | null)[]>([])
  const openTimer = useRef<number | undefined>(undefined)
  const closeTimer = useRef<number | undefined>(undefined)
  const pointerFocus = useRef(false)

  const coarse = useMediaQuery('(pointer: coarse)')
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const [open, setOpen] = useState(false)
  const [pinned, setPinned] = useState(false)
  const [pageIndex, setPageIndex] = useState(0)
  // Кнопка «+N» хранится как 'more', а не как позиция: на короткой странице её индекс меняется.
  const [activeTarget, setActiveTarget] = useState<number | 'more'>(0)
  const [tipIndex, setTipIndex] = useState<number | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  // Поправка на предка с transform: он делает его containing block для position: fixed.
  const [correction, setCorrection] = useState<Anchor>({ x: 0, y: 0 })

  const pages = orbitPages(assistants)
  const page = pages[pageIndex % pages.length] ?? { helpers: [], start: 0, remaining: 0 }
  const initials = orbitInitials(assistants)
  const nextPage = pages[(pageIndex + 1) % pages.length] ?? page
  const slotCount = page.helpers.length + (page.remaining > 0 ? 1 : 0)
  const offsets = orbitSlotOffsets(slotCount, coarse)
  const satSize = coarse ? ORBIT_SATELLITE_SIZE.coarse : ORBIT_SATELLITE_SIZE.fine
  const roving = activeTarget === 'more' ? slotCount - 1 : Math.min(activeTarget, slotCount - 1)

  const clearTimers = useCallback((): void => {
    window.clearTimeout(openTimer.current)
    window.clearTimeout(closeTimer.current)
  }, [])

  const close = useCallback((): void => {
    clearTimers()
    setOpen(false)
    setPinned(false)
    setTipIndex(null)
    setPageIndex(0)
    setActiveTarget(0)
    setAnnouncement('')
  }, [clearTimers])

  const measureHub = useCallback((): Anchor | null => {
    const hub = hubRef.current
    if (hub === null) return null
    const rect = hub.getBoundingClientRect()
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 }
  }, [])

  useLayoutEffect(() => {
    if (!open) return
    setAnchor(measureHub())
  }, [open, measureHub])

  useLayoutEffect(() => {
    const layer = layerRef.current
    if (!open || anchor === null || layer === null) return
    const rect = layer.getBoundingClientRect()
    const dx = anchor.x - (rect.left - correction.x)
    const dy = anchor.y - (rect.top - correction.y)
    if (Math.abs(dx - correction.x) > 0.5 || Math.abs(dy - correction.y) > 0.5) setCorrection({ x: dx, y: dy })
  }, [open, anchor, correction])

  // Закрытие: внешний pointerdown, resize/смена breakpoint, прокрутка (следуем, пока хаб виден целиком).
  useEffect(() => {
    if (!open) return
    function onPointerDown(e: globalThis.PointerEvent): void {
      if (!wrapRef.current?.contains(e.target as Node)) close()
    }
    function onScroll(): void {
      const hub = hubRef.current
      if (hub === null) return
      const rect = hub.getBoundingClientRect()
      const clip = wrapRef.current?.closest('.stages-scroll')?.getBoundingClientRect()
      const top = Math.max(0, clip?.top ?? 0)
      const bottom = Math.min(window.innerHeight, clip?.bottom ?? window.innerHeight)
      if (rect.top < top || rect.bottom > bottom) close()
      else setAnchor(measureHub())
    }
    function onEscape(e: globalThis.KeyboardEvent): void {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation()
      if (wrapRef.current?.contains(document.activeElement)) {
        pointerFocus.current = true // возврат фокуса на хаб не должен снова раскрывать орбиту
        hubRef.current?.focus()
      }
      close()
    }
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('keydown', onEscape, true)
    window.addEventListener('resize', close)
    window.addEventListener('scroll', onScroll, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('keydown', onEscape, true)
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', onScroll, true)
    }
  }, [open, close, measureHub])

  const previousSelection = useRef(selectedStageId)
  useEffect(() => {
    if (previousSelection.current !== selectedStageId) close()
    previousSelection.current = selectedStageId
  }, [selectedStageId, close])

  // Хаб исчезает при первом старте стадии: фокус не должен потеряться на body.
  useLayoutEffect(() => {
    const wrap = wrapRef.current
    return () => {
      window.clearTimeout(openTimer.current)
      window.clearTimeout(closeTimer.current)
      if (wrap !== null && wrap.contains(document.activeElement)) {
        wrap.closest('.stage-item')?.querySelector<HTMLElement>('.stage-row')?.focus({ preventScroll: true })
      }
    }
  }, [])

  function scheduleOpen(e: PointerEvent): void {
    if (e.pointerType === 'touch') return
    window.clearTimeout(closeTimer.current)
    if (open) return
    window.clearTimeout(openTimer.current)
    openTimer.current = window.setTimeout(() => setOpen(true), HOVER_OPEN_MS)
  }

  function scheduleClose(e: PointerEvent): void {
    if (e.pointerType === 'touch') return
    window.clearTimeout(openTimer.current)
    window.clearTimeout(closeTimer.current)
    closeTimer.current = window.setTimeout(() => {
      if (!pinned && !wrapRef.current?.contains(document.activeElement)) close()
    }, HOVER_CLOSE_MS)
  }

  function onHubClick(): void {
    window.clearTimeout(openTimer.current)
    if (open && pinned) {
      close()
      return
    }
    setOpen(true)
    setPinned(true)
  }

  function onHubFocus(): void {
    if (pointerFocus.current) return
    setOpen(true)
  }

  function onBlur(e: React.FocusEvent): void {
    if (!open) return
    if (!wrapRef.current?.contains(e.relatedTarget as Node | null)) close()
  }

  function focusSlot(slot: number): void {
    setActiveTarget(page.remaining > 0 && slot === page.helpers.length ? 'more' : slot)
    satelliteRefs.current[slot]?.focus()
  }

  function onLayerKeyDown(e: KeyboardEvent): void {
    const forward = (roving + 1) % slotCount
    const backward = (roving - 1 + slotCount) % slotCount
    const next: Record<string, number> = {
      ArrowRight: forward,
      ArrowDown: forward,
      ArrowLeft: backward,
      ArrowUp: backward,
      Home: 0,
      End: slotCount - 1,
    }
    if (!(e.key in next)) return
    e.preventDefault()
    focusSlot(next[e.key] ?? 0)
  }

  function showNextPage(): void {
    const nextIndex = (pageIndex + 1) % pages.length
    const nextPage = pages[nextIndex]
    if (nextPage === undefined) return
    // Фокус переводим на «+N» до смены страницы: кнопка переживает рендер, а сфокусированный помощник — нет.
    focusSlot(page.helpers.length)
    setPageIndex(nextIndex)
    setAnnouncement(orbitPageAnnouncement(nextPage, assistants.length))
    setTipIndex(null)
  }

  const tipHelper = tipIndex === null ? undefined : page.helpers[tipIndex]
  const tipOffset = tipIndex === null ? undefined : offsets[tipIndex]
  const tip = anchor === null || tipOffset === undefined ? undefined : tooltipPlacement(anchor, tipOffset, offsets, satSize)

  const layerStyle: CSSProperties | undefined = anchor === null
    ? { visibility: 'hidden' }
    : { left: anchor.x + correction.x, top: anchor.y + correction.y }

  return (
    <span
      ref={wrapRef}
      className="stage-orbit"
      data-open={open ? 'true' : undefined}
      onPointerEnter={scheduleOpen}
      onPointerLeave={scheduleClose}
      onBlur={onBlur}
    >
      <button
        ref={hubRef}
        type="button"
        className="stage-orbit-hub"
        aria-label={orbitHubLabel(assistants.length, stageId)}
        aria-expanded={open}
        aria-controls={open ? layerId : undefined}
        aria-describedby={summaryId}
        onPointerDown={() => { pointerFocus.current = true }}
        onKeyDown={() => { pointerFocus.current = false }}
        onClick={onHubClick}
        onFocus={onHubFocus}
        onBlur={() => { pointerFocus.current = false }}
      >
        <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden="true">
          <circle cx="5.2" cy="5.6" r="2" />
          <circle cx="11" cy="5.6" r="1.7" />
          <path d="M1.8 13c0-2 1.5-3.3 3.4-3.3S8.6 11 8.6 13M9.4 9.9c.5-.2 1-.3 1.6-.3 1.8 0 3.2 1.2 3.2 3.2" />
        </svg>
        <span className="stage-orbit-count" aria-hidden="true">{assistants.length}</span>
      </button>
      <span id={summaryId} className="stage-orbit-sr">{assistants.map((h) => h.name).join(', ')}</span>
      {assistants.map((helper, i) => (
        <span key={i} id={`${baseId}-d${i}`} className="stage-orbit-sr">{orbitTooltip(helper)}</span>
      ))}
      {open && (
        <span
          ref={layerRef}
          id={layerId}
          className="stage-orbit-layer"
          role="group"
          aria-label={`Helpers declared for ${stageId}`}
          data-reduced-motion={reducedMotion ? 'true' : undefined}
          style={layerStyle}
          onMouseDown={(e) => e.preventDefault()}
          onKeyDown={onLayerKeyDown}
        >
          {offsets.map((offset, i) => (
            <span key={i} className="stage-orbit-bridge" style={bridgeStyle(HUB_CENTRE, offset, satSize)} aria-hidden="true" />
          ))}
          {tip !== undefined && tipOffset !== undefined && (
            // Заходит на 1px внутрь подсказки и лежит под спутниками и подсказкой: путь к подсказке без щели.
            <span
              className="stage-orbit-bridge stage-orbit-tooltip-bridge"
              style={bridgeStyle(tipOffset, { x: tip.x, y: tip.below ? tip.y + 1 : tip.y - 1 }, satSize)}
              aria-hidden="true"
            />
          )}
          {page.helpers.map((helper, i) => (
            <button
              key={`${pageIndex}:${page.start + i}`}
              ref={(el) => { satelliteRefs.current[i] = el }}
              type="button"
              className="stage-orbit-satellite"
              aria-label={helper.name}
              aria-describedby={`${baseId}-d${page.start + i}`}
              tabIndex={roving === i ? 0 : -1}
              style={satelliteStyle(offsets[i], i, reducedMotion)}
              onPointerEnter={() => setTipIndex(i)}
              onFocus={() => { setActiveTarget(i); setTipIndex(i) }}
              onClick={() => setTipIndex(i)}
            >
              <span aria-hidden="true">{initials[page.start + i]}</span>
            </button>
          ))}
          {page.remaining > 0 && (
            <button
              ref={(el) => { satelliteRefs.current[page.helpers.length] = el }}
              type="button"
              className="stage-orbit-satellite stage-orbit-more"
              aria-label={orbitMoreLabel(nextPage, assistants.length)}
              tabIndex={roving === page.helpers.length ? 0 : -1}
              style={satelliteStyle(offsets[page.helpers.length], page.helpers.length, reducedMotion)}
              onFocus={() => { setActiveTarget('more'); setTipIndex(null) }}
              onClick={showNextPage}
            >
              <span aria-hidden="true">{orbitMoreText(page)}</span>
            </button>
          )}
          {tipHelper !== undefined && (
            <span className="stage-orbit-tooltip" aria-hidden="true" style={tip && tooltipStyle(tip)}>{orbitTooltip(tipHelper)}</span>
          )}
          <span className="stage-orbit-sr" role="status" aria-live="polite">{announcement}</span>
        </span>
      )}
    </span>
  )
}

// Полоса идёт от центра до центра без разрыва, иначе медленный курсор покидает орбиту по пути.
// Область нажатия хаба остаётся за хабом: открытый хаб лежит над слоем (z-index в stages-list.css).
function bridgeStyle(from: OrbitPoint, to: OrbitPoint, satSize: number): CSSProperties {
  const [dx, dy] = [to.x - from.x, to.y - from.y]
  return {
    left: from.x,
    top: from.y,
    width: Math.hypot(dx, dy),
    height: satSize * 0.8,
    transform: `rotate(${Math.atan2(dy, dx)}rad)`,
  }
}

// Один расчёт для подсказки и её перемычки: иначе у края окна или при перевороте под дугу они разойдутся.
function tooltipPlacement(anchor: Anchor, tipOffset: OrbitPoint, offsets: OrbitPoint[], satSize: number): TooltipPlacement {
  const half = TOOLTIP_MAX_WIDTH / 2 + VIEWPORT_MARGIN
  const centerX = Math.min(Math.max(anchor.x + tipOffset.x, half), window.innerWidth - half)
  const arcTop = Math.min(...offsets.map((o) => o.y)) - satSize / 2 - 6
  const arcBottom = Math.max(...offsets.map((o) => o.y)) + satSize / 2 + 6
  const below = anchor.y + arcTop < TOOLTIP_FLIP_HEIGHT
  return { x: centerX - anchor.x, y: below ? arcBottom : arcTop, below }
}

function tooltipStyle(tip: TooltipPlacement): CSSProperties {
  return { left: tip.x, top: tip.y, transform: tip.below ? 'translateX(-50%)' : 'translate(-50%, -100%)' }
}

function satelliteStyle(offset: { x: number; y: number } | undefined, index: number, reducedMotion: boolean): CSSProperties {
  const style: Record<string, string> = { '--orbit-dx': `${offset?.x ?? 0}px`, '--orbit-dy': `${offset?.y ?? 0}px` }
  if (!reducedMotion) style['--orbit-delay'] = `${index * 40}ms`
  return style as CSSProperties
}
