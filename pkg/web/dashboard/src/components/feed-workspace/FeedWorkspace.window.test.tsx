import { render, screen, fireEvent, act } from '@testing-library/react'
import MarkdownIt from 'markdown-it'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import type { MeshMessage } from '../../api/mesh-client'
import { FeedWorkspace } from './FeedWorkspace'
import { resetUnknownCommandLabels } from '../../lib/executor-label'
import { EventBuffer } from '../../hooks/use-event-feed/event-buffer'

const at = (n: number): string => new Date(Date.UTC(2026, 0, 1, 10, 0, 0) + n * 1000).toISOString()
const event = (type: string, payload: unknown, n: number, stageId = 's1'): AfmEvent => ({ type, payload, stageId, timestamp: at(n), seq: n }) as AfmEvent
const board = (n: number): AfmEvent => event('mesh_board', { key: `k${n}`, author_id: 'a', status: 'ready' }, n)
const tool = (n: number): AfmEvent => event('agent_action', { tool: 'Read', detail: `file${n}` }, n)
const text = (n: number, detail = `message ${n}`): AfmEvent => event('agent_action', { tool: 'text', detail }, n)
const range = (from: number, to: number): number[] => Array.from({ length: to - from }, (_, i) => from + i)

function scrollFeed(container: HTMLElement, scrollTop: number): HTMLElement {
  const scroll = container.querySelector('.feed-scroll') as HTMLElement
  Object.defineProperties(scroll, { scrollHeight: { value: 5000, configurable: true }, clientHeight: { value: 200, configurable: true } })
  scroll.scrollTop = scrollTop
  fireEvent.scroll(scroll)
  return scroll
}

