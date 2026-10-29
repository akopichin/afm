import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AssistantActivityFeed } from './AssistantActivityFeed'
import type { AssistantActivity } from '../../hooks/use-assistant-activity'
import type { AssistantActivityEntry } from '../../types/assistant-activity'

function entry(n: number, over: Partial<AssistantActivityEntry> = {}): AssistantActivityEntry {
  return {
    eventId: `e${n}`, taskId: 't1', participantId: '', turn: 1,
    occurredAt: new Date(Date.UTC(2026, 9, 27, 12, 0, n)).toISOString(),
    tool: 'Bash', detail: `cmd ${n}`, truncated: false, cursor: String(n * 10), ...over,
  }
}

function activity(entries: AssistantActivityEntry[], over: Partial<AssistantActivity> = {}): AssistantActivity {
  return {
    entries, status: 'ready', refreshFailing: false, hasOlder: false, loadingOlder: false, olderCapped: false,
    loadOlder: vi.fn(), retry: vi.fn(), ...over,
  }
}

const LONG = `line one\n${'x'.repeat(400)}\nline three`

// jsdom has no layout: give the scroll region the metrics the stick-to-bottom hook reads.
function setMetrics(el: HTMLElement, m: { scrollHeight: () => number; clientHeight: number }): void {
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: m.scrollHeight })
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => m.clientHeight })
}

// jsdom has no layout: a row sits 100px per preceding sibling, so rows added above push the ones below down.
function mockRowOffsets(): void {
  vi.spyOn(HTMLElement.prototype, 'offsetTop', 'get').mockImplementation(function (this: HTMLElement) {
    return this.parentElement === null ? 0 : Array.from(this.parentElement.children).indexOf(this) * 100
  })
}

afterEach(() => vi.restoreAllMocks())

const scrollRegion = () => screen.getByRole('region', { name: 'Helper activity' })

describe('AssistantActivityFeed rows', () => {
  test('a tool call is a compact row with its name and detail', () => {
    render(<AssistantActivityFeed activity={activity([entry(1, { tool: 'WebFetch', detail: 'https://example.com/a' })])} empty={null} />)
    expect(screen.getByText('WebFetch')).toBeInTheDocument()
    expect(screen.getByText('https://example.com/a')).toBeInTheDocument()
  })

  test('a message is rendered as safe Markdown', () => {
    const { container } = render(
      <AssistantActivityFeed activity={activity([entry(1, { tool: 'text', detail: 'Found **two** issues <script>alert(1)</script>' })])} empty={null} />,
    )
    expect(container.querySelector('strong')).toHaveTextContent('two')
    expect(container.querySelector('script')).toBeNull()
    expect(screen.queryByText('text')).toBeNull()
  })

  test('an image marker in a message is not turned into an image', () => {
    const { container } = render(
      <AssistantActivityFeed activity={activity([entry(1, { tool: 'text', detail: 'see [AFM image: shot.png]' })])} empty={null} />,
    )
    expect(container.querySelector('img')).toBeNull()
  })

  test('two identical calls stay two rows', () => {
    render(
      <AssistantActivityFeed
        activity={activity([entry(1, { tool: 'ToolSearch', detail: 'same' }), entry(2, { tool: 'ToolSearch', detail: 'same' })])}
        empty={null}
      />,
    )
    expect(screen.getAllByText('ToolSearch')).toHaveLength(2)
  })

  test('a truncated entry says it was shortened', () => {
    render(<AssistantActivityFeed activity={activity([entry(1, { truncated: true })])} empty={null} />)
    expect(screen.getByText(/shortened/i)).toBeInTheDocument()
  })

  test('a short detail has no expand control', () => {
    render(<AssistantActivityFeed activity={activity([entry(1)])} empty={null} />)
    expect(screen.queryByRole('button', { name: /show more|show less/i })).toBeNull()
  })

  test('a long detail is expanded by the reader, and the feed lets go of the bottom first', () => {
    render(<AssistantActivityFeed activity={activity([entry(1, { detail: LONG })])} empty={null} />)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()

    const toggle = screen.getByRole('button', { name: /show more/i })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)

    expect(screen.getByRole('button', { name: /show less/i })).toHaveAttribute('aria-expanded', 'true')
    // Releasing stick-to-bottom is what keeps the expanded text from being scrolled away.
    expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeInTheDocument()
  })

  test('a long message can be expanded too, and collapsed again', () => {
    render(<AssistantActivityFeed activity={activity([entry(1, { tool: 'text', detail: LONG })])} empty={null} />)
    fireEvent.click(screen.getByRole('button', { name: /show more/i }))
    fireEvent.click(screen.getByRole('button', { name: /show less/i }))
    expect(screen.getByRole('button', { name: /show more/i })).toHaveAttribute('aria-expanded', 'false')
  })

  test('the expand control points at the text it controls', () => {
    render(<AssistantActivityFeed activity={activity([entry(1, { detail: LONG })])} empty={null} />)
    const toggle = screen.getByRole('button', { name: /show more/i })
    const id = toggle.getAttribute('aria-controls')
    expect(id).toBeTruthy()
    expect(document.getElementById(id ?? '')).toHaveTextContent('line one')
  })
})

