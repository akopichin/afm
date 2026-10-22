import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import * as meshClient from '../../api/mesh-client'
import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../../api/mesh-client'
import meshCss from '../../../skins/base/mesh.css?raw'
import { RunConversationsPanel, runConversationThreads } from './RunConversationsPanel'

const user: MeshParticipant = { id: 'user', kind: 'user', scope: 'run', reachable: true }
const runAgent: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true }
const runHelper: MeshParticipant = { id: 'side:r1', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'Assistant', reachable: true }
const stageAgent: MeshParticipant = { id: 'stage:impl', kind: 'stage_agent', scope: 'stage', stage_id: 'impl', alias: 'impl', display_name: 'Implement', reachable: true }
const stageHelper: MeshParticipant = { id: 'task:t1', kind: 'helper', scope: 'run', stage_id: 'impl', alias: 'h1', display_name: 'Stage helper', reachable: true }
const people = [user, runAgent, runHelper, stageAgent, stageHelper]

const thread = (id: string, members: string[], extra: Partial<MeshThread> = {}): MeshThread => ({ id, scope: 'run', members, status: 'open', ...extra })
const summary = (seq: number, preview: string, sender = 'run:qa'): MeshThreadSummaries[string] => ({
  last_message: { id: `m${seq}`, seq, created_at: '2026-10-22T10:00:00Z', sender_id: sender, preview },
  last_other_message_seq: seq,
  title_seq: 0,
})

describe('runConversationThreads', () => {
  const ids = (threads: MeshThread[]) => runConversationThreads(threads, people).map((t) => t.id)

  test('keeps a run agent direct thread and a run-scoped helper thread with the human', () => {
    expect(ids([thread('a', ['user', 'run:qa']), thread('b', ['user', 'side:r1'])])).toEqual(['a', 'b'])
  })

  test('hides a stage agent thread and a stage helper thread (a member has a stage_id)', () => {
    expect(ids([thread('a', ['user', 'stage:impl']), thread('b', ['user', 'task:t1']), thread('c', ['user', 'run:qa', 'stage:impl'])])).toEqual([])
  })

  test('hides a thread opened from a stage even when its members are run-wide', () => {
    expect(ids([thread('a', ['user', 'run:qa'], { origin_stage: 'impl' })])).toEqual([])
  })

  test('hides an agent-only thread without the human', () => {
    expect(ids([thread('a', ['run:qa', 'side:r1'])])).toEqual([])
  })

  test('defers a thread that names a participant the roster does not know yet, instead of showing an unresolved name', () => {
    expect(ids([thread('a', ['user', 'run:new'])])).toEqual([])
    expect(runConversationThreads([thread('a', ['user', 'run:new'])], [...people, { ...runAgent, id: 'run:new' }]).map((t) => t.id)).toEqual(['a'])
  })

  test('keeps a closed thread (it stays readable)', () => {
    expect(ids([thread('a', ['user', 'run:qa'], { status: 'closed' })])).toEqual(['a'])
  })
})

function panelUi(props: Parameters<typeof RunConversationsPanel>[0]) {
  return (
    <>
      <button type="button" className="run-conversations-toggle" aria-controls="run-conversations">Run conversations</button>
      <div className="split"><RunConversationsPanel {...props} /></div>
    </>
  )
}

function renderPanel(overrides: Partial<Parameters<typeof RunConversationsPanel>[0]> = {}) {
  const onClose = vi.fn()
  const props = {
    open: true,
    onClose,
    participants: people,
    threads: [thread('old', ['user', 'run:qa'], { title: 'Old topic' }), thread('new', ['user', 'side:r1'], { title: 'New topic' })],
    summaries: { old: summary(3, 'first'), new: summary(9, 'latest') } as MeshThreadSummaries,
    ...overrides,
  }
  const view = render(panelUi(props))
  const update = (patch: Partial<typeof props>) => view.rerender(panelUi({ ...props, ...patch }))
  return { ...view, onClose, props, update, root: () => document.getElementById('run-conversations') as HTMLElement }
}

function stubNarrow(matches: boolean) {
  const listeners = new Set<() => void>()
  const query = { matches, addEventListener: (_: string, l: () => void) => listeners.add(l), removeEventListener: (_: string, l: () => void) => listeners.delete(l) }
  vi.stubGlobal('matchMedia', () => query)
  return (next: boolean) => {
    query.matches = next
    act(() => listeners.forEach((l) => l()))
  }
}