describe('FeedWorkspace row window', () => {
  beforeEach(() => resetUnknownCommandLabels())

  it('a feed of 400 messages mounts only the last 150 and offers Show earlier', () => {
    const { container } = render(<FeedWorkspace events={range(1, 401).map((n) => text(n))} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(150)
    expect(screen.getByRole('button', { name: 'Show earlier (250)' })).toBeTruthy()
    expect(container.textContent).toContain('message 400')
    expect(container.querySelector('.feed-item-text')?.textContent?.trim()).toBe('message 251')
  })

  it('one group of 400 board rows renders 150 rows under one header and expands by 150', () => {
    const { container } = render(<FeedWorkspace events={range(1, 401).map(board)} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(1)
    expect(container.querySelectorAll('.feed-item')).toHaveLength(150)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (250)' }))
    expect(container.querySelectorAll('.feed-group')).toHaveLength(1)
    expect(container.querySelectorAll('.feed-item')).toHaveLength(300)
    const first = container.querySelector('.feed-item .feed-item-text')
    expect(first?.textContent).toContain('k101')
    expect(screen.getByRole('button', { name: 'Show earlier (100)' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (100)' }))
    expect(container.querySelectorAll('.feed-item')).toHaveLength(400)
    expect(screen.queryByRole('button', { name: /Show earlier/ })).toBeNull()
  })

  it('a long script output group is limited the same way', () => {
    const events = range(1, 301).map((n) => event('script_output', { stream: 'stdout', hook: 'run', line: `line${n}` }, n))
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    expect(container.querySelectorAll('.feed-item')).toHaveLength(150)
    expect(screen.getByRole('button', { name: 'Show earlier (150)' })).toBeTruthy()
  })

  it('keeps the start of the window while the reader is away from the bottom and shrinks it on return', () => {
    const events = range(1, 301).map((n) => text(n))
    const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
    scrollFeed(container, 100)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (150)' }))
    expect(container.querySelectorAll('.feed-group')).toHaveLength(300)
    rerender(<FeedWorkspace events={[...events, text(301), text(302)]} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(302)
    expect(container.querySelector('.feed-item-text')?.textContent?.trim()).toBe('message 1')
    fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
    expect(container.querySelectorAll('.feed-group')).toHaveLength(150)
  })

  describe('unseen count', () => {
    const badge = (container: HTMLElement): string | undefined => container.querySelector('.jump-latest-count')?.textContent ?? undefined
    // The list is capped: the window slides while the arrival counter keeps growing.
    const capped = (from: number): AfmEvent[] => range(from, from + 400).map((n) => text(n))
    const view = (events: AfmEvent[], arrivals: number, stageId: string | null = 's1') => <FeedWorkspace events={events} stageId={stageId} arrivals={arrivals} />

    it('follows accepted arrivals while a capped window slides, and never decreases', () => {
      const { container, rerender } = render(view(capped(1), 10))
      scrollFeed(container, 100)
      expect(badge(container)).toBeUndefined()
      rerender(view(capped(5), 14))
      expect(badge(container)).toBe('4')
      rerender(view(capped(6), 15))
      expect(badge(container)).toBe('5')
      rerender(view(capped(6), 15))
      expect(badge(container)).toBe('5')
      fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
      expect(badge(container)).toBeUndefined()
    })

    it('does not depend on where new events land in the list', () => {
      const events = range(1, 11).map((n) => text(n))
      const { container, rerender } = render(view(events, 10))
      scrollFeed(container, 100)
      // Two arrivals inserted before the tail (as a history merge does) still count as two.
      rerender(view([...events.slice(0, 9), text(11), text(12), events[9] as AfmEvent], 12))
      expect(badge(container)).toBe('2')
    })

    it('ignores a list change that is not an arrival (history replay)', () => {
      const { container, rerender } = render(view(range(1, 11).map((n) => text(n)), 10))
      scrollFeed(container, 100)
      rerender(view(range(1, 16).map((n) => text(n)), 10))
      expect(badge(container)).toBeUndefined()
    })

    it('restarts from the new release point after returning to the bottom', () => {
      const { container, rerender } = render(view(capped(1), 10))
      scrollFeed(container, 100)
      rerender(view(capped(4), 13))
      expect(badge(container)).toBe('3')
      fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
      scrollFeed(container, 100)
      expect(badge(container)).toBeUndefined()
      rerender(view(capped(6), 15))
      expect(badge(container)).toBe('2')
    })

    it('restarts on a stage switch', () => {
      const { container, rerender } = render(view(capped(1), 10))
      scrollFeed(container, 100)
      rerender(view(capped(4), 13))
      expect(badge(container)).toBe('3')
      rerender(view(capped(1), 50, 's2'))
      expect(badge(container)).toBeUndefined()
      rerender(view(capped(2), 51, 's2'))
      expect(badge(container)).toBe('1')
    })

    it('counts arrivals in the Activity feed, which has no stage', () => {
      const { container, rerender } = render(view(range(1, 11).map(board), 10, null))
      scrollFeed(container, 100)
      rerender(view(range(1, 14).map(board), 13, null))
      expect(badge(container)).toBe('3')
    })

    // Cases driven by a real EventBuffer: the counter and the list come from the same pipeline the app uses.
    describe('with an EventBuffer', () => {
      const frame = (buffer: EventBuffer, events: AfmEvent[]) => <FeedWorkspace events={events} stageId="s1" arrivals={buffer.arrivals().stages.get('s1') ?? 0} />
      const accept = (buffer: EventBuffer, ns: number[]): AfmEvent[] => {
        ns.forEach((n) => buffer.push(text(n)))
        return buffer.flush() as AfmEvent[]
      }
      const seqless = (n: number, timestamp = at(n)): AfmEvent => ({ ...text(n), seq: undefined, timestamp }) as AfmEvent

      it('counts 4 then 5 when a cap-4 window slides [1..4] -> [5..8] -> [6..9]', () => {
        const buffer = new EventBuffer(4)
        const { container, rerender } = render(frame(buffer, accept(buffer, [1, 2, 3, 4])))
        scrollFeed(container, 100)
        rerender(frame(buffer, accept(buffer, [5, 6, 7, 8])))
        expect(badge(container)).toBe('4')
        rerender(frame(buffer, accept(buffer, [9])))
        expect(badge(container)).toBe('5')
      })

      it('counts a hidden ask_user that evicts the oldest event: [5..8] -> [6,7,8,ask_user] is 4 then 5', () => {
        const buffer = new EventBuffer(4)
        const { container, rerender } = render(frame(buffer, accept(buffer, [1, 2, 3, 4])))
        scrollFeed(container, 100)
        rerender(frame(buffer, accept(buffer, [5, 6, 7, 8])))
        expect(buffer.arrivals().stages.get('s1')).toBe(8)
        expect(badge(container)).toBe('4')
        buffer.push(event('ask_user', { question: 'q' }, 9))
        const slid = buffer.flush() as AfmEvent[]
        expect(slid.map((e) => e.type)).toEqual(['agent_action', 'agent_action', 'agent_action', 'ask_user'])
        expect(slid.map((e) => e.seq)).toEqual([6, 7, 8, 9])
        rerender(frame(buffer, slid))
        expect(buffer.arrivals().stages.get('s1')).toBe(9)
        expect(badge(container)).toBe('5')
      })

      it('counts hidden events too, so the number never goes down', () => {
        const buffer = new EventBuffer(4)
        const { container, rerender } = render(frame(buffer, accept(buffer, [1, 2, 3, 4])))
        scrollFeed(container, 100)
        buffer.push(event('ask_user', { question: 'q' }, 5))
        rerender(frame(buffer, buffer.flush() as AfmEvent[]))
        const afterHidden = Number(badge(container) ?? 0)
        rerender(frame(buffer, accept(buffer, [6])))
        expect(Number(badge(container))).toBe(afterHidden + 1)
      })

      it('a history replay that replaces a live event timestamp is not an arrival', () => {
        const buffer = new EventBuffer(4)
        buffer.push(seqless(1))
        const { container, rerender } = render(frame(buffer, buffer.flush() as AfmEvent[]))
        scrollFeed(container, 100)
        rerender(frame(buffer, buffer.merge([seqless(1, '2025-01-01T00:00:00.000Z')]).events))
        expect(buffer.arrivals().stages.get('s1')).toBe(1)
        expect(badge(container)).toBeUndefined()
      })

      it('same-timestamp events that eviction renumbers count once each', () => {
        const buffer = new EventBuffer(4)
        const sameTime = (n: number): AfmEvent => seqless(n, at(0))
        ;[1, 2, 3, 4].forEach((n) => buffer.push(sameTime(n)))
        const { container, rerender } = render(frame(buffer, buffer.flush() as AfmEvent[]))
        scrollFeed(container, 100)
        expect(buffer.arrivals().stages.get('s1')).toBe(4)
        buffer.push(sameTime(5))
        rerender(frame(buffer, buffer.flush() as AfmEvent[]))
        expect(buffer.arrivals().stages.get('s1')).toBe(5)
        expect(badge(container)).toBe('1')
      })

      it('scrolling back to the bottom resets; the next release counts only later arrivals', () => {
        const buffer = new EventBuffer(4)
        const { container, rerender } = render(frame(buffer, accept(buffer, [1])))
        scrollFeed(container, 100)
        rerender(frame(buffer, accept(buffer, [2])))
        expect(badge(container)).toBe('1')
        scrollFeed(container, 4800)
        expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
        rerender(frame(buffer, accept(buffer, [3])))
        scrollFeed(container, 100)
        expect(badge(container)).toBeUndefined()
        rerender(frame(buffer, accept(buffer, [4])))
        expect(badge(container)).toBe('1')
      })
    })

    it('the accessible label keeps growing past 99 while the visible badge stays 99+', () => {
      const { container, rerender } = render(view(capped(1), 200))
      scrollFeed(container, 100)
      rerender(view(capped(201), 400))
      expect(badge(container)).toBe('99+')
      expect(screen.getByRole('button', { name: 'Jump to latest, 200 new' })).toBeTruthy()
      rerender(view(capped(401), 600))
      expect(screen.getByRole('button', { name: 'Jump to latest, 400 new' })).toBeTruthy()
    })

    it('counts conversation messages by their arrivals, not by list length at the cap', async () => {
      const message = (n: number): MeshMessage => ({ id: `m${n}`, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `body ${n}`, seq: n, created_at: at(n) })
      const conversation = (arrived: number, from: number) => ({ messages: range(from, from + 2000).map(message), threads: {}, arrived })
      const spy = vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation')
      spy.mockReturnValue(conversation(0, 1) as never)
      try {
        const mesh = { roster: { enabled: true, probed: true, participants: [], threads: [] }, profiles: [], stages: [], pick: null }
        const events = range(1, 4).map((n) => text(n))
        const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" arrivals={3} mesh={mesh} />)
        scrollFeed(container, 100)
        spy.mockReturnValue(conversation(1, 2) as never)
        rerender(<FeedWorkspace events={events} stageId="s1" arrivals={3} mesh={mesh} />)
        expect(badge(container)).toBe('1')
      } finally { spy.mockRestore() }
    })
  })

  it('pins the window start when the reader scrolls up, so new rows do not slide it', () => {
    const events = range(1, 301).map((n) => text(n))
    const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
    scrollFeed(container, 100)
    rerender(<FeedWorkspace events={[...events, text(301)]} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(151)
    expect(container.querySelector('.feed-item-text')?.textContent?.trim()).toBe('message 151')
  })

  it('starts at the first available row when the pinned row was evicted', () => {
    const events = range(1, 301).map((n) => text(n))
    const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
    scrollFeed(container, 100)
    rerender(<FeedWorkspace events={range(200, 320).map((n) => text(n))} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(120)
  })

  it('resets the window on a stage switch', () => {
    const events = range(1, 401).map((n) => text(n))
    const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (250)' }))
    expect(container.querySelectorAll('.feed-group')).toHaveLength(300)
    rerender(<FeedWorkspace events={events} stageId="s2" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(150)
  })

  it('Show earlier keeps the first visible row where it was', () => {
    const events = range(1, 401).map((n) => text(n))
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    const scroll = container.querySelector('.feed-scroll') as HTMLElement
    // 20px per mounted row: showing 150 more rows above grows the content by 3000px.
    Object.defineProperty(scroll, 'scrollHeight', { get: () => container.querySelectorAll('.feed-group').length * 20, configurable: true })
    scroll.scrollTop = 40
    fireEvent.scroll(scroll)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (250)' }))
    expect(scroll.scrollTop).toBe(3040)
  })

  describe('jump to a quoted message outside the window', () => {
    const message = (id: string, n: number, extra: Partial<MeshMessage> = {}): MeshMessage => ({
      id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `body ${id}`, seq: n, created_at: at(n), ...extra,
    })

    it('expands the window to the quoted message and flashes it', async () => {
      const spy = vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation').mockReturnValue({
        messages: [message('old', 1), message('new', 1000, { in_reply_to: 'old' })],
        threads: {},
      } as unknown as ReturnType<typeof import('../../hooks/use-mesh').useStageConversation>)
      try {
        const mesh = { roster: { enabled: true, probed: true, participants: [], threads: [] }, profiles: [], stages: [], pick: null }
        const { container } = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={mesh} />)
        expect(container.querySelector('[data-message-id="old"]')).toBeNull()
        const quote = container.querySelector('.conversation-quote-jump') as HTMLElement
        fireEvent.click(quote)
        const target = container.querySelector('[data-message-id="old"]')
        expect(target).not.toBeNull()
        expect(target).toHaveClass('is-jump-target')
      } finally { spy.mockRestore() }
    })

    it('a finished jump does not pull the window back after Jump to latest', async () => {
      const spy = vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation').mockReturnValue({
        messages: [message('old', 1), message('new', 1000, { in_reply_to: 'old' })],
        threads: {},
      } as unknown as ReturnType<typeof import('../../hooks/use-mesh').useStageConversation>)
      try {
        const mesh = { roster: { enabled: true, probed: true, participants: [], threads: [] }, profiles: [], stages: [], pick: null }
        const { container } = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={mesh} />)
        fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
        expect(container.querySelector('[data-message-id="old"]')).not.toBeNull()

        fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))

        expect(container.querySelector('[data-message-id="old"]')).toBeNull()
        expect(container.querySelectorAll('.feed-group').length).toBeLessThanOrEqual(150)
      } finally { spy.mockRestore() }
    })
  })

  describe('jump to the question awaiting the reply', () => {
    const participants = [
      { id: 'user', kind: 'user', scope: 'run', display_name: 'You', reachable: true },
      { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', alias: 's1', display_name: 'S1', stage_id: 's1', reachable: true },
      { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
    ]
    const thread = { id: 'mt-1', scope: 'run', members: ['user', 'stage:s1', 'run:qa'], status: 'open' }
    const question = (id: string, n: number): MeshMessage => ({
      id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'request', body: `body ${id}`, seq: n, created_at: at(n),
    })
    async function renderWithQuestions(ids: Array<[string, number]>) {
      const spy = vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation').mockReturnValue({
        messages: ids.map(([id, n]) => question(id, n)),
        threads: { 'mt-1': thread },
      } as unknown as ReturnType<typeof import('../../hooks/use-mesh').useStageConversation>)
      const mesh = { roster: { enabled: true, probed: true, participants, threads: [] }, profiles: [], stages: [], pick: null }
      const replyJumpRef: { current: (() => void) | null } = { current: null }
      const view = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={mesh as never} replyJumpRef={replyJumpRef} />)
      return { ...view, replyJumpRef, spy }
    }

    it('expands the window to a pending question outside it and flashes it', async () => {
      const { container, replyJumpRef, spy } = await renderWithQuestions([['q1', 1]])
      try {
        expect(container.querySelector('[data-message-id="q1"]')).toBeNull()
        act(() => replyJumpRef.current?.())
        expect(container.querySelector('[data-message-id="q1"]')).toHaveClass('is-jump-target')
      } finally { spy.mockRestore() }
    })

    it('goes to the next pending question on each activation', async () => {
      const { container, replyJumpRef, spy } = await renderWithQuestions([['q1', 1], ['q2', 1000]])
      try {
        act(() => replyJumpRef.current?.())
        expect(container.querySelector('[data-message-id="q1"]')).toHaveClass('is-jump-target')
        act(() => replyJumpRef.current?.())
        expect(container.querySelector('[data-message-id="q2"]')).toHaveClass('is-jump-target')
        act(() => replyJumpRef.current?.())
        expect(container.querySelector('[data-message-id="q1"]')).toHaveClass('is-jump-target')
      } finally { spy.mockRestore() }
    })

    it('does nothing without a pending question', async () => {
      const { container, replyJumpRef, spy } = await renderWithQuestions([])
      try {
        act(() => replyJumpRef.current?.())
        expect(container.querySelector('.is-jump-target')).toBeNull()
      } finally { spy.mockRestore() }
    })
  })

  it('Show earlier inside an expanded tool series keeps the first visible row where it was', () => {
    const { container } = render(<FeedWorkspace events={range(1, 401).map(tool)} stageId="s1" />)
    const details = container.querySelector('details') as HTMLDetailsElement
    act(() => {
      details.open = true
      details.dispatchEvent(new Event('toggle'))
    })
    const scroll = container.querySelector('.feed-scroll') as HTMLElement
    // 20px per mounted call row: 150 more rows above grow the content by 3000px.
    Object.defineProperty(scroll, 'scrollHeight', { get: () => container.querySelectorAll('.feed-tool-rows .feed-item').length * 20, configurable: true })
    scroll.scrollTop = 40
    fireEvent.scroll(scroll)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (250)' }))
    expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(300)
    expect(scroll.scrollTop).toBe(3040)
  })
})

