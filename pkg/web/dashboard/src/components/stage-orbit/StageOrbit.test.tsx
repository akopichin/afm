import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeclaredAssistant } from '../../types/stage'
import stagesListCss from '../../../skins/base/stages-list.css?raw'
import { StageOrbit } from './StageOrbit'
import { ORBIT_HUB_HIT_HALF, ORBIT_SATELLITE_SIZE } from './orbit-model'

// jsdom has no PointerEvent: without it the pointerType of synthetic events is lost.
class TestPointerEvent extends MouseEvent {
  readonly pointerType: string
  constructor(type: string, init: MouseEventInit & { pointerType?: string } = {}) {
    super(type, init)
    this.pointerType = init.pointerType ?? ''
  }
}
Object.defineProperty(window, 'PointerEvent', { value: TestPointerEvent, configurable: true })

function helpers(n: number): DeclaredAssistant[] {
  return Array.from({ length: n }, (_, i) => ({
    profile: `p${i}`, name: `Helper ${i}`, trigger: i === 1 ? 'checkpoints' : 'one_shot', mode: i === 1 ? 'required' : 'advisory',
  }))
}

function renderOrbit(n = 3, selectedStageId: string | null = null) {
  const onSelect = vi.fn()
  const view = render(
    <li className="stage-item" data-stage-id="s1">
      <button type="button" className="stage-row" onClick={onSelect}>row</button>
      <span className="stage-actions">
        <StageOrbit stageId="s1" assistants={helpers(n)} selectedStageId={selectedStageId} />
      </span>
    </li>,
  )
  return { ...view, onSelect }
}

const hub = () => screen.getByRole('button', { name: /declared for s1/ })
const layer = () => screen.queryByRole('group', { name: /Helpers declared/ })
const satellites = () => screen.getAllByRole('button').filter((b) => b.classList.contains('stage-orbit-satellite'))
const pointer = (el: Element, type: 'pointerenter' | 'pointerleave', pointerType = 'mouse') =>
  type === 'pointerenter' ? fireEvent.pointerEnter(el, { pointerType }) : fireEvent.pointerLeave(el, { pointerType })

