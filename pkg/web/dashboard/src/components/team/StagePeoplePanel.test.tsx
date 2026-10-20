import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import { StagePeoplePanel } from './StagePeoplePanel'
import type { MeshParticipant } from '../../api/mesh-client'
import type { Stage } from '../../types'

function stage(id: string, status: Stage['status']): Stage {
  return {
    id, name: id, status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false,
    showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [],
  }
}

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', reachable: true, command: 'claude' },
  { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true },
]

function renderPanel(overrides: Partial<Parameters<typeof StagePeoplePanel>[0]> = {}) {
  const onPick = vi.fn()
  const onClose = vi.fn()
  const onAgentAdded = vi.fn()
  render(
    <StagePeoplePanel
      stageId="impl"
      participants={participants}
      stages={[stage('impl', 'running')]}
      profiles={[{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }]}
      open
      onClose={onClose}
      onPick={onPick}
      onAgentAdded={onAgentAdded}
      {...overrides}
    />,
  )
  return { onPick, onClose, onAgentAdded }
}

function mockFeedWidth(initialWidth: number) {
  let width = initialWidth
  const observers = new Set<{ callback: ResizeObserverCallback; targets: Set<Element> }>()
  vi.stubGlobal('ResizeObserver', class {
    private targets = new Set<Element>()
    private record: { callback: ResizeObserverCallback; targets: Set<Element> }
    constructor(callback: ResizeObserverCallback) {
      this.record = { callback, targets: this.targets }
      observers.add(this.record)
    }
    observe(target: Element) { this.targets.add(target) }
    unobserve(target: Element) { this.targets.delete(target) }
    disconnect() { observers.delete(this.record); this.targets.clear() }
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const measured = this.matches('.stage-feed-layout') ? width : 0
    return { x: 0, y: 0, left: 0, top: 0, right: measured, bottom: 0, width: measured, height: 0, toJSON: () => ({}) } as DOMRect
  })
  vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(function (this: Element) {
    return this.matches('.stage-feed-layout') ? width : 0
  })
  return {
    resize(nextWidth: number) {
      width = nextWidth
      act(() => {
        for (const observer of observers) {
          const entries = [...observer.targets].map((target) => ({ target, contentRect: target.getBoundingClientRect() } as ResizeObserverEntry))
          observer.callback(entries, {} as ResizeObserver)
        }
      })
    },
  }
}

function renderResizablePanel(open = false) {
  const props = {
    stageId: 'impl', participants, stages: [stage('impl', 'running')],
    profiles: [{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }],
    open, onClose: vi.fn(), onPick: vi.fn(), onAgentAdded: vi.fn(),
  }
  const view = render(
    <>
      <button type="button" className="stage-people-toggle" aria-label="Show people" aria-controls="stage-people">People</button>
      <div className="stage-feed-layout">
        <div className="feed-workspace" />
        <StagePeoplePanel {...props} />
      </div>
    </>,
  )
  return { ...view, layout: view.container.querySelector('.stage-feed-layout') as HTMLElement, props }
}

function peopleSeparator() {
  return screen.getByRole('separator', { name: 'Resize people panel' })
}

