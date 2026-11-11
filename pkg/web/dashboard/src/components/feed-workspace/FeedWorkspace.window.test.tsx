import { render, screen, fireEvent, act } from '@testing-library/react'
import type { ReactElement } from 'react'
import MarkdownIt from 'markdown-it'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import type { MeshMessage } from '../../api/mesh-client'
import { FeedWorkspace } from './FeedWorkspace'
import { lastMount, lastScroll, listCalls, moveScroller, resetListCalls } from '../../test/list-spy'
import { buildFeedTimeline, toFeedItems } from './feed-view-model'
import { buildVirtualFeedRows } from './feed-virtual-model'
import { resetUnknownCommandLabels } from '../../lib/executor-label'
import { EventBuffer } from '../../hooks/use-event-feed/event-buffer'
import { toEvent } from '../../hooks/use-event-feed/use-event-feed'

const at = (n: number): string => new Date(Date.UTC(2026, 0, 1, 10, 0, 0) + n * 1000).toISOString()
const event = (type: string, payload: unknown, n: number, stageId = 's1'): AfmEvent => ({ type, payload, stageId, timestamp: at(n), seq: n }) as AfmEvent
const board = (n: number): AfmEvent => event('mesh_board', { key: `k${n}`, author_id: 'a', status: 'ready' }, n)
const tool = (n: number): AfmEvent => event('agent_action', { tool: 'Read', detail: `file${n}` }, n)
const text = (n: number, detail = `message ${n}`): AfmEvent => event('agent_action', { tool: 'text', detail }, n)
const range = (from: number, to: number): number[] => Array.from({ length: to - from }, (_, i) => from + i)

// A scroller with a fixed layout (jsdom has none), scrolled by a reader's wheel gesture.
function scrollFeed(container: HTMLElement, scrollTop: number): HTMLElement {
  const scroll = container.querySelector('.feed-scroll') as HTMLElement
  Object.defineProperties(scroll, { scrollHeight: { value: 5000, configurable: true }, clientHeight: { value: 200, configurable: true } })
  // The scroller stood on the other side before the gesture: a user scroll is told by where the scroller moved.
  scroll.scrollTop = scrollTop < 4000 ? 4800 : 100
  fireEvent.scroll(scroll)
  scroll.scrollTop = scrollTop
  // One act with a frozen clock: a slow render between the gesture and its scroll event must not outlive the gesture window.
  const now = vi.spyOn(performance, 'now').mockReturnValue(performance.now())
  try {
    act(() => {
      fireEvent.wheel(scroll, { deltaY: scrollTop < 4000 ? -100 : 100 })
      fireEvent.scroll(scroll)
    })
  } finally { now.mockRestore() }
  return scroll
}

// A small viewport: the list may mount only the rows around it.
const smallViewport = (feed: ReactElement): ReactElement => (
  <VirtuosoMockContext.Provider value={{ viewportHeight: 400, itemHeight: 20 }}>{feed}</VirtuosoMockContext.Provider>
)
const mounted = (container: HTMLElement): number => container.querySelectorAll('[data-virtual-row-key]').length

const noMesh = { roster: { enabled: true, probed: true, participants: [], threads: [] }, profiles: [], stages: [], pick: null }
// rowIndex — where the feed's own model puts a row: the index the list is asked to scroll to.
const rowIndex = (events: AfmEvent[], messages: MeshMessage[], key: string): number | undefined =>
  buildVirtualFeedRows(buildFeedTimeline(toFeedItems(events), messages, 'all'), new Set()).indexByKey.get(key)
const conversationOf = async (messages: MeshMessage[], threads: Record<string, unknown> = {}) =>
  vi.spyOn(await import('../../hooks/use-mesh'), 'useStageConversation').mockReturnValue({ messages, threads } as unknown as ReturnType<typeof import('../../hooks/use-mesh').useStageConversation>)