describe('StageOrbit', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('shows a hub with the helper count and no layer until opened', () => {
    renderOrbit(3)
    expect(hub().getAttribute('aria-label')).toBe('3 helpers declared for s1')
    expect(hub().getAttribute('aria-expanded')).toBe('false')
    expect(hub().getAttribute('aria-controls')).toBeNull()
    expect(layer()).toBeNull()
  })

  it('opens after the hover intent delay and closes after the grace period', () => {
    renderOrbit(3)
    const wrap = hub().closest('.stage-orbit')!
    pointer(wrap, 'pointerenter')
    act(() => { vi.advanceTimersByTime(100) })
    expect(layer()).toBeNull()
    act(() => { vi.advanceTimersByTime(60) })
    expect(layer()).not.toBeNull()
    expect(hub().getAttribute('aria-expanded')).toBe('true')
    expect(satellites()).toHaveLength(3)

    pointer(wrap, 'pointerleave')
    act(() => { vi.advanceTimersByTime(200) })
    expect(layer()).not.toBeNull()
    pointer(wrap, 'pointerenter') // returning within the grace keeps it open
    act(() => { vi.advanceTimersByTime(400) })
    expect(layer()).not.toBeNull()
    pointer(wrap, 'pointerleave')
    act(() => { vi.advanceTimersByTime(260) })
    expect(layer()).toBeNull()
  })

  it('does not open when the pointer only passes through', () => {
    renderOrbit(3)
    const wrap = hub().closest('.stage-orbit')!
    pointer(wrap, 'pointerenter')
    act(() => { vi.advanceTimersByTime(80) })
    pointer(wrap, 'pointerleave')
    act(() => { vi.advanceTimersByTime(500) })
    expect(layer()).toBeNull()
  })

  it('ignores touch hover and pins on click without selecting the stage', () => {
    const { onSelect } = renderOrbit(3)
    const wrap = hub().closest('.stage-orbit')!
    pointer(wrap, 'pointerenter', 'touch')
    act(() => { vi.advanceTimersByTime(500) })
    expect(layer()).toBeNull()

    fireEvent.click(hub())
    expect(layer()).not.toBeNull()
    pointer(wrap, 'pointerleave')
    act(() => { vi.advanceTimersByTime(500) })
    expect(layer()).not.toBeNull() // pinned
    fireEvent.click(satellites()[0]!)
    expect(onSelect).not.toHaveBeenCalled()
    fireEvent.click(hub())
    expect(layer()).toBeNull()
  })

  it('closes a pinned orbit on an outside pointerdown only', () => {
    renderOrbit(3)
    fireEvent.click(hub())
    fireEvent.pointerDown(satellites()[0]!)
    expect(layer()).not.toBeNull()
    fireEvent.pointerDown(document.body)
    expect(layer()).toBeNull()
  })

  it('shows "Name · Trigger" with Required in an aria-hidden tooltip on satellite focus', () => {
    renderOrbit(3)
    fireEvent.click(hub())
    act(() => { satellites()[1]!.focus() })
    const tip = document.querySelector('.stage-orbit-tooltip')!
    expect(tip.textContent).toBe('Helper 1 · At checkpoints · Required')
    expect(tip.getAttribute('aria-hidden')).toBe('true')
    expect(satellites()[1]!.getAttribute('aria-label')).toBe('Helper 1')
    const described = document.getElementById(satellites()[1]!.getAttribute('aria-describedby')!)
    expect(described?.textContent).toBe('Helper 1 · At checkpoints · Required')
  })

  it('opens on keyboard focus and moves between satellites with roving tabindex', () => {
    renderOrbit(4)
    act(() => { hub().focus() })
    expect(layer()).not.toBeNull()
    const sats = satellites()
    expect(sats.map((s) => s.tabIndex)).toEqual([0, -1, -1, -1])
    act(() => { sats[0]!.focus() })
    fireEvent.keyDown(sats[0]!, { key: 'ArrowRight' })
    expect(document.activeElement).toBe(sats[1])
    expect(sats.map((s) => s.tabIndex)).toEqual([-1, 0, -1, -1])
    fireEvent.keyDown(sats[1]!, { key: 'End' })
    expect(document.activeElement).toBe(sats[3])
    fireEvent.keyDown(sats[3]!, { key: 'Home' })
    expect(document.activeElement).toBe(sats[0])
  })

  it('Escape closes, returns focus to the hub and stops the event; a closed orbit lets it pass', () => {
    renderOrbit(3)
    const outer = vi.fn()
    document.addEventListener('keydown', outer)
    act(() => { hub().focus() })
    act(() => { satellites()[0]!.focus() })
    fireEvent.keyDown(satellites()[0]!, { key: 'Escape' })
    expect(layer()).toBeNull()
    expect(document.activeElement).toBe(hub())
    expect(outer).not.toHaveBeenCalled()

    fireEvent.keyDown(hub(), { key: 'Escape' })
    expect(outer).toHaveBeenCalledTimes(1)
    document.removeEventListener('keydown', outer)
  })

  // jsdom has neither layout nor hit testing, so the emitted geometry is hit-tested here: inside the fixed layer
  // later siblings paint over earlier ones, and the hub hit square competes with the whole layer by z-index.
  describe('hover geometry: hub, satellite and tooltip stay connected', () => {
    type Point = { x: number; y: number }
    const TOOLTIP_BOX = { width: 60, height: 28 } // one short line; jsdom does not size the tooltip
    const HUB_CENTRE: Point = { x: 0, y: 0 }
    let sheet: HTMLStyleElement

    beforeEach(() => {
      sheet = document.createElement('style')
      sheet.textContent = stagesListCss
      document.head.append(sheet)
    })
    afterEach(() => {
      sheet.remove()
      vi.restoreAllMocks()
      vi.unstubAllGlobals()
    })

    const px = (value: string) => parseFloat(value) || 0
    const zIndex = (el: Element) => parseInt(getComputedStyle(el).zIndex, 10) || 0
    const slotCentre = (el: HTMLElement): Point => ({
      x: px(el.style.getPropertyValue('--orbit-dx')), y: px(el.style.getPropertyValue('--orbit-dy')),
    })
    const sizes = (coarse: boolean) => ({
      half: coarse ? ORBIT_HUB_HIT_HALF.coarse : ORBIT_HUB_HIT_HALF.fine,
      satSize: coarse ? ORBIT_SATELLITE_SIZE.coarse : ORBIT_SATELLITE_SIZE.fine,
    })
    const pageCount = (n: number) => (n > 4 ? Math.ceil(n / 3) : 1)

    // Coordinates below are relative to the hub centre, placed at `at` in the 1024px-wide jsdom window.
    function openOrbit(n: number, coarse: boolean, at: Point = { x: 250, y: 400 }) {
      vi.stubGlobal('matchMedia', (query: string) => ({
        matches: coarse && query.includes('pointer: coarse'), addEventListener: () => {}, removeEventListener: () => {},
      }))
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
        const isLayer = this.classList.contains('stage-orbit-layer')
        const size = isLayer ? 0 : 26
        const left = isLayer ? px(this.style.left) : at.x - size / 2
        const top = isLayer ? px(this.style.top) : at.y - size / 2
        return { left, top, right: left + size, bottom: top + size, x: left, y: top, width: size, height: size, toJSON: () => ({}) }
      })
      renderOrbit(n)
      fireEvent.click(hub())
    }

    // Shape of a layer child as a containment test; margin > 0 also counts its edge, margin < 0 only its interior.
    function shapeOf(el: HTMLElement, satSize: number): (p: Point, margin: number) => boolean {
      if (el.classList.contains('stage-orbit-satellite')) {
        const c = slotCentre(el)
        return (p, margin) => Math.hypot(p.x - c.x, p.y - c.y) <= satSize / 2 + margin
      }
      if (el.classList.contains('stage-orbit-tooltip')) {
        const [x, y] = [px(el.style.left), px(el.style.top)]
        const [top, bottom] = el.style.transform === 'translateX(-50%)' ? [y, y + TOOLTIP_BOX.height] : [y - TOOLTIP_BOX.height, y]
        return (p, margin) => Math.abs(p.x - x) <= TOOLTIP_BOX.width / 2 + margin && p.y >= top - margin && p.y <= bottom + margin
      }
      if (el.classList.contains('stage-orbit-bridge')) {
        // The CSS centres the strip's height on (left, top) and turns it there: rotate(θ), optionally then translateX.
        const angle = Number(/rotate\(([-\d.e]+)rad\)/.exec(el.style.transform)?.[1])
        const shift = Number(/translateX\(([-\d.e]+)px\)/.exec(el.style.transform)?.[1] ?? 0)
        const [left, top, length, halfHeight] = [px(el.style.left), px(el.style.top), px(el.style.width), px(el.style.height) / 2]
        const [cos, sin] = [Math.cos(angle), Math.sin(angle)]
        return (p, margin) => {
          const [dx, dy] = [p.x - left, p.y - top]
          const along = dx * cos + dy * sin - shift
          const across = -dx * sin + dy * cos
          return along >= -margin && along <= length + margin && Math.abs(across) <= halfHeight + margin
        }
      }
      return () => false
    }

    // Topmost orbit element under a point, or null where the pointer would leave the orbit.
    function hitTester(coarse: boolean): (p: Point) => Element | null {
      const { half, satSize } = sizes(coarse)
      const hubEl = hub()
      const hubAbove = zIndex(hubEl) > zIndex(layer()!)
      const painted = Array.from(layer()!.children as HTMLCollectionOf<HTMLElement>).reverse()
        .map((el) => ({ el, contains: shapeOf(el, satSize) }))
      return (p) => {
        const inHub = Math.abs(p.x) <= half && Math.abs(p.y) <= half
        if (inHub && hubAbove) return hubEl
        // Below the layer the hub loses only points a layer element covers with its interior: a shared edge is a tie.
        const margin = inHub ? -1e-6 : 1e-6
        return painted.find(({ contains }) => contains(p, margin))?.el ?? (inHub ? hubEl : null)
      }
    }

    // The closed hub hit square every 0.5px, edges and corners included.
    function squarePoints(half: number): Point[] {
      const steps = Array.from({ length: 4 * half + 1 }, (_, i) => -half + i / 2)
      return steps.flatMap((x) => steps.map((y) => ({ x, y })))
    }

    // A band along from→to: samples every ≤0.5px along it, on lines every ≤2px across its width.
    function bandPoints(from: Point, to: Point, halfWidth = 0): Point[] {
      const length = Math.hypot(to.x - from.x, to.y - from.y)
      const [ux, uy] = [(to.x - from.x) / length, (to.y - from.y) / length]
      const alongCount = Math.ceil(length * 2)
      const acrossCount = Math.ceil(halfWidth)
      const points: Point[] = []
      for (let i = 0; i <= alongCount; i++) {
        for (let j = 0; j <= acrossCount; j++) {
          const along = (length * i) / alongCount
          const across = acrossCount === 0 ? 0 : -halfWidth + (2 * halfWidth * j) / acrossCount
          points.push({ x: from.x + ux * along - uy * across, y: from.y + uy * along + ux * across })
        }
      }
      return points
    }

    function expectHubOwnsItsSquare(coarse: boolean) {
      const [hitAt, hubEl] = [hitTester(coarse), hub()]
      const lost = squarePoints(sizes(coarse).half).filter((p) => hitAt(p) !== hubEl)
      expect(lost[0], `${lost.length} hub square points hit the orbit layer instead`).toBeUndefined()
    }

    // For each helper on each page: satellite → tooltip stays inside the orbit, the connector stays under the
    // satellite and the tooltip, and the hub keeps its square; paging drops the connector of the replaced helper.
    function expectTooltipPathsConnected(n: number, coarse: boolean, at: Point, check?: (sat: HTMLElement, tip: HTMLElement) => void) {
      openOrbit(n, coarse, at)
      for (let page = 0; page < pageCount(n); page++) {
        for (const sat of satellites().filter((s) => !s.classList.contains('stage-orbit-more'))) {
          fireEvent.pointerEnter(sat)
          const tip = document.querySelector<HTMLElement>('.stage-orbit-tooltip')!
          const below = tip.style.transform === 'translateX(-50%)'
          const edge = { x: px(tip.style.left), y: px(tip.style.top) }
          const hitAt = hitTester(coarse)
          const outside = bandPoints(slotCentre(sat), edge).filter((p) => hitAt(p) === null)
          expect(outside[0], `${outside.length} points outside between ${sat.getAttribute('aria-label')} and its tooltip`).toBeUndefined()
          expect(hitAt(slotCentre(sat))).toBe(sat)
          expect(hitAt({ x: edge.x, y: edge.y + (below ? 2 : -2) })).toBe(tip)
          expectHubOwnsItsSquare(coarse)
          check?.(sat, tip)
        }
        if (page < pageCount(n) - 1) {
          fireEvent.click(screen.getByRole('button', { name: /^Show helpers/ }))
          expect(document.querySelector('.stage-orbit-tooltip-bridge')).toBeNull()
        }
      }
    }

    const modes = [['fine', false], ['coarse', true]] as const
    const sideTransform = { above: 'translate(-50%, -100%)', below: 'translateX(-50%)' }
    const helperCounts = modes.flatMap(([mode, coarse]) => [1, 2, 3, 4, 7].map((n) => [mode, n, coarse] as const))

    it.each(helperCounts)('%s pointer, %i helpers: the hub keeps its square and a full-width path reaches every slot on every page', (_, n, coarse) => {
      openOrbit(n, coarse)
      const { satSize } = sizes(coarse)
      for (let page = 0; page < pageCount(n); page++) {
        expectHubOwnsItsSquare(coarse)
        const hitAt = hitTester(coarse)
        for (const sat of satellites()) {
          const outside = bandPoints(HUB_CENTRE, slotCentre(sat), satSize * 0.4).filter((p) => hitAt(p) === null)
          expect(outside[0], `${outside.length} points outside on the way to ${sat.getAttribute('aria-label')}`).toBeUndefined()
        }
        if (page < pageCount(n) - 1) fireEvent.click(screen.getByRole('button', { name: /^Show helpers/ }))
      }
    })

    it.each(helperCounts.flatMap(([mode, n, coarse]) => (['above', 'below'] as const).map((side) => [mode, n, side, coarse] as const)))(
      '%s pointer, %i helpers: each satellite connects to its tooltip %s the arc',
      (_, n, side, coarse) => {
        expectTooltipPathsConnected(n, coarse, { x: 250, y: side === 'above' ? 400 : 50 }, (_sat, tip) => {
          expect(tip.style.transform).toBe(sideTransform[side])
        })
      },
    )

    it.each(modes.flatMap(([mode, coarse]) => [
      [mode, 'left', 'above', coarse, { x: 90, y: 400 }],
      [mode, 'right', 'below', coarse, { x: 980, y: 50 }],
      [mode, 'left', 'below', coarse, { x: 90, y: 50 }],
      [mode, 'right', 'above', coarse, { x: 980, y: 400 }],
    ] as const))('%s pointer: a tooltip clamped at the %s window edge, %s the arc, stays connected on every page', (_, _edge, side, coarse, at) => {
      expectTooltipPathsConnected(7, coarse, at, (sat, tip) => {
        expect(px(tip.style.left)).not.toBeCloseTo(slotCentre(sat).x)
        expect(tip.style.transform).toBe(sideTransform[side])
      })
    })
  })

  it('coarse hub hit area is at least 44px: 24px padding box plus the ::after inset', () => {
    const coarseBlock = /@media \(pointer: coarse\) \{[^@]*?\.stage-orbit-hub::after \{ inset: -(\d+)px; \}/.exec(stagesListCss)
    expect(coarseBlock).not.toBeNull()
    expect(24 + 2 * Number(coarseBlock![1])).toBeGreaterThanOrEqual(44)
    expect(2 * ORBIT_HUB_HIT_HALF.coarse).toBe(44)
  })

  it('pages through more than four helpers with +N, wraps, and announces the range', () => {
    renderOrbit(7)
    fireEvent.click(hub())
    expect(satellites().map((s) => s.getAttribute('aria-label'))).toEqual(['Helper 0', 'Helper 1', 'Helper 2', 'Show helpers 4–6 of 7'])
    const more = () => satellites()[satellites().length - 1]!
    act(() => { more().focus() })
    fireEvent.click(more())
    expect(satellites().map((s) => s.getAttribute('aria-label')).slice(0, 3)).toEqual(['Helper 3', 'Helper 4', 'Helper 5'])
    expect(document.activeElement).toBe(more())
    expect(more().textContent).toBe('+4')
    expect(more().getAttribute('aria-label')).toBe('Show helpers 7 of 7')
    expect(screen.getByRole('status').textContent).toBe('Showing helpers 4 to 6 of 7')
    fireEvent.click(more())
    expect(satellites()[0]!.getAttribute('aria-label')).toBe('Helper 6')
    expect(more().getAttribute('aria-label')).toBe('Show helpers 1–3 of 7')
    expect(more().textContent).toBe('+6')
    fireEvent.click(more())
    expect(satellites()[0]!.getAttribute('aria-label')).toBe('Helper 0')
  })

  // The last page can hold fewer helpers, so the paging control changes its position while keeping its DOM node.
  describe('paging keeps one roving stop on the focused element', () => {
    const more = () => screen.getByRole('button', { name: /^Show helpers/ })
    const press = (key: string) => fireEvent.keyDown(document.activeElement!, { key })
    const expectFocusedRovingStop = () => expect(satellites().filter((s) => s.tabIndex === 0)).toEqual([document.activeElement])

    function focusPagingControl(n: number) {
      const view = renderOrbit(n)
      act(() => { hub().focus() })
      act(() => { satellites()[0]!.focus() })
      press('End')
      expect(document.activeElement).toBe(more())
      return view
    }

    function showLastPage(n: number) {
      for (let page = 1; page < Math.ceil(n / 3); page++) fireEvent.click(more())
    }

    it.each([5, 6, 7, 8])('%i helpers: arrows on the last page, then paging back to the first page', (n) => {
      focusPagingControl(n)
      const control = more()
      showLastPage(n)
      press('ArrowLeft')
      expect(document.activeElement).toBe(satellites().at(-2))
      expectFocusedRovingStop()
      press('ArrowRight')
      expect(document.activeElement).toBe(more())
      expectFocusedRovingStop()

      fireEvent.click(more())
      expect(satellites()[0]!.getAttribute('aria-label')).toBe('Helper 0')
      expect(more()).toBe(control)
      expect(document.activeElement).toBe(control)
      expectFocusedRovingStop()
      expect(control.getAttribute('aria-label')).toBe(`Show helpers ${n === 5 ? '4–5' : '4–6'} of ${n}`)
      expect(screen.getByRole('status').textContent).toBe(`Showing helpers 1 to 3 of ${n}`)

      press('ArrowRight')
      expect(document.activeElement).toBe(satellites()[0])
      expectFocusedRovingStop()
      press('ArrowLeft')
      expect(document.activeElement).toBe(control)
      expectFocusedRovingStop()
    })

    it.each([5, 6, 7, 8])('%i helpers: Home, End and both arrow axes follow the current page', (n) => {
      focusPagingControl(n)
      showLastPage(n)
      press('Home')
      expect(document.activeElement).toBe(satellites()[0])
      expectFocusedRovingStop()
      press('ArrowUp')
      expect(document.activeElement).toBe(more())
      expectFocusedRovingStop()
      press('ArrowDown')
      expect(document.activeElement).toBe(satellites()[0])
      press('End')
      expect(document.activeElement).toBe(more())

      fireEvent.click(more())
      press('ArrowLeft')
      expect(satellites().at(-2)!.getAttribute('aria-label')).toBe('Helper 2')
      expect(document.activeElement).toBe(satellites().at(-2))
      expectFocusedRovingStop()
    })

    it('a pointer press on the paging control moves focus there from the helper it replaces', () => {
      const { onSelect } = focusPagingControl(5)
      press('Home')
      const helper = document.activeElement
      expect(document.querySelector('.stage-orbit-tooltip')).not.toBeNull()

      const control = more()
      fireEvent.pointerDown(control, { pointerType: 'mouse' })
      expect(fireEvent.mouseDown(control)).toBe(false) // the layer keeps the browser from moving focus itself
      fireEvent.mouseUp(control)
      fireEvent.click(control)
      expect(helper?.isConnected).toBe(false)
      expect(document.activeElement).toBe(control)
      expectFocusedRovingStop()
      expect(document.querySelector('.stage-orbit-tooltip')).toBeNull()
      expect(onSelect).not.toHaveBeenCalled()
    })

    it('a pointer press on the paging control moves focus there from the hub', () => {
      renderOrbit(7)
      act(() => { hub().focus() })
      fireEvent.mouseDown(more())
      fireEvent.click(more())
      expect(satellites()[0]!.getAttribute('aria-label')).toBe('Helper 3')
      expect(document.activeElement).toBe(more())
      expectFocusedRovingStop()
    })

    it('Escape after paging resets the next keyboard opening to the first page and first helper', () => {
      focusPagingControl(7)
      showLastPage(7)
      press('Escape')
      expect(layer()).toBeNull()
      expect(document.activeElement).toBe(hub())
      act(() => { screen.getByRole('button', { name: 'row' }).focus() })
      act(() => { hub().focus() })
      expect(satellites()[0]!.getAttribute('aria-label')).toBe('Helper 0')
      expect(satellites().map((s) => s.tabIndex)).toEqual([0, -1, -1, -1])
    })

    it.each(['paging control', 'helper'])('falls back to the stage row when it unmounts with the %s focused', (target) => {
      const { rerender } = focusPagingControl(5)
      showLastPage(5)
      press('ArrowLeft')
      press('ArrowRight')
      fireEvent.click(more())
      if (target === 'helper') press('Home')
      rerender(
        <li className="stage-item" data-stage-id="s1">
          <button type="button" className="stage-row">row</button>
          <span className="stage-actions" />
        </li>,
      )
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'row' }))
    })
  })

  it('drops the stagger delay under reduced motion', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: query.includes('prefers-reduced-motion'), addEventListener: () => {}, removeEventListener: () => {},
    }))
    renderOrbit(3)
    fireEvent.click(hub())
    expect(layer()!.getAttribute('data-reduced-motion')).toBe('true')
    expect(satellites()[2]!.style.getPropertyValue('--orbit-delay')).toBe('')
    vi.unstubAllGlobals()
  })

  it('staggers satellites otherwise', () => {
    renderOrbit(3)
    fireEvent.click(hub())
    expect(satellites()[2]!.style.getPropertyValue('--orbit-delay')).toBe('80ms')
  })

  it('closes when the stage becomes selected, and on resize', () => {
    const { rerender } = renderOrbit(3)
    fireEvent.click(hub())
    expect(layer()).not.toBeNull()
    rerender(
      <li className="stage-item" data-stage-id="s1">
        <button type="button" className="stage-row">row</button>
        <span className="stage-actions"><StageOrbit stageId="s1" assistants={helpers(3)} selectedStageId="s1" /></span>
      </li>,
    )
    expect(layer()).toBeNull()
    fireEvent.click(hub())
    expect(layer()).not.toBeNull()
    fireEvent(window, new Event('resize'))
    expect(layer()).toBeNull()
  })

  it('wraps arrow navigation modulo the slot count; Home and End stay', () => {
    renderOrbit(4)
    act(() => { hub().focus() })
    const sats = satellites()
    act(() => { sats[0]!.focus() })
    fireEvent.keyDown(sats[0]!, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(sats[3])
    fireEvent.keyDown(sats[3]!, { key: 'ArrowRight' })
    expect(document.activeElement).toBe(sats[0])
    fireEvent.keyDown(sats[0]!, { key: 'ArrowUp' })
    expect(document.activeElement).toBe(sats[3])
    fireEvent.keyDown(sats[3]!, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(sats[0])
  })

  it('closes a pinned orbit when focus leaves the wrapper, but not when it moves inside', () => {
    renderOrbit(3)
    const outside = document.createElement('button')
    document.body.append(outside)
    fireEvent.click(hub())
    act(() => { satellites()[0]!.focus() })
    expect(layer()).not.toBeNull()
    act(() => { outside.focus() })
    expect(layer()).toBeNull()
    outside.remove()
  })

  it('closes when the selected stage changes to another stage', () => {
    const { rerender } = renderOrbit(3, 'other')
    fireEvent.click(hub())
    expect(layer()).not.toBeNull()
    rerender(
      <li className="stage-item" data-stage-id="s1">
        <button type="button" className="stage-row">row</button>
        <span className="stage-actions"><StageOrbit stageId="s1" assistants={helpers(3)} selectedStageId="third" /></span>
      </li>,
    )
    expect(layer()).toBeNull()
  })

  it('reopens on keyboard focus after a pointer press on the hub', () => {
    renderOrbit(3)
    const outside = document.createElement('button')
    document.body.append(outside)
    fireEvent.pointerDown(hub())
    act(() => { hub().focus() })
    expect(layer()).toBeNull()
    act(() => { outside.focus() })
    act(() => { hub().focus() }) // keyboard Tab-in: no pointerdown before it
    expect(layer()).not.toBeNull()
    outside.remove()
  })

  describe('scroll', () => {
    // jsdom has no layout: the layer rect must follow its fixed left/top, otherwise the position correction never settles.
    beforeEach(() => {
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
        if (!this.classList.contains('stage-orbit-layer')) return rectAt(0)
        const left = parseFloat(this.style.left) || 0
        const top = parseFloat(this.style.top) || 0
        return { ...rectAt(top), left, right: left, x: left }
      })
    })
    afterEach(() => vi.restoreAllMocks())
    const rectAt = (top: number, bottom = top + 20): DOMRect => ({ top, bottom, left: 0, right: 20, width: 20, height: 20, x: 0, y: top, toJSON: () => ({}) })

    it('repositions the layer while the hub stays visible and closes once it is clipped', () => {
      renderOrbit(3)
      const hubEl = hub()
      const spy = vi.spyOn(hubEl, 'getBoundingClientRect').mockReturnValue(rectAt(100))
      fireEvent.click(hubEl)
      const left = () => (layer() as HTMLElement).style.top
      const before = left()
      spy.mockReturnValue(rectAt(160))
      fireEvent.scroll(window)
      expect(layer()).not.toBeNull()
      expect(left()).not.toBe(before)
      spy.mockReturnValue(rectAt(-10, 10))
      fireEvent.scroll(window)
      expect(layer()).toBeNull()
    })

    it('closes when the hub leaves the scroll container clip', () => {
      const { container } = render(
        <div className="stages-scroll"><li className="stage-item"><span className="stage-actions"><StageOrbit stageId="s1" assistants={helpers(3)} /></span></li></div>,
      )
      const clip = container.querySelector('.stages-scroll')!
      vi.spyOn(clip, 'getBoundingClientRect').mockReturnValue(rectAt(50, 300))
      const hubEl = hub()
      const spy = vi.spyOn(hubEl, 'getBoundingClientRect').mockReturnValue(rectAt(100))
      fireEvent.click(hubEl)
      expect(layer()).not.toBeNull()
      spy.mockReturnValue(rectAt(40, 60))
      fireEvent.scroll(window)
      expect(layer()).toBeNull()
    })
  })

  it('moves focus to the stage row of the same item when it unmounts with focus inside', () => {
    const { rerender } = renderOrbit(3)
    act(() => { hub().focus() })
    rerender(
      <li className="stage-item" data-stage-id="s1">
        <button type="button" className="stage-row">row</button>
        <span className="stage-actions" />
      </li>,
    )
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'row' }))
  })

  it('does not touch focus when it unmounts without focus inside', () => {
    const { rerender } = renderOrbit(3)
    const outside = document.createElement('button')
    document.body.append(outside)
    outside.focus()
    rerender(<li className="stage-item" data-stage-id="s1"><button type="button" className="stage-row">row</button></li>)
    expect(document.activeElement).toBe(outside)
    outside.remove()
  })

  it('makes no network requests', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderOrbit(3)
    fireEvent.click(hub())
    fireEvent.click(satellites()[0]!)
    expect(fetchSpy).not.toHaveBeenCalled()
    fetchSpy.mockRestore()
  })
})