describe('AssistantActivityFeed layout', () => {
  test('the empty state shows only without entries', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([])} empty={<p>Nothing yet</p>} />)
    expect(screen.getByText('Nothing yet')).toBeInTheDocument()
    rerender(<AssistantActivityFeed activity={activity([entry(1)])} empty={<p>Nothing yet</p>} />)
    expect(screen.queryByText('Nothing yet')).toBeNull()
  })

  test('the result card is the last item, after the history', () => {
    render(
      <AssistantActivityFeed activity={activity([entry(1, { detail: 'first' }), entry(2, { detail: 'second' })])} empty={null}>
        <div data-testid="result-card">Verdict</div>
      </AssistantActivityFeed>,
    )
    const region = scrollRegion()
    const second = within(region).getByText('second')
    const card = within(region).getByTestId('result-card')
    expect(second.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  test('the trailing card is shown even when there is no history', () => {
    render(
      <AssistantActivityFeed activity={activity([])} empty={<p>No history</p>}>
        <div data-testid="result-card">Verdict</div>
      </AssistantActivityFeed>,
    )
    expect(screen.getByTestId('result-card')).toBeInTheDocument()
  })

  test('has no composer', () => {
    render(<AssistantActivityFeed activity={activity([entry(1)])} empty={null} />)
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  test('Jump to latest appears when the reader scrolls up and returns to the bottom', () => {
    render(<AssistantActivityFeed activity={activity([entry(1), entry(2)])} empty={null} />)
    const region = scrollRegion()
    setMetrics(region, { scrollHeight: () => 1000, clientHeight: 300 })

    region.scrollTop = 100
    fireEvent.scroll(region)
    const jump = screen.getByRole('button', { name: 'Jump to latest' })

    fireEvent.click(jump)
    expect(region.scrollTop).toBe(1000)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })

  test('new entries keep a reader who scrolled up where they are', async () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(1), entry(2)])} empty={null} />)
    const region = scrollRegion()
    setMetrics(region, { scrollHeight: () => 1000, clientHeight: 300 })
    region.scrollTop = 100
    fireEvent.scroll(region)

    rerender(<AssistantActivityFeed activity={activity([entry(1), entry(2), entry(3)])} empty={null} />)
    await act(async () => { await Promise.resolve() })
    expect(region.scrollTop).toBe(100)
    expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeInTheDocument()
  })
})