describe('FeedWorkspace virtual feed', () => {
  beforeEach(() => {
    resetUnknownCommandLabels()
    resetListCalls()
  })

  it('a history of 5000 messages mounts only the rows around the viewport, and has no Show earlier', () => {
    const { container } = render(smallViewport(<FeedWorkspace events={range(1, 5001).map((n) => text(n))} stageId="s1" />))
    expect(mounted(container)).toBeGreaterThan(0)
    expect(mounted(container)).toBeLessThan(80)
    expect(screen.queryByRole('button', { name: /Show earlier/ })).toBeNull()
  })

  it('one group of 5000 board rows is 5000 rows of the list, not one block', () => {
    const { container } = render(smallViewport(<FeedWorkspace events={range(1, 5001).map(board)} stageId="s1" />))
    expect(mounted(container)).toBeLessThan(80)
    // Each mounted row is its own group element, in the order of the history.
    expect(container.querySelectorAll('.feed-group')).toHaveLength(mounted(container))
    expect(container.querySelector('[data-virtual-row-key]')?.textContent).toContain('k1')
  })

  it('an open series of 2000 calls is rows of the list: the DOM stays bounded', () => {
    const { container } = render(smallViewport(<FeedWorkspace events={range(1, 2001).map(tool)} stageId="s1" />))
    fireEvent.click(container.querySelector('button.feed-tool-summary') as HTMLElement)
    expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'true')
    expect(container.querySelectorAll('.feed-tool-rows .feed-item').length).toBeGreaterThan(0)
    expect(mounted(container)).toBeLessThan(80)
  })

  it('a list that slides at a constant length keeps following the bottom', () => {
    const buffer = new EventBuffer(4)
    const frame = (events: AfmEvent[]) => <FeedWorkspace events={events} stageId="s1" arrivals={buffer.arrivals().stages.get('s1') ?? 0} />
    const accept = (ns: number[]): AfmEvent[] => {
      ns.forEach((n) => buffer.push(text(n)))
      return buffer.flush() as AfmEvent[]
    }
    const { rerender } = render(frame(accept([1, 2, 3, 4])))
    rerender(frame(accept([5])))
    rerender(frame(accept([6, 7])))
    expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
  })

  describe('state of rows that leave the screen', () => {
    const message = (id: string, n: number, extra: Partial<MeshMessage> = {}): MeshMessage => ({
      id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `body ${id}`, seq: n, created_at: at(n), ...extra,
    })
    afterEach(() => vi.restoreAllMocks())

    it('an open series stays open when its row is unmounted by a filter and shown again', async () => {
      await conversationOf([message('c1', 1)])
      const events = [tool(2), tool(3), tool(4)]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
      fireEvent.click(container.querySelector('button.feed-tool-summary') as HTMLElement)
      expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
      fireEvent.click(screen.getByRole('button', { name: /^Conversations/ }))
      expect(container.querySelector('button.feed-tool-summary')).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: /^All activity/ }))
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'true')
      expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
    })

    it('an open report stays open and is not fetched again when its row is remounted', async () => {
      await conversationOf([message('c1', 1)])
      const verify = event('verify_result', { verification_id: 'v1', step: 1, verdict: 'pass', reason: 'ok', command: 'go test', report_path: 'r.md' }, 2)
      const { default: client } = { default: await import('../../api/verify-report-client') }
      const fetchReport = vi.spyOn(client, 'getVerifyReport').mockResolvedValue('# Report body')
      const { container } = render(<FeedWorkspace events={[verify]} stageId="s1" mesh={noMesh} />)
      fireEvent.click(screen.getByRole('button', { name: 'Show full report' }))
      expect(await screen.findByText('Report body')).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: /^Conversations/ }))
      fireEvent.click(screen.getByRole('button', { name: /^All activity/ }))
      expect(await screen.findByText('Report body')).toBeInTheDocument()
      expect(container.querySelector('.verify-report-toggle')).toHaveAttribute('aria-expanded', 'true')
      expect(fetchReport).toHaveBeenCalledTimes(1)
    })

    it('an open series stays open when its first call is evicted, and closes with the series', () => {
      const { container, rerender } = render(<FeedWorkspace events={[tool(1), tool(2), tool(3)]} stageId="s1" />)
      fireEvent.click(container.querySelector('button.feed-tool-summary') as HTMLElement)
      rerender(<FeedWorkspace events={[tool(2), tool(3), tool(4)]} stageId="s1" />)
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'true')
      expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
      // Every call of the series is gone: a new series with the same look starts closed.
      rerender(<FeedWorkspace events={[tool(7), tool(8)]} stageId="s1" />)
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'false')
      rerender(<FeedWorkspace events={[tool(2), tool(3), tool(4)]} stageId="s1" />)
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'false')
    })

    it('state is cleared when the stage changes', () => {
      const events = [tool(1), tool(2)]
      const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
      fireEvent.click(container.querySelector('button.feed-tool-summary') as HTMLElement)
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'true')
      rerender(<FeedWorkspace events={events} stageId="s2" />)
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'false')
    })
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

    it('restarts on a stage switch: the new stage opens at the bottom and the reader releases it again', () => {
      const { container, rerender } = render(view(capped(1), 10))
      scrollFeed(container, 100)
      rerender(view(capped(4), 13))
      expect(badge(container)).toBe('3')
      rerender(view(capped(1), 50, 's2'))
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
      rerender(view(capped(2), 51, 's2'))
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
      scrollFeed(container, 100)
      rerender(view(capped(3), 52, 's2'))
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

  describe('jump to a quoted message', () => {
    const message = (id: string, n: number, extra: Partial<MeshMessage> = {}): MeshMessage => ({
      id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `body ${id}`, seq: n, created_at: at(n), ...extra,
    })
    const targetOf = (container: HTMLElement, id: string) => container.querySelector(`[data-message-id="${id}"]`)
    afterEach(() => vi.restoreAllMocks())

    it('a quote far above leads to the quoted message by data and outlines it; Jump to latest lets go', async () => {
      await conversationOf([message('old', 1), message('new', 1000, { in_reply_to: 'old' })])
      const { container } = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={noMesh} />)
      fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
      expect(targetOf(container, 'old')).toHaveClass('is-jump-target')
      expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
      expect(targetOf(container, 'old')).not.toHaveClass('is-jump-target')
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
    })

    it('a quote clamped at the tail still releases follow: a scroll event without a gesture keeps it released, a wheel down at the end resumes it', async () => {
      await conversationOf([message('old', 1), message('new', 1000, { in_reply_to: 'old' })])
      const { container } = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={noMesh} />)
      const scroll = container.querySelector('.feed-scroll') as HTMLElement
      Object.defineProperties(scroll, { scrollHeight: { value: 5000, configurable: true }, clientHeight: { value: 200, configurable: true } })
      scroll.scrollTop = 4790
      fireEvent.scroll(scroll)
      fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
      expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()
      fireEvent.scroll(scroll)
      expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()
      scrollFeed(container, 4800)
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
    })

    it('a quote navigation cancels the bottom watch of an earlier collapse: five seconds of appends at the end do not resume follow', async () => {
      vi.useFakeTimers()
      try {
        await conversationOf([message('old', 1), message('new', 1000, { in_reply_to: 'old' })])
        const events = [text(2), text(3), tool(4), tool(5)]
        const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
        const scroll = container.querySelector('.feed-scroll') as HTMLElement
        // The expanded series stands 331px from the end when it is collapsed.
        Object.defineProperties(scroll, { scrollHeight: { value: 1331, configurable: true }, clientHeight: { value: 200, configurable: true } })
        scroll.scrollTop = 800
        const summary = container.querySelector('button.feed-tool-summary') as HTMLElement
        fireEvent.click(summary)
        fireEvent.click(summary)
        fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
        expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()
        // The list ends at the bottom after the layout, without a scroll event.
        Object.defineProperty(scroll, 'scrollHeight', { value: 1000, configurable: true })
        for (let second = 0; second < 5; second++) {
          rerender(<FeedWorkspace events={[...events, text(10 + second)]} stageId="s1" mesh={noMesh} />)
          act(() => vi.advanceTimersByTime(1000))
        }
        expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()
      } finally { vi.useRealTimers() }
    })

    it('a filter round trip with no reading row to carry lets the list follow the tail again', async () => {
      await conversationOf([message('c1', 1)])
      const events = [text(2), text(3), text(4)]
      const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
      scrollFeed(container, 100)
      expect(screen.getByRole('button', { name: /Jump to latest/ })).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: /^Conversations/ }))
      fireEvent.click(screen.getByRole('button', { name: /^All activity/ }))
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
      rerender(<FeedWorkspace events={[...events, text(5)]} stageId="s1" mesh={noMesh} />)
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
    })

    it('scrolls the list to the quoted row: its index, centred, instantly', async () => {
      const events = range(2, 400).map((n) => text(n))
      const messages = [message('old', 200), message('new', 1000, { in_reply_to: 'old' })]
      await conversationOf(messages)
      const { container } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
      resetListCalls()
      fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
      const index = rowIndex(events, messages, 'old')
      expect(index).toBeGreaterThan(150)
      expect(lastScroll()).toEqual({ index, align: 'center', behavior: 'auto' })
    })

    it('Jump to latest cancels the navigation: the list is sent to its last row at once', async () => {
      await conversationOf([message('old', 200), message('new', 1000, { in_reply_to: 'old' })])
      const { container } = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={noMesh} />)
      fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
      expect(lastScroll()).toMatchObject({ align: 'center' })
      fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
      expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
      expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull()
    })

    it('a quote hidden by the Author filter opens the list at the quoted row, under the unfiltered view', async () => {
      const events = range(2, 400).map((n) => text(n))
      const messages = [message('old', 200), message('new', 1000, { in_reply_to: 'old', recipient_ids: ['user'] })]
      await conversationOf(messages)
      const { container } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
      fireEvent.click(screen.getByRole('button', { name: /Author's work/ }))
      expect(lastMount()).toEqual({ index: 'LAST', align: 'end' })
      fireEvent.click(container.querySelector('.conversation-quote-jump') as HTMLElement)
      expect(screen.getByRole('button', { name: /All activity/ })).toHaveAttribute('aria-pressed', 'true')
      expect(lastMount()).toEqual({ index: rowIndex(events, messages, 'old'), align: 'center', offset: undefined })
    })

    describe('live calls received in one millisecond', () => {
      const liveCall = (): AfmEvent => toEvent({ type: 'agent_action', stage_id: 's1', data: { tool: 'Read', detail: 'same' } })
      const separator: AfmEvent = event('stage_status_changed', 'running', 1)
      const summaryButtons = (container: HTMLElement): HTMLElement[] => [...container.querySelectorAll<HTMLElement>('button.feed-tool-summary')]

      it('an evicted head does not open the next series', () => {
        const events = [liveCall(), liveCall(), liveCall(), separator, liveCall(), liveCall(), liveCall()]
        const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
        fireEvent.click(summaryButtons(container)[0] as HTMLElement)
        expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)

        for (const next of [events.slice(1), [...events.slice(1), liveCall()]]) {
          rerender(<FeedWorkspace events={next} stageId="s1" />)
          const [first, second] = summaryButtons(container)
          expect(first).toHaveAttribute('aria-expanded', 'true')
          expect(second).toHaveAttribute('aria-expanded', 'false')
          // Only the first series shows its calls: two are left of it.
          expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(2)
        }
      })
    })

    it('an open tool series survives a filter that hides it and the way back', async () => {
      await conversationOf([message('old', 1)])
      const { container } = render(<FeedWorkspace events={[text(2), tool(3), tool(4), tool(5)]} stageId="s1" mesh={noMesh} />)
      fireEvent.click(container.querySelector('button.feed-tool-summary') as HTMLElement)
      expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
      fireEvent.click(screen.getByRole('button', { name: /Conversations/ }))
      expect(container.querySelector('button.feed-tool-summary')).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: /All activity/ }))
      expect(container.querySelector('button.feed-tool-summary')).toHaveAttribute('aria-expanded', 'true')
      expect(container.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
    })

    it('the outline is a time-limited request by id: a second click on the quote starts it again', async () => {
      vi.useFakeTimers()
      try {
        await conversationOf([message('old', 1), message('new', 1000, { in_reply_to: 'old' })])
        const { container } = render(<FeedWorkspace events={range(2, 20).map((n) => text(n))} stageId="s1" mesh={noMesh} />)
        const quote = container.querySelector('.conversation-quote-jump') as HTMLElement
        fireEvent.click(quote)
        act(() => vi.advanceTimersByTime(1000))
        fireEvent.click(quote)
        act(() => vi.advanceTimersByTime(1000))
        expect(targetOf(container, 'old')).toHaveClass('is-jump-target')
        act(() => vi.advanceTimersByTime(700))
        expect(targetOf(container, 'old')).not.toHaveClass('is-jump-target')
      } finally { vi.useRealTimers() }
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
      const spy = await conversationOf(ids.map(([id, n]) => question(id, n)), { 'mt-1': thread })
      const mesh = { roster: { enabled: true, probed: true, participants, threads: [] }, profiles: [], stages: [], pick: null }
      const replyJumpRef: { current: (() => void) | null } = { current: null }
      const view = render(<FeedWorkspace events={range(2, 400).map((n) => text(n))} stageId="s1" mesh={mesh as never} replyJumpRef={replyJumpRef} />)
      return { ...view, replyJumpRef, spy }
    }

    it('goes to a pending question far above and outlines it', async () => {
      const { container, replyJumpRef, spy } = await renderWithQuestions([['q1', 1]])
      try {
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

// Scroll ownership: while the reader scrolls by hand the feed does not move on its own; an explicit Jump moves it at once.
describe('FeedWorkspace scroll ownership', () => {
  // A controlled clock: the gesture window is measured with performance.now.
  let clock = 0
  beforeEach(() => {
    resetUnknownCommandLabels()
    resetListCalls()
    clock = 1_000
    vi.spyOn(performance, 'now').mockImplementation(() => clock)
  })
  afterEach(() => vi.restoreAllMocks())

  // stand — a following feed at the given distance above the bottom of a 200 px viewport.
  const stand = (container: HTMLElement, scrollHeight: number, scrollTop: number): HTMLElement => {
    const scroll = container.querySelector('.feed-scroll') as HTMLElement
    Object.defineProperties(scroll, { scrollHeight: { value: scrollHeight, configurable: true }, clientHeight: { value: 200, configurable: true } })
    scrollTo(scroll, scrollTop)
    return scroll
  }
  // A scroll event outside act would leave the follow update queued past the assertion.
  const scrollTo = (scroll: HTMLElement, top: number): void => act(() => moveScroller(scroll, top))
  const jumpShown = (): boolean => screen.queryByRole('button', { name: /Jump to latest/ }) !== null
  const tailRequested = (): boolean => listCalls.scrolls.some((location) => (location as { index: unknown }).index === 'LAST')

  it('a PageUp that keeps scrolling across an append releases follow: the append does not take the scroll over', () => {
    const { container, rerender } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1030, 800)
    fireEvent.keyDown(scroll, { key: 'PageUp' })
    scrollTo(scroll, 795)
    expect(jumpShown()).toBe(false)
    resetListCalls()
    rerender(<FeedWorkspace events={[text(10), text(11)]} stageId="s1" />)
    expect(tailRequested()).toBe(false)
    scrollTo(scroll, 700)
    expect(jumpShown()).toBe(true)
  })

  it('a wheel scroll queued before an append is still the reader\'s: follow is released and later appends stay put', () => {
    const { container, rerender } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1000, 800)
    fireEvent.wheel(scroll, { deltaY: -100 })
    scroll.scrollTop = 700
    rerender(<FeedWorkspace events={[text(10), text(11)]} stageId="s1" />)
    fireEvent.scroll(scroll)
    expect(jumpShown()).toBe(true)
    resetListCalls()
    rerender(<FeedWorkspace events={[text(10), text(11), text(12)]} stageId="s1" />)
    expect(tailRequested()).toBe(false)
  })

  it('a PageDown that reaches the bottom across an eviction behind a conversation row resumes follow and hides Jump', async () => {
    await conversationOf([{ id: 'c1', thread_id: 't1', sender_id: 'stage:s1', recipient_ids: ['user'], kind: 'message', body: 'old conversation', seq: 1, created_at: at(0) } as MeshMessage])
    let scroll: HTMLElement | null = null
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const key = this.getAttribute('data-virtual-row-key')
      const n = key?.startsWith('seq:') === true ? Number(key.slice(4)) : 0
      const top = this.id === 'feed-content' ? 0 : key === 'c1' ? -100 : (n - 10) * 20 + 120 - (scroll?.scrollTop ?? 100)
      const height = this.id === 'feed-content' ? 200 : key !== null ? 20 : 0
      return { top, bottom: top + height, height, left: 0, right: 300, width: 300, x: 0, y: top, toJSON: () => ({}) } as DOMRect
    })
    const events = range(10, 60).map((n) => text(n))
    const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" mesh={noMesh} />)
    scroll = stand(container, 1030, 800)
    fireEvent.wheel(scroll, { deltaY: -100 })
    scrollTo(scroll, 650)
    expect(jumpShown()).toBe(true)
    await act(() => new Promise<void>((done) => requestAnimationFrame(() => done())))
    fireEvent.keyDown(scroll, { key: 'PageDown' })
    scrollTo(scroll, 700)
    resetListCalls()
    // The capped window evicts its oldest event behind the conversation row: the anchor restore waits for the PageDown.
    rerender(<FeedWorkspace events={[...events.slice(1), text(60)]} stageId="s1" mesh={noMesh} />)
    expect(listCalls.scrolls).toEqual([])
    scrollTo(scroll, 820)
    expect(jumpShown()).toBe(false)
    clock += 600
    rerender(<FeedWorkspace events={[...events.slice(2), text(60), text(61)]} stageId="s1" mesh={noMesh} />)
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
  })

  it('an append during a scrollbar hold does not scroll; after the release the tail is followed again', () => {
    const { container, rerender } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1030, 800)
    fireEvent.pointerDown(scroll)
    resetListCalls()
    // A tail request during the hold would land on stale size estimates far above the bottom.
    listCalls.move = () => moveScroller(scroll, 400)
    rerender(<FeedWorkspace events={[text(10), text(11)]} stageId="s1" />)
    expect(tailRequested()).toBe(false)
    expect(jumpShown()).toBe(false)
    listCalls.move = null
    fireEvent.pointerUp(window)
    clock += 600
    rerender(<FeedWorkspace events={[text(10), text(11), text(12)]} stageId="s1" />)
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
  })

  it('an explicit Jump to latest wins over a recent upward wheel', () => {
    const { container } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1000, 800)
    fireEvent.wheel(scroll, { deltaY: -100 })
    scrollTo(scroll, 500)
    expect(jumpShown()).toBe(true)
    resetListCalls()
    // The jump moves the scroller while the wheel is still recent: that scroll is the jump's, not the wheel's.
    listCalls.move = () => moveScroller(scroll, 400)
    fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
  })

  it('a stage switch right after an upward wheel opens the empty stage, its first data and its older history at the tail', () => {
    const { container, rerender } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1000, 800)
    fireEvent.wheel(scroll, { deltaY: -100 })
    scrollTo(scroll, 700)
    expect(jumpShown()).toBe(true)
    rerender(<FeedWorkspace events={[]} stageId="s2" />)
    expect(jumpShown()).toBe(false)
    rerender(<FeedWorkspace events={[event('agent_action', { tool: 'text', detail: 'first' }, 20, 's2')]} stageId="s2" />)
    expect(lastMount()).toEqual({ index: 'LAST', align: 'end' })
    expect(jumpShown()).toBe(false)
    resetListCalls()
    rerender(<FeedWorkspace events={[event('agent_action', { tool: 'text', detail: 'older' }, 19, 's2'), event('agent_action', { tool: 'text', detail: 'first' }, 20, 's2')]} stageId="s2" />)
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
  })

  it('a visible event anchor that Conversations does not hold opens the tail and follows the next append', async () => {
    await conversationOf([{ id: 'c1', thread_id: 't1', sender_id: 'stage:s1', recipient_ids: ['user'], kind: 'message', body: 'conversation', seq: 1, created_at: at(0) } as MeshMessage])
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const key = this.getAttribute('data-virtual-row-key')
      const top = key === 'seq:10' ? 20 : key === 'c1' ? -50 : 0
      const height = this.id === 'feed-content' ? 200 : key !== null ? 40 : 0
      return { top, bottom: top + height, height, left: 0, right: 300, width: 300, x: 0, y: top, toJSON: () => ({}) } as DOMRect
    })
    const { container, rerender } = render(<FeedWorkspace events={[text(10, 'anchor text')]} stageId="s1" mesh={noMesh} />)
    const scroll = stand(container, 260, 20)
    fireEvent.wheel(scroll, { deltaY: -100 })
    scrollTo(scroll, 0)
    await act(() => new Promise<void>((done) => requestAnimationFrame(() => done())))
    expect(container.querySelector('[data-virtual-row-key="seq:10"]')).not.toBeNull()
    expect(jumpShown()).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: /^Conversations/ }))
    expect(lastMount()).toEqual({ index: 'LAST', align: 'end' })
    expect(jumpShown()).toBe(false)
    rerender(<FeedWorkspace events={[text(10, 'anchor text'), text(11)]} stageId="s1" mesh={noMesh} />)
    expect(jumpShown()).toBe(false)
  })

  it('a nudge inside the threshold, an append during it, its end and a later append keep following; a longer scroll releases and Jump returns', () => {
    const { container, rerender } = render(<FeedWorkspace events={[text(10)]} stageId="s1" />)
    const scroll = stand(container, 1030, 800)
    fireEvent.wheel(scroll, { deltaY: -10 })
    scrollTo(scroll, 795)
    resetListCalls()
    rerender(<FeedWorkspace events={[text(10), text(11)]} stageId="s1" />)
    expect(tailRequested()).toBe(false)
    clock += 600
    rerender(<FeedWorkspace events={[text(10), text(11), text(12)]} stageId="s1" />)
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
    fireEvent.keyDown(scroll, { key: 'PageUp' })
    scrollTo(scroll, 790)
    resetListCalls()
    rerender(<FeedWorkspace events={[text(10), text(11), text(12), text(13)]} stageId="s1" />)
    scrollTo(scroll, 600)
    expect(tailRequested()).toBe(false)
    expect(jumpShown()).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }))
    expect(lastScroll()).toEqual({ index: 'LAST', align: 'end', behavior: 'auto' })
    expect(jumpShown()).toBe(false)
  })

  // Opening a report keeps the row keys: no composition change consumes the hold it places.
  describe('a report opened while an anchor restore waits for a gesture', () => {
    // 20 px rows in the order of the feed's own model, under a 200 px scroller at its scrollTop.
    let keys: string[] = []
    const keysOf = (events: AfmEvent[], messages: MeshMessage[] = []): string[] => buildVirtualFeedRows(buildFeedTimeline(toFeedItems(events), messages, 'all'), new Set()).rows.map((row) => row.key)
    const show = (rerender: (ui: ReactElement) => void, events: AfmEvent[], messages: MeshMessage[] = []): void => {
      keys = keysOf(events, messages)
      rerender(<FeedWorkspace events={events} stageId="s1" mesh={messages.length > 0 ? noMesh : undefined} />)
    }
    const verify = (n: number): AfmEvent => event('verify_result', { verification_id: 'v1', step: 1, verdict: 'pass', reason: 'ok', command: 'go test', report_path: 'r.md' }, n)
    const topOf = (container: HTMLElement, key: string): number => container.querySelector(`[data-virtual-row-key="${key}"]`)!.getBoundingClientRect().top
    // The list moves its scroller to the requested row, with the scroll event of a real list.
    const moveOnRequest = (scroll: HTMLElement): void => {
      listCalls.move = (location) => {
        const { index, offset = 0 } = location as { index: number; offset?: number }
        moveScroller(scroll, index * 20 + offset)
      }
    }
    const openReport = async (): Promise<void> => {
      fireEvent.click(screen.getByRole('button', { name: 'Show full report' }))
      await act(async () => { await Promise.resolve(); await Promise.resolve() })
      expect(screen.getByRole('button', { name: 'Hide report' })).toBeInTheDocument()
    }
    beforeEach(async () => {
      vi.useFakeTimers()
      vi.spyOn(await import('../../api/verify-report-client'), 'getVerifyReport').mockResolvedValue('# Report body')
      vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
        const row = this.closest('[data-virtual-row-key]')
        const key = row === this || this.hasAttribute('data-feed-item') ? row?.getAttribute('data-virtual-row-key') ?? null : null
        const offset = (document.querySelector('.feed-scroll') as HTMLElement | null)?.scrollTop ?? 0
        const top = this.id === 'feed-content' || key === null ? 0 : keys.indexOf(key) * 20 - offset
        const height = this.id === 'feed-content' ? 200 : key === null ? 0 : 20
        return { top, bottom: top + height, height, left: 0, right: 300, width: 300, x: 0, y: top, toJSON: () => ({}) } as DOMRect
      })
    })
    afterEach(() => vi.useRealTimers())

    it('after two capped renames of a series, the restore they deferred does not move the report the reader opened', async () => {
      // A window of 30 events behind three conversation rows that stay; calls 4-6 make one series, keyed by its first call.
      const messages = [1, 2, 3].map((n): MeshMessage => ({ id: `c${n}`, thread_id: 't1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `conversation ${n}`, seq: n, created_at: at(n - 4) }))
      await conversationOf(messages)
      const windowFrom = (from: number): AfmEvent[] => range(from, from + 30).map((n) => (n <= 6 ? tool(n) : n === 8 ? verify(n) : text(n)))
      keys = keysOf(windowFrom(4), messages)
      const { container, rerender } = render(<FeedWorkspace events={windowFrom(4)} stageId="s1" mesh={noMesh} />)
      const scroll = stand(container, 1030, 64)
      fireEvent.wheel(scroll, { deltaY: -10 })
      scrollTo(scroll, 54)
      act(() => vi.advanceTimersByTime(20))
      expect(jumpShown()).toBe(true)
      resetListCalls()
      show(rerender, windowFrom(5), messages)
      // The wheel goes on and captures the series under its new key; the next eviction renames it again.
      scrollTo(scroll, 53)
      act(() => vi.advanceTimersByTime(20))
      show(rerender, windowFrom(6), messages)
      expect(listCalls.scrolls).toEqual([])
      expect(topOf(container, 'seq:8')).toBe(47)
      await openReport()
      moveOnRequest(scroll)
      clock += 600
      act(() => vi.advanceTimersByTime(2000))
      expect(topOf(container, 'seq:8')).toBe(47)
      expect(jumpShown()).toBe(true)
    })

    it('the opened report does not hold its row over the next gesture: the restore of the next change waits for it and keeps the row read last', async () => {
      const events = range(1, 31).map((n) => (n === 8 ? verify(n) : text(n)))
      keys = keysOf(events)
      const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
      const scroll = stand(container, 1030, 120)
      fireEvent.wheel(scroll, { deltaY: -50 })
      scrollTo(scroll, 110)
      act(() => vi.advanceTimersByTime(20))
      expect(jumpShown()).toBe(true)
      resetListCalls()
      const merged = events.filter((e) => e.seq !== 2)
      show(rerender, merged)
      expect(listCalls.scrolls).toEqual([])
      expect(topOf(container, 'seq:8')).toBe(10)
      await openReport()
      // A later wheel down leaves seq:9 10 px above the top; then rows above it merge away again.
      clock += 200
      fireEvent.wheel(scroll, { deltaY: 50 })
      scrollTo(scroll, 150)
      act(() => vi.advanceTimersByTime(20))
      resetListCalls()
      moveOnRequest(scroll)
      show(rerender, merged.filter((e) => e.seq !== 3))
      expect(listCalls.scrolls).toEqual([])
      clock += 600
      act(() => vi.advanceTimersByTime(500))
      expect(listCalls.scrolls).toEqual([{ index: 6, align: 'start', offset: 10, behavior: 'auto' }])
      expect(topOf(container, 'seq:9')).toBe(-10)
    })

    it('a composition change after the report was opened over two capped renames restores the rows seen at the click', async () => {
      // A window of 30 events behind three conversation rows that stay; calls 4-6 make one series, keyed by its first call.
      const messages = [1, 2, 3].map((n): MeshMessage => ({ id: `c${n}`, thread_id: 't1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: `conversation ${n}`, seq: n, created_at: at(n - 4) }))
      await conversationOf(messages)
      const windowFrom = (from: number): AfmEvent[] => range(from, from + 30).map((n) => (n <= 6 ? tool(n) : n === 8 ? verify(n) : text(n)))
      keys = keysOf(windowFrom(4), messages)
      const { container, rerender } = render(<FeedWorkspace events={windowFrom(4)} stageId="s1" mesh={noMesh} />)
      const scroll = stand(container, 1030, 64)
      fireEvent.wheel(scroll, { deltaY: -10 })
      scrollTo(scroll, 54)
      act(() => vi.advanceTimersByTime(20))
      expect(jumpShown()).toBe(true)
      resetListCalls()
      show(rerender, windowFrom(5), messages)
      // The wheel goes on and captures the series under its new key; the next eviction renames it again.
      scrollTo(scroll, 53)
      act(() => vi.advanceTimersByTime(20))
      show(rerender, windowFrom(6), messages)
      expect(listCalls.scrolls).toEqual([])
      expect(topOf(container, 'seq:8')).toBe(47)
      await openReport()
      moveOnRequest(scroll)
      // Two later conversation messages land above the event rows: the composition changes without a scroll that captures anchors.
      const incoming: MeshMessage = { id: 'c4', thread_id: 't1', sender_id: 'run:qa', recipient_ids: ['stage:s1'], kind: 'message', body: 'conversation 4', seq: 4, created_at: at(3) }
      const nextMessages = [...messages, incoming, { ...incoming, id: 'c5', seq: 5, body: 'conversation 5', created_at: at(4) }]
      await conversationOf(nextMessages)
      show(rerender, windowFrom(6), nextMessages)
      clock += 600
      act(() => vi.advanceTimersByTime(500))
      expect(topOf(container, 'tools:seq:6')).toBe(7)
      expect(topOf(container, 'seq:8')).toBe(47)
      expect(jumpShown()).toBe(true)
    })
  })
})