function stubSplitWidth(width: number) {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const measured = this.matches('.split') ? width : 0
    return { x: 0, y: 0, left: 0, top: 0, right: measured, bottom: 0, width: measured, height: 0, toJSON: () => ({}) } as DOMRect
  })
}

describe('RunConversationsPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  test('lists the run conversations, the one with the latest message first', () => {
    renderPanel()
    const rows = screen.getAllByRole('button', { pressed: false })
    expect(rows.map((r) => within(r).getByText(/topic/).textContent)).toEqual(['New topic', 'Old topic'])
  })

  test('only run conversations are listed: stage and agent-only threads are left out', () => {
    renderPanel({ threads: [thread('keep', ['user', 'run:qa'], { title: 'Keep' }), thread('stage', ['user', 'stage:impl'], { title: 'Stage talk' }), thread('mesh', ['run:qa', 'side:r1'], { title: 'Mesh talk' })] })
    expect(screen.getByText('Keep')).toBeInTheDocument()
    expect(screen.queryByText('Stage talk')).toBeNull()
    expect(screen.queryByText('Mesh talk')).toBeNull()
  })

  test('explains the empty state', () => {
    renderPanel({ threads: [], summaries: {} })
    expect(screen.getByText(/No run conversations yet/)).toBeInTheDocument()
  })

  test('list -> history -> Back, and the history is read from the thread', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({
      messages: [{ id: 'm1', thread_id: 'new', sender_id: 'side:r1', recipient_ids: ['user'], kind: 'message', body: 'Need your call', seq: 9, created_at: '2026-10-22T10:00:00Z' }],
      next_cursor: 9,
    })
    renderPanel()
    fireEvent.click(screen.getByText('New topic'))
    expect(await screen.findByText('Need your call')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Back to conversations' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(screen.getByText('Old topic')).toBeInTheDocument()
    expect(screen.queryByText('Need your call')).toBeNull()
  })

  test('an answer is sent to the conversation members', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'mm-1', thread_id: 'old', seq: 10 } as meshClient.MeshSendReceipt)
    renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    fireEvent.change(screen.getByPlaceholderText(/^Message /), { target: { value: 'Ship it' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(send).toHaveBeenCalledWith('old', expect.objectContaining({ recipientIds: ['run:qa'], body: 'Ship it' })))
  })

  test('the typed draft survives Back and re-opening the same conversation', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await act(async () => {})
    fireEvent.change(screen.getByPlaceholderText(/^Message /), { target: { value: 'half-written' } })
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    fireEvent.click(screen.getByText('Old topic'))
    await act(async () => {})
    expect(screen.getByPlaceholderText(/^Message /)).toHaveValue('half-written')
  })

  test('the open conversation stays open while the panel is closed and re-opened', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const { update } = renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await act(async () => {})
    fireEvent.change(screen.getByPlaceholderText(/^Message /), { target: { value: 'keep me' } })
    update({ open: false })
    update({ open: true })
    expect(screen.getByPlaceholderText(/^Message /)).toHaveValue('keep me')
  })

  test('a conversation that stops being a run conversation returns to the list', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const { update, props } = renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await act(async () => {})
    update({ threads: [props.threads[1]!] })
    expect(screen.queryByPlaceholderText(/^Message /)).toBeNull()
    expect(screen.getByText('New topic')).toBeInTheDocument()
  })

  test('closed: the root is inert and exposes no tab stops; open: it is interactive', () => {
    const { update, root } = renderPanel({ open: false })
    expect(root().inert).toBe(true)
    update({ open: true })
    expect(root().inert).toBe(false)
  })

  test('opening focuses the first row, never the separator', () => {
    const { update } = renderPanel({ open: false })
    update({ open: true })
    expect(document.activeElement).toBe(screen.getByText('New topic').closest('button'))
    expect(document.activeElement).not.toBe(screen.getByRole('separator'))
  })

  test('opening focuses the first unread row when there is one', () => {
    const { update } = renderPanel({ open: false, unreadThreadIds: new Set(['old']) })
    update({ open: true })
    expect(document.activeElement).toBe(screen.getByText('Old topic').closest('button'))
  })

  test('opening an empty panel focuses its heading', () => {
    renderPanel({ threads: [], summaries: {} })
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Run conversations' }))
  })

  test('Escape inside the desktop panel closes it; Escape elsewhere does not', () => {
    const { onClose } = renderPanel()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.keyDown(screen.getByText('New topic'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  test('closing hands the focus back to the opener', () => {
    const { update } = renderPanel()
    expect(document.activeElement).toBe(screen.getByText('New topic').closest('button'))
    update({ open: false })
    expect(document.activeElement).toBe(document.querySelector('.run-conversations-toggle'))
  })

  test('the drawer form: dialog semantics, scrim closes, Escape closes anywhere, Tab stays inside', () => {
    stubNarrow(true)
    const { onClose, root } = renderPanel()
    expect(root()).toHaveAttribute('role', 'dialog')
    expect(root()).toHaveAttribute('aria-modal', 'true')
    expect(document.activeElement && root().contains(document.activeElement)).toBe(true)
    // The separator is not reachable in the drawer.
    expect(screen.getByRole('separator', { hidden: true })).toHaveAttribute('tabindex', '-1')

    const focusables = [...root().querySelectorAll<HTMLElement>('button:not([disabled])')]
    focusables.at(-1)?.focus()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(document.activeElement).toBe(focusables[0])

    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(document.querySelector('.run-conversations-scrim') as HTMLElement)
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  test('an empty drawer still traps Tab and Shift+Tab at its heading', () => {
    stubNarrow(true)
    const { root } = renderPanel({ threads: [], summaries: {} })
    const heading = screen.getByRole('heading', { name: 'Run conversations' })
    expect(document.activeElement).toBe(heading)
    const closeButton = screen.getByRole('button', { name: 'Close' })

    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(closeButton)
    expect(root().contains(document.activeElement)).toBe(true)

    heading.focus()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(document.activeElement).toBe(closeButton)
  })

  test('the drawer keeps its focus trap when it swaps the list for a conversation and back', async () => {
    stubNarrow(true)
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    const { root } = renderPanel()
    const insideDrawer = () => document.activeElement !== null && document.activeElement !== document.body && root().contains(document.activeElement)
    const wrapsFromLast = () => {
      const focusables = [...root().querySelectorAll<HTMLElement>('button:not([disabled]), textarea')]
      focusables.at(-1)?.focus()
      fireEvent.keyDown(document, { key: 'Tab' })
      expect(document.activeElement).toBe(focusables[0])
    }

    fireEvent.click(screen.getByText('New topic'))
    await screen.findByPlaceholderText(/Message/)
    // The clicked row is gone: the focus moved on to the conversation's Back button, not to the page.
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(insideDrawer()).toBe(true)
    wrapsFromLast()

    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    // Back lands on the row the human came from.
    expect(document.activeElement).toBe(screen.getByText('New topic').closest('button'))
    expect(insideDrawer()).toBe(true)
    wrapsFromLast()
  })

  test('on the desktop panel the same swap also keeps the focus inside', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findByPlaceholderText(/Message/)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Back to conversations' }))
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(document.activeElement).toBe(screen.getByText('Old topic').closest('button'))
  })

  test('the drawer has no scrim and no dialog role while closed', () => {
    stubNarrow(true)
    const { root } = renderPanel({ open: false })
    expect(document.querySelector('.run-conversations-scrim')).toBeNull()
    expect(root()).not.toHaveAttribute('role', 'dialog')
    expect(root().inert).toBe(true)
  })

  test('opening the drawer focuses its first row and closing returns focus to the opener', () => {
    stubNarrow(true)
    const { update } = renderPanel({ open: false })
    const opener = document.querySelector('.run-conversations-toggle') as HTMLElement
    opener.focus()
    update({ open: true })
    expect(document.activeElement).toBe(screen.getByText('New topic').closest('button'))
    update({ open: false })
    expect(document.activeElement).toBe(opener)
  })

  test('opening clears inert before the drawer tries to focus a control', () => {
    stubNarrow(true)
    const { update, root } = renderPanel({ open: false })
    const focus = HTMLElement.prototype.focus
    const inertAtFocus: boolean[] = []
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement) {
      if (root().contains(this)) inertAtFocus.push(root().inert)
      focus.call(this)
    })

    update({ open: true })
    expect(inertAtFocus[0]).toBe(false)
    expect(root().contains(document.activeElement)).toBe(true)
  })

  test('going from an open desktop panel to a narrow viewport moves focus into the drawer', () => {
    const setNarrow = stubNarrow(false)
    const { root } = renderPanel({ open: true })
    ;(document.querySelector('.run-conversations-toggle') as HTMLElement).focus()
    setNarrow(true)
    expect(document.activeElement && root().contains(document.activeElement)).toBe(true)
  })

  test('the separator resizes with its own saved width, ArrowLeft widens, and the content keeps 320px', () => {
    stubSplitWidth(900)
    localStorage.setItem('afm.peopleWidth', '500')
    renderPanel()
    const handle = screen.getByRole('separator', { name: 'Resize run conversations' })
    expect(handle).toHaveAttribute('aria-valuenow', '340')
    expect(document.querySelector('.split')).toHaveStyle({ '--run-conversations-width': '340px' })
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(handle).toHaveAttribute('aria-valuenow', '356')
    expect(localStorage.getItem('afm.runConversationsWidth')).toBe('356')
    expect(localStorage.getItem('afm.peopleWidth')).toBe('500')
    fireEvent.mouseDown(handle, { clientX: 600 })
    fireEvent.mouseMove(document, { clientX: -2000 })
    fireEvent.mouseUp(document)
    // 900px split minus the 320px the primary content keeps.
    expect(handle).toHaveAttribute('aria-valuenow', '560')
    expect(handle).toHaveAttribute('aria-valuemax', '560')
  })

  test('a narrow split clamps the rendered width so 320px stay for the content', () => {
    stubSplitWidth(600)
    renderPanel()
    expect(screen.getByRole('separator', { name: 'Resize run conversations' })).toHaveAttribute('aria-valuenow', '280')
  })
})

describe('RunConversationsPanel unread signal and linked replies', () => {
  const question = (id: string, seq: number) => ({ id, thread_id: 'old', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'request', body: `Question ${id}`, seq, created_at: '2026-10-22T10:00:00Z' })
  const chip = () => document.querySelector('.feed-reply-chip--conversation')

  type Observer = { elements: Element[]; callback: IntersectionObserverCallback; disconnected: boolean }
  let observers: Observer[] = []
  function stubObserver() {
    observers = []
    vi.stubGlobal('IntersectionObserver', class {
      record: Observer
      constructor(callback: IntersectionObserverCallback) {
        this.record = { elements: [], callback, disconnected: false }
        observers.push(this.record)
      }
      observe(el: Element) { this.record.elements.push(el) }
      unobserve() {}
      disconnect() { this.record.disconnected = true }
      takeRecords() { return [] }
    })
  }
  const live = () => observers.filter((o) => !o.disconnected)
  function see(id: string) {
    const el = document.querySelector(`[data-message-id="${id}"]`) as HTMLElement
    const entry = { target: el, isIntersecting: true, intersectionRatio: 1, intersectionRect: { height: 100 }, boundingClientRect: { height: 100 }, rootBounds: { height: 400 } } as unknown as IntersectionObserverEntry
    act(() => live().filter((o) => o.elements.includes(el)).forEach((o) => o.callback([entry], {} as IntersectionObserver)))
  }

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  test('an unread conversation row is bright and named for assistive technology; the others are not', () => {
    renderPanel({ unreadThreadIds: new Set(['old']) })
    const old = screen.getByText('Old topic').closest('button') as HTMLElement
    expect(old).toHaveClass('unread')
    expect(within(old).getByText('New messages')).toBeInTheDocument()
    expect(screen.getByText('New topic').closest('button')).not.toHaveClass('unread')
  })

  test('Back shows the unread mark of the other conversations', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [], next_cursor: 0 })
    renderPanel({ unreadThreadIds: new Set(['old']) })
    fireEvent.click(screen.getByText('New topic'))
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    expect(screen.getByText('Old topic').closest('button')).toHaveClass('unread')
  })

  test('the conversation reports a seen agent message through onSeen with its thread id', async () => {
    stubObserver()
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question('q1', 7)], next_cursor: 7 })
    const onSeen = vi.fn()
    renderPanel({ onSeen, seenSeqs: { old: 2 } })
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findAllByText('Question q1')
    see('q1')
    expect(onSeen).toHaveBeenCalledWith('old', 7)
  })

  test('a message behind the stored cursor is not tracked', async () => {
    stubObserver()
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question('q1', 7)], next_cursor: 7 })
    renderPanel({ onSeen: vi.fn(), seenSeqs: { old: 7 } })
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findAllByText('Question q1')
    expect(live().flatMap((o) => o.elements)).toEqual([])
  })

  test('a closed panel tracks nothing and does not accent; opening starts both', async () => {
    stubObserver()
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question('q1', 7)], next_cursor: 7 })
    const onSeen = vi.fn()
    const { update } = renderPanel({ onSeen })
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findAllByText('Question q1')
    update({ open: false })
    expect(live().flatMap((o) => o.elements)).toEqual([])
    update({ open: true })
    expect(live().flatMap((o) => o.elements)).toHaveLength(1)
    // Bright only once the bubble is actually on screen.
    expect(document.querySelector('.is-new-agent-message')).toBeNull()
    const bubble = live().flatMap((o) => o.elements)[0]!
    const onScreen = { target: bubble, isIntersecting: true, intersectionRect: { height: 100 }, boundingClientRect: { height: 100 }, rootBounds: { height: 400 } } as unknown as IntersectionObserverEntry
    act(() => live().forEach((o) => o.callback([onScreen], {} as IntersectionObserver)))
    expect(document.querySelector('.is-new-agent-message')).not.toBeNull()
  })

  test('the reply target and the draft survive Back, a closed panel and re-opening the conversation', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question('q1', 7)], next_cursor: 7 })
    const send = vi.spyOn(meshClient, 'sendMeshMessage').mockResolvedValue({ message_id: 'mm-1', thread_id: 'old', seq: 8 } as meshClient.MeshSendReceipt)
    const { update } = renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await waitFor(() => expect(chip()).toHaveTextContent('Question q1'))
    fireEvent.change(screen.getByPlaceholderText(/^Message /), { target: { value: 'my answer' } })
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    update({ open: false })
    update({ open: true })
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findAllByText('Question q1')
    expect(chip()).toHaveTextContent('Question q1')
    expect(screen.getByPlaceholderText(/^Message /)).toHaveValue('my answer')
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(send).toHaveBeenCalledWith('old', expect.objectContaining({ kind: 'reply', inReplyTo: 'q1', body: 'my answer' })))
  })

  test('a cancelled target stays cancelled after Back and re-opening', async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockResolvedValue({ messages: [question('q1', 7)], next_cursor: 7 })
    renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await waitFor(() => expect(chip()).not.toBeNull())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel reply in conversation' }))
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    fireEvent.click(screen.getByText('Old topic'))
    await screen.findAllByText('Question q1')
    expect(chip()).toBeNull()
  })

  test("another conversation's target is its own", async () => {
    vi.spyOn(meshClient, 'getMeshThread').mockImplementation(async (id) => ({ messages: id === 'old' ? [question('q1', 7)] : [], next_cursor: 7 }))
    renderPanel()
    fireEvent.click(screen.getByText('Old topic'))
    await waitFor(() => expect(chip()).not.toBeNull())
    fireEvent.click(screen.getByRole('button', { name: 'Back to conversations' }))
    fireEvent.click(screen.getByText('New topic'))
    await act(async () => {})
    expect(chip()).toBeNull()
  })
})

