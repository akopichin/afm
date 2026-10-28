import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { AssistantTaskView } from './AssistantTaskView'
import type { AssistantTask } from '../../types/assistant-task'
import type { AssistantActivityEntry } from '../../types/assistant-activity'

const ROW_PX = 34
const VIEW_PX = 581

// jsdom has no layout: rows are ROW_PX tall, the scroll region shows VIEW_PX, scrollTop is clamped to the
// content, and every programmatic scrollTop write queues one scroll event for the next frame, as a browser does
// — the event reads the position at that time, whatever it was written to in between.
function mockLayout(): void {
  const isRegion = (el: HTMLElement) => el.classList.contains('assistant-activity-scroll')
  const contentHeight = (region: HTMLElement) => region.children.length * ROW_PX
  const maxTop = (region: HTMLElement) => Math.max(0, contentHeight(region) - VIEW_PX)
  const tops = new WeakMap<HTMLElement, number>()
  const pendingEvent = new WeakSet<HTMLElement>()

  vi.spyOn(HTMLElement.prototype, 'offsetTop', 'get').mockImplementation(function (this: HTMLElement) {
    return this.parentElement === null ? 0 : Array.from(this.parentElement.children).indexOf(this) * ROW_PX
  })
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
    return isRegion(this) ? 0 : ROW_PX
  })
  vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(function (this: HTMLElement) {
    return isRegion(this) ? contentHeight(this) : 0
  })
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(function (this: HTMLElement) {
    return isRegion(this) ? VIEW_PX : 0
  })
  vi.spyOn(HTMLElement.prototype, 'scrollTop', 'get').mockImplementation(function (this: HTMLElement) {
    return isRegion(this) ? Math.min(tops.get(this) ?? 0, maxTop(this)) : 0
  })
  vi.spyOn(HTMLElement.prototype, 'scrollTop', 'set').mockImplementation(function (this: HTMLElement, value: number) {
    if (!isRegion(this)) return
    tops.set(this, Math.max(0, Math.min(value, maxTop(this))))
    if (pendingEvent.has(this)) return
    pendingEvent.add(this)
    setTimeout(() => {
      pendingEvent.delete(this)
      this.dispatchEvent(new Event('scroll'))
    }, 0)
  })
}

// One browser frame: the queued scroll events are delivered.
const frame = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 5)) })

function makeTask(id: string): AssistantTask {
  return {
    id, parentTaskId: null, profileId: 'reviewer', title: id, mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
  }
}

const live = (n: number, taskId: string): AssistantActivityEntry => ({
  eventId: `${taskId}-${n}`, taskId, participantId: '', turn: 1, occurredAt: new Date().toISOString(),
  tool: 'Bash', detail: `cmd ${n}`, truncated: false, cursor: undefined,
})
const ringOf = (taskId: string, count: number) => Array.from({ length: count }, (_, i) => live(i + 1, taskId))
const saved = (n: number, taskId: string) => ({
  cursor: String(n), event_id: `${taskId}-${n}`, task_id: taskId, tool: 'Bash', detail: `cmd ${n}`, turn: 1,
  occurred_at: new Date().toISOString(),
})

const json = (body: unknown): Response => new Response(JSON.stringify(body), { status: 200 })

// Each helper has saved 1..total; its tail request answers the latest 100 rows once `answer` releases the gate.
function mockTails(totals: Record<string, number>): { answer: () => Promise<void> } {
  let release: () => void = () => {}
  const gate = new Promise<void>((resolve) => { release = resolve })
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = typeof input === 'string' ? input : (input as Request).url
    const taskId = /assistants\/([^/]+)\/activity/.exec(url)?.[1]
    if (taskId === undefined) return json(url.includes('/events') ? { events: [], next_cursor: 0, has_more: false } : {})
    await gate
    const total = totals[taskId] ?? 0
    const rows = Array.from({ length: Math.min(100, total) }, (_, i) => saved(total - Math.min(100, total) + 1 + i, taskId))
    return json({ entries: rows, after: String(total), before: String(total - rows.length), has_more: false, has_older: total > 100 })
  })
  return { answer: () => act(async () => { release() }) }
}

const region = () => screen.getByRole('region', { name: 'Helper activity' })
const atEnd = () => region().scrollTop === region().scrollHeight - VIEW_PX
const jumpButton = () => screen.queryByRole('button', { name: 'Jump to latest' })

function view(taskId: string, ring: AssistantActivityEntry[]) {
  return <AssistantTaskView task={makeTask(taskId)} runId="run-1" connected liveActivity={ring} />
}

afterEach(() => vi.restoreAllMocks())

describe('AssistantTaskView feed following', () => {
  test('a helper opened with a live ring longer than one tail page ends at the latest row and follows', async () => {
    mockLayout()
    const api = mockTails({ t1: 170 })
    const ring = ringOf('t1', 170)
    const { rerender } = render(view('t1', ring))
    await frame()
    expect(atEnd()).toBe(true)

    await api.answer()
    await frame()
    expect(await screen.findByText('Load older activity')).toBeInTheDocument()
    expect(atEnd()).toBe(true)
    expect(jumpButton()).toBeNull()

    rerender(view('t1', [...ring, live(171, 't1')]))
    await frame()
    expect(screen.getByText('cmd 171')).toBeInTheDocument()
    expect(atEnd()).toBe(true)
  })

  test('the same when the tail answer comes before the first frame', async () => {
    mockLayout()
    const api = mockTails({ t1: 170 })
    render(view('t1', ringOf('t1', 170)))

    await api.answer()
    await frame()

    expect(atEnd()).toBe(true)
    expect(jumpButton()).toBeNull()
  })

  test('switching to another helper and back opens at the latest row again', async () => {
    mockLayout()
    const api = mockTails({ t1: 170, t2: 3 })
    const ring = [...ringOf('t1', 170), ...ringOf('t2', 3)]
    const { rerender } = render(view('t1', ring))
    await api.answer()
    await frame()

    rerender(view('t2', ring))
    await frame()
    rerender(view('t1', ring))
    await frame()
    expect(atEnd()).toBe(true)
    await frame()

    rerender(view('t1', [...ring, live(171, 't1')]))
    await frame()
    expect(atEnd()).toBe(true)
    expect(jumpButton()).toBeNull()
  })

  test('a reader who scrolled up is not pulled back down by new rows', async () => {
    mockLayout()
    const api = mockTails({ t1: 170 })
    const ring = ringOf('t1', 170)
    const { rerender } = render(view('t1', ring))
    await api.answer()
    await frame()

    act(() => { region().scrollTop = 500 })
    await frame()
    expect(jumpButton()).not.toBeNull()

    rerender(view('t1', [...ring, live(171, 't1')]))
    await frame()
    expect(region().scrollTop).toBe(500)
  })
})