describe('FeedWorkspace lazy tool groups', () => {
  const open = (details: HTMLDetailsElement, value: boolean): void => {
    act(() => {
      details.open = value
      details.dispatchEvent(new Event('toggle'))
    })
  }

  it('renders no call rows while collapsed, exactly count rows when expanded, none after collapsing', () => {
    const { container } = render(<FeedWorkspace events={[tool(1), tool(2), tool(3)]} stageId="s1" />)
    const details = container.querySelector('details') as HTMLDetailsElement
    expect(details.querySelector('summary')).toHaveTextContent('3 tool calls')
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(0)
    open(details, true)
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
    open(details, false)
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(0)
  })

  it('an expanded series of 400 calls renders 150 rows and its own Show earlier; collapsing resets it', () => {
    const { container } = render(<FeedWorkspace events={range(1, 401).map(tool)} stageId="s1" />)
    const details = container.querySelector('details') as HTMLDetailsElement
    open(details, true)
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(150)
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier (250)' }))
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(300)
    open(details, false)
    open(details, true)
    expect(details.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(150)
  })
})

describe('FeedWorkspace Markdown parsing', () => {
  let parse: ReturnType<typeof vi.spyOn>
  let render_: ReturnType<typeof vi.spyOn>
  beforeEach(() => {
    parse = vi.spyOn(MarkdownIt.prototype, 'parse')
    render_ = vi.spyOn(MarkdownIt.prototype, 'render')
  })
  afterEach(() => {
    parse.mockRestore()
    render_.mockRestore()
  })

  it('one appended event parses only the new Markdown rows', () => {
    const base = range(1, 21).map((n) => text(n, `unique body ${n} **bold**`))
    const { rerender } = render(<FeedWorkspace events={base} stageId="s1" />)
    parse.mockClear()
    render_.mockClear()
    rerender(<FeedWorkspace events={[...base, text(21, 'unique body 21 **bold**')]} stageId="s1" />)
    // The new row: one parse to find code blocks plus the render itself (which parses too).
    expect(render_.mock.calls.length).toBeLessThanOrEqual(1)
    expect(parse.mock.calls.length).toBeLessThanOrEqual(2)
  })

  it('one appended event does not reparse unchanged conversation rows', async () => {
    const messages = range(1, 6).map((n) => ({
      id: `cm${n}`, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `conversation body ${n} **bold**`, seq: n, created_at: at(n),
    }))
    const spy = vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation').mockReturnValue({
      messages,
      threads: {},
    } as unknown as ReturnType<typeof import('../../hooks/use-mesh').useStageConversation>)
    try {
      const mesh = { roster: { enabled: true, probed: true, participants: [], threads: [] }, profiles: [], stages: [], pick: null }
      const base = range(10, 15).map((n) => text(n, `conversation feed ${n}`))
      const { rerender } = render(<FeedWorkspace events={base} stageId="s1" mesh={mesh} />)
      parse.mockClear()
      render_.mockClear()
      rerender(<FeedWorkspace events={[...base, text(15, 'conversation feed 15')]} stageId="s1" mesh={mesh} />)
      expect(render_.mock.calls.length).toBeLessThanOrEqual(1)
      expect(parse.mock.calls.length).toBeLessThanOrEqual(2)
    } finally { spy.mockRestore() }
  })
})