describe('AssistantActivityFeed bounded window', () => {
  test('rows trimmed from the top while the reader is scrolled up do not move what they are looking at', () => {
    mockRowOffsets()
    // The button above the rows keeps every row's index (and so its mocked offset) comparable across renders.
    const rows = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => entry(from + i))
    const { rerender } = render(<AssistantActivityFeed activity={activity(rows(1, 6), { hasOlder: true })} empty={null} />)
    const region = scrollRegion()
    // With a layout the scroll up lets go of the end; without one the feed would still count as following it.
    setMetrics(region, { scrollHeight: () => region.children.length * 100, clientHeight: 100 })
    region.scrollTop = 250 // e3 is the first row still in view
    fireEvent.scroll(region)

    rerender(<AssistantActivityFeed activity={activity(rows(3, 8), { hasOlder: true })} empty={null} />)
    // e3 was at 300 − 250 = 50 below the top of the view and must stay there.
    expect(region.scrollTop).toBe(50)
  })

  test('an anchor row that is still there keeps its place when rows are appended below', () => {
    mockRowOffsets()
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(1), entry(2), entry(3)])} empty={null} />)
    const region = scrollRegion()
    region.scrollTop = 150
    fireEvent.scroll(region)
    rerender(<AssistantActivityFeed activity={activity([entry(1), entry(2), entry(3), entry(4)])} empty={null} />)
    expect(region.scrollTop).toBe(150)
  })

  test('focus inside a row that gets trimmed moves to the region instead of the page', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(1, { detail: LONG }), entry(2), entry(3)], { hasOlder: true })} empty={null} />)
    const toggle = screen.getByRole('button', { name: /show more/i })
    toggle.focus()

    rerender(<AssistantActivityFeed activity={activity([entry(2), entry(3)], { hasOlder: true })} empty={null} />)
    expect(scrollRegion()).toHaveFocus()
  })

  test('a trim does not take focus from a reader who already moved it elsewhere', () => {
    const view = (entries: AssistantActivityEntry[]) => (
      <>
        <button type="button">elsewhere</button>
        <AssistantActivityFeed activity={activity(entries, { hasOlder: true })} empty={null} />
      </>
    )
    const { rerender } = render(view([entry(1, { detail: LONG }), entry(2)]))
    screen.getByRole('button', { name: /show more/i }).focus()
    screen.getByRole('button', { name: 'elsewhere' }).focus()
    rerender(view([entry(2)]))
    expect(screen.getByRole('button', { name: 'elsewhere' })).toHaveFocus()
  })

  test('a trim does not take focus when the reader clicked away from the page controls', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(1, { detail: LONG }), entry(2)], { hasOlder: true })} empty={null} />)
    screen.getByRole('button', { name: /show more/i }).focus()
    ;(document.activeElement as HTMLElement).blur()
    rerender(<AssistantActivityFeed activity={activity([entry(2)], { hasOlder: true })} empty={null} />)
    expect(document.body).toHaveFocus()
  })
})