describe('new agent message signal styles', () => {
  const rule = (selector: string): string => meshCss.match(new RegExp(`${selector.replace(/[.\\[\]]/g, '\\$&')}\\s*\\{([^}]*)\\}`))?.[1] ?? ''
  // The reduced-motion block that names the pulsing opener (each block ends at its closing `}` line).
  const reducedMotion = meshCss.split('@media (prefers-reduced-motion: reduce)').find((part) => part.trimStart().startsWith('{\n  .run-conversations-toggle.is-pulsing'))?.split('\n}')[0] ?? ''

  test('the closed opener and the new bubble animate, and a bright row marks unread threads', () => {
    expect(rule('.run-conversations-toggle.is-pulsing')).toMatch(/animation:/)
    expect(rule('.team-conversation-message.is-new-agent-message .team-conversation-message-body')).toMatch(/animation:/)
    expect(rule('.team-thread-item.unread')).toMatch(/background:/)
    expect(meshCss).toMatch(/@keyframes runConversationsPulse/)
    expect(meshCss).toMatch(/@keyframes newAgentMessage/)
  })

  test('the new bubble is bright without the animation, so reduced motion still shows it', () => {
    expect(rule('.team-conversation-message.is-new-agent-message .team-conversation-message-body')).toMatch(/background:/)
    expect(reducedMotion).toMatch(/\.team-conversation-message\.is-new-agent-message \.team-conversation-message-body/)
    expect(reducedMotion).toMatch(/animation:\s*none/)
    expect(reducedMotion).not.toMatch(/background:\s*none|background:\s*transparent/)
  })

  test('the opener count has its own visible badge', () => {
    expect(rule('.run-conversations-toggle-count')).toMatch(/border-radius:/)
  })
})