describe('StagePeoplePanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  })

  test('an added agent is handed to the owner right away, with its alias, before the roster poll has it', async () => {
    const added: MeshParticipant = { id: 'run:t9', kind: 'run_agent', scope: 'run', alias: 'qa-2', display_name: 'QA', reachable: true }
    const attachSpy = vi
      .spyOn(meshClient, 'attachAndSendMesh')
      .mockResolvedValue({ task_id: 't9', participant_id: 'run:t9', thread_id: 'mt-9', participant: added })
    const { onAgentAdded } = renderPanel()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent…' }))
    fireEvent.change(screen.getByPlaceholderText('What should this run agent do?'), { target: { value: 'review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent' }))
    await waitFor(() => expect(onAgentAdded).toHaveBeenCalledWith(added))
    // Its first conversation shows in this stage's feed.
    expect(attachSpy).toHaveBeenCalledWith(expect.objectContaining({ originStage: 'impl' }))
  })

  test('a pick addresses the person and closes the drawer', () => {
    const { onPick, onClose } = renderPanel()
    fireEvent.click(screen.getByText('QA reviewer'))
    expect(onPick).toHaveBeenCalledWith(participants[2])
    expect(onClose).toHaveBeenCalled()
  })

  test('the reachable side agent stays selectable in People', () => {
    const side: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'Side agent', reachable: true }
    const { onPick } = renderPanel({ participants: [...participants, side] })
    fireEvent.click(screen.getByText('Side agent', { selector: '.team-person-name' }))
    expect(onPick).toHaveBeenCalledWith(side)
  })

  test('without a composer the rows are disabled and a hint says when writing is possible', () => {
    renderPanel({ onPick: undefined })
    expect(screen.getByText('QA reviewer').closest('button')).toBeDisabled()
    expect(screen.getByText(/while the run is going/)).toBeInTheDocument()
  })

  test('"Add run agent…" opens the Team tab\'s Add run agent dialog', () => {
    renderPanel()
    fireEvent.click(screen.getByRole('button', { name: 'Add run agent…' }))
    expect(screen.getByRole('dialog', { name: 'Add run agent' })).toBeInTheDocument()
  })

  test('"Attach helper to stage…" sits next to it and opens the stage\'s helper dialog; without it there is no button', () => {
    const onAttachHelper = vi.fn()
    const { onClose } = renderPanel({ onAttachHelper })
    fireEvent.click(screen.getByRole('button', { name: 'Attach helper to stage…' }))
    expect(onAttachHelper).toHaveBeenCalledWith('impl')
    expect(onClose).toHaveBeenCalled()
  })

  test('without onAttachHelper there is no attach button', () => {
    renderPanel()
    expect(screen.queryByRole('button', { name: 'Attach helper to stage…' })).toBeNull()
  })

  test('the drawer closes on Escape and on its scrim', () => {
    const { onClose } = renderPanel()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Close people' }))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  test('the drawer takes the focus when opened, keeps Tab inside, and gives it back when closed', () => {
    vi.stubGlobal('matchMedia', () => ({
      matches: true, addEventListener: () => {}, removeEventListener: () => {},
    }))
    const props = {
      stageId: 'impl', participants, stages: [stage('impl', 'running')], onClose: vi.fn(), onPick: vi.fn(), onAgentAdded: vi.fn(),
      profiles: [{ id: 'qa', title: 'QA', command: 'claude', available: true, unavailableReason: '' }],
    }
    const { rerender } = render(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open={false} />
      </>,
    )
    const opener = screen.getByRole('button', { name: 'Show people' })
    opener.focus()
    rerender(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open />
      </>,
    )
    const drawer = document.getElementById('stage-people') as HTMLElement
    expect(drawer.contains(document.activeElement)).toBe(true)

    // Tab from the last control wraps to the first one; Shift+Tab from the first goes to the last.
    const focusables = [...drawer.querySelectorAll<HTMLElement>('button:not([disabled])')]
    focusables.at(-1)?.focus()
    fireEvent.keyDown(drawer, { key: 'Tab' })
    expect(document.activeElement).toBe(focusables[0])
    fireEvent.keyDown(drawer, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(focusables.at(-1))

    rerender(
      <>
        <button type="button">Show people</button>
        <StagePeoplePanel {...props} open={false} />
      </>,
    )
    expect(document.activeElement).toBe(opener)
  })

  test('the labeled vertical separator grows People when dragged left and persists its own width across remount', () => {
    mockFeedWidth(1200)
    localStorage.setItem('afm.teamRosterWidth', '400')
    const view = renderResizablePanel()
    const handle = peopleSeparator()
    expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    expect(handle).toHaveAttribute('aria-valuemin', '200')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
    expect(handle).toHaveAttribute('aria-valuenow', '280')

    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: 700 })
    fireEvent.mouseUp(document)
    expect(view.layout.style.getPropertyValue('--people-width')).toBe('380px')
    expect(handle).toHaveAttribute('aria-valuenow', '380')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('380')
    expect(localStorage.getItem('afm.teamRosterWidth')).toBe('400')

    view.unmount()
    const remount = renderResizablePanel()
    expect(remount.layout.style.getPropertyValue('--people-width')).toBe('380px')
    expect(peopleSeparator()).toHaveAttribute('aria-valuenow', '380')
  })

  test('ArrowLeft widens People, ArrowRight narrows it, and both directions clamp; double-click resets', () => {
    mockFeedWidth(1200)
    const { layout } = renderResizablePanel()
    const handle = peopleSeparator()
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '296')
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(handle).toHaveAttribute('aria-valuenow', '280')
    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: -1000 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '560')
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '560')
    fireEvent.doubleClick(handle)
    expect(layout.style.getPropertyValue('--people-width')).toBe('280px')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('280')
    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: 9999 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '200')
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(handle).toHaveAttribute('aria-valuenow', '200')
  })

  test('initial narrow container and subsequent rail resizing clamp only the rendered width', () => {
    localStorage.setItem('afm.peopleWidth', '500')
    const measured = mockFeedWidth(600)
    const { layout } = renderResizablePanel()
    const handle = peopleSeparator()
    expect(layout.style.getPropertyValue('--people-width')).toBe('280px')
    expect(handle).toHaveAttribute('aria-valuenow', '280')
    expect(handle).toHaveAttribute('aria-valuemax', '280')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('500')

    measured.resize(580)
    expect(handle).toHaveAttribute('aria-valuenow', '260')
    expect(handle).toHaveAttribute('aria-valuemax', '260')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('500')

    measured.resize(1000)
    expect(layout.style.getPropertyValue('--people-width')).toBe('500px')
    expect(handle).toHaveAttribute('aria-valuenow', '500')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
  })

  test('outward resize at a temporary clamp keeps the saved People preference', () => {
    localStorage.setItem('afm.peopleWidth', '500')
    const measured = mockFeedWidth(600)
    const { layout } = renderResizablePanel()
    const handle = peopleSeparator()
    expect(handle).toHaveAttribute('aria-valuenow', '280')

    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: 700 })
    fireEvent.mouseUp(document)
    expect(handle).toHaveAttribute('aria-valuenow', '280')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('500')

    measured.resize(1000)
    expect(layout.style.getPropertyValue('--people-width')).toBe('500px')

    measured.resize(600)
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(handle).toHaveAttribute('aria-valuenow', '264')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('264')
  })

  test('reset in a narrow container keeps the default preference for when room returns', () => {
    localStorage.setItem('afm.peopleWidth', '500')
    const measured = mockFeedWidth(580)
    const { layout } = renderResizablePanel()
    fireEvent.doubleClick(peopleSeparator())
    expect(layout.style.getPropertyValue('--people-width')).toBe('260px')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('280')
    measured.resize(1000)
    expect(layout.style.getPropertyValue('--people-width')).toBe('280px')
  })

  test('invalid saved width uses the default without breaking the separator', () => {
    localStorage.setItem('afm.peopleWidth', 'not-a-width')
    mockFeedWidth(1200)
    renderResizablePanel()
    expect(peopleSeparator()).toHaveAttribute('aria-valuenow', '280')
  })

  test('mouseup outside the handle and unmount both stop dragging and restore body styles', () => {
    mockFeedWidth(1200)
    const view = renderResizablePanel()
    const handle = peopleSeparator()
    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: 750 })
    fireEvent.mouseUp(document.body)
    expect(document.body.style.cursor).toBe('')
    expect(document.body.style.userSelect).toBe('')
    fireEvent.mouseMove(document, { clientX: 600 })
    expect(handle).toHaveAttribute('aria-valuenow', '330')

    fireEvent.mouseDown(handle, { clientX: 800 })
    expect(document.body.style.cursor).toBe('col-resize')
    view.unmount()
    expect(document.body.style.cursor).toBe('')
    expect(document.body.style.userSelect).toBe('')
    const savedWidth = localStorage.getItem('afm.peopleWidth')
    fireEvent.mouseMove(document, { clientX: 400 })
    fireEvent.mouseUp(document)
    expect(localStorage.getItem('afm.peopleWidth')).toBe(savedWidth)
  })

  test('window blur ends a drag when mouseup happened outside the browser', () => {
    mockFeedWidth(1200)
    renderResizablePanel()
    const handle = peopleSeparator()
    fireEvent.mouseDown(handle, { clientX: 800 })
    fireEvent.mouseMove(document, { clientX: 750 })
    expect(handle).toHaveAttribute('aria-valuenow', '330')
    fireEvent.blur(window)
    expect(document.body.style.cursor).toBe('')
    expect(document.body.style.userSelect).toBe('')
    fireEvent.mouseMove(document, { clientX: 600 })
    expect(handle).toHaveAttribute('aria-valuenow', '330')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('330')
  })

  test('focus moves to the People toggle when the viewport switches to drawer mode', () => {
    let mobile = false
    const listeners = new Set<(event: MediaQueryListEvent) => void>()
    vi.stubGlobal('matchMedia', (query: string) => ({
      media: query, get matches() { return mobile && query === '(max-width: 1180px)' }, onchange: null,
      addEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.delete(listener),
      addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    mockFeedWidth(1200)
    renderResizablePanel()
    const handle = peopleSeparator()
    handle.focus()
    mobile = true
    act(() => { for (const listener of listeners) listener({ matches: true } as MediaQueryListEvent) })
    expect(screen.getByRole('button', { name: 'Show people' })).toHaveFocus()
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    expect(handle).toHaveAttribute('tabindex', '-1')
  })

  test('focus moves to the People toggle when CSS hides the separator before the media listener runs', () => {
    let mobile = false
    const listeners = new Set<(event: MediaQueryListEvent) => void>()
    vi.stubGlobal('matchMedia', (query: string) => ({
      media: query, get matches() { return mobile && query === '(max-width: 1180px)' },
      addEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.delete(listener),
    }))
    mockFeedWidth(1200)
    renderResizablePanel()
    const handle = peopleSeparator() as HTMLElement
    handle.focus()
    handle.style.display = 'none'
    handle.blur() // Chrome drops focus before the matchMedia callback when display becomes none.
    expect(document.activeElement).toBe(document.body)

    mobile = true
    act(() => { for (const listener of listeners) listener({ matches: true } as MediaQueryListEvent) })
    expect(screen.getByRole('button', { name: 'Show people' })).toHaveFocus()
  })

  test('opening the drawer then widening the viewport restores the desktop separator and releases Tab', () => {
    let mobile = true
    const listeners = new Set<(event: MediaQueryListEvent) => void>()
    vi.stubGlobal('matchMedia', (query: string) => ({
      media: query, get matches() { return mobile && query === '(max-width: 1180px)' }, onchange: null,
      addEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => listeners.delete(listener),
      addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    mockFeedWidth(1200)
    renderResizablePanel(true)
    const handle = document.querySelector<HTMLElement>('.stage-people-resizer') as HTMLElement
    expect(handle).toHaveAttribute('aria-hidden', 'true')
    mobile = false
    act(() => { for (const listener of listeners) listener({ matches: false } as MediaQueryListEvent) })
    expect(handle).not.toHaveAttribute('aria-hidden')
    expect(handle).toHaveAttribute('tabindex', '0')
    handle.focus()
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '296')
    const panel = document.getElementById('stage-people') as HTMLElement
    const last = [...panel.querySelectorAll<HTMLElement>('button:not([disabled])')].at(-1) as HTMLElement
    last.focus()
    expect(fireEvent.keyDown(last, { key: 'Tab' })).toBe(true)
    expect(last).toHaveFocus()
  })
})