describe('AssistantActivityFeed older history', () => {
  test('offers Load older only when there is older history', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5)])} empty={null} />)
    expect(screen.queryByRole('button', { name: /load older/i })).toBeNull()
    rerender(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true })} empty={null} />)
    expect(screen.getByRole('button', { name: /load older/i })).toBeInTheDocument()
  })

  test('clicking Load older asks for the page once; a busy button ignores repeat clicks and keeps focus', () => {
    const loadOlder = vi.fn()
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true, loadOlder })} empty={null} />)
    const button = screen.getByRole('button', { name: /load older/i })
    button.focus()
    fireEvent.click(button)
    expect(loadOlder).toHaveBeenCalledTimes(1)

    rerender(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true, loadingOlder: true, loadOlder })} empty={null} />)
    const busy = screen.getByRole('button', { name: /loading/i })
    expect(busy).toHaveAttribute('aria-busy', 'true')
    expect(busy).toHaveAttribute('aria-disabled', 'true')
    expect(busy).not.toBeDisabled()
    fireEvent.click(busy)
    expect(loadOlder).toHaveBeenCalledTimes(1)
    expect(busy).toHaveFocus()
  })

  test('prepending an older page does not move what the reader is looking at', () => {
    mockRowOffsets()
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5), entry(6)], { hasOlder: true })} empty={null} />)
    const region = scrollRegion()
    region.scrollTop = 40
    fireEvent.scroll(region)

    fireEvent.click(screen.getByRole('button', { name: /load older/i }))
    rerender(<AssistantActivityFeed activity={activity([entry(2), entry(3), entry(4), entry(5), entry(6)], { hasOlder: true })} empty={null} />)

    expect(region.scrollTop).toBe(40 + 300)
  })

  test('live rows appended while the older page loads do not shift the reader', () => {
    mockRowOffsets()
    let rows = 2
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5), entry(6)], { hasOlder: true })} empty={null} />)
    const region = scrollRegion()
    setMetrics(region, { scrollHeight: () => rows * 100, clientHeight: 150 })
    region.scrollTop = 40
    fireEvent.scroll(region)

    fireEvent.click(screen.getByRole('button', { name: /load older/i }))
    rows = 3
    rerender(<AssistantActivityFeed activity={activity([entry(5), entry(6), entry(7)], { hasOlder: true, loadingOlder: true })} empty={null} />)
    rows = 7
    // The older page and one more live row land in the same commit.
    rerender(
      <AssistantActivityFeed activity={activity([entry(2), entry(3), entry(4), entry(5), entry(6), entry(7), entry(8)], { hasOlder: true })} empty={null} />,
    )

    expect(region.scrollTop).toBe(40 + 300)
  })

  test('a reader at the very bottom is released before older rows arrive, so the feed does not jump down', async () => {
    mockRowOffsets()
    let rows = 2
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5), entry(6)], { hasOlder: true })} empty={null} />)
    const region = scrollRegion()
    setMetrics(region, { scrollHeight: () => rows * 100, clientHeight: 200 })
    region.scrollTop = 0

    fireEvent.click(screen.getByRole('button', { name: /load older/i }))
    rows = 4
    rerender(<AssistantActivityFeed activity={activity([entry(3), entry(4), entry(5), entry(6)], { hasOlder: true })} empty={null} />)

    // Following the bottom is observed asynchronously: give it the chance to (wrongly) scroll.
    await act(async () => { await Promise.resolve() })
    expect(region.scrollTop).toBe(200)
  })

  test('when the history runs out the button is replaced by a note that takes focus', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true })} empty={null} />)
    const button = screen.getByRole('button', { name: /load older/i })
    button.focus()
    fireEvent.click(button)

    rerender(<AssistantActivityFeed activity={activity([entry(1), entry(5)], { hasOlder: false })} empty={null} />)
    expect(screen.queryByRole('button', { name: /load older/i })).toBeNull()
    expect(screen.getByText('Start of activity')).toHaveFocus()
  })

  test('the note does not steal focus from somewhere else', () => {
    const { rerender } = render(
      <>
        <button type="button">elsewhere</button>
        <AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true })} empty={null} />
      </>,
    )
    fireEvent.click(screen.getByRole('button', { name: /load older/i }))
    screen.getByRole('button', { name: 'elsewhere' }).focus()
    rerender(
      <>
        <button type="button">elsewhere</button>
        <AssistantActivityFeed activity={activity([entry(1), entry(5)], { hasOlder: false })} empty={null} />
      </>,
    )
    expect(screen.getByRole('button', { name: 'elsewhere' })).toHaveFocus()
  })

  test('a feed that already shows the start says so without moving focus', () => {
    render(<AssistantActivityFeed activity={activity([entry(1)])} empty={null} />)
    expect(screen.getByText('Start of activity')).not.toHaveFocus()
  })

  test('at the bound Load older is replaced by an accurate note instead of a dead button', () => {
    render(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true, olderCapped: true })} empty={null} />)
    expect(screen.queryByRole('button', { name: /load older/i })).toBeNull()
    expect(screen.getByText('Showing the latest 2000 actions. Earlier actions are not shown.')).toBeInTheDocument()
    expect(screen.queryByText('Start of activity')).toBeNull()
  })

  test('focus stays in the feed when Load older reaches the bound', () => {
    const { rerender } = render(<AssistantActivityFeed activity={activity([entry(5)], { hasOlder: true })} empty={null} />)
    const button = screen.getByRole('button', { name: /load older/i })
    button.focus()
    fireEvent.click(button)

    rerender(<AssistantActivityFeed activity={activity([entry(4), entry(5)], { hasOlder: true, olderCapped: true })} empty={null} />)
    expect(screen.getByText(/Earlier actions are not shown/)).toHaveFocus()
  })

  test('at the latest rows and pinned to the bottom there is no Jump to latest', () => {
    render(<AssistantActivityFeed activity={activity([entry(1)], { hasOlder: true })} empty={null} />)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })
})
