import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { App } from './App'

class Socket {
  static last: Socket
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() { Socket.last = this }
  close() {}
}

// Failed stages without a plan: the Failed view is their only place until View in Feed is used.
const stageView = (id: string, status = 'failed') => ({ id, name: id, status, updated_at: '2026-11-01T10:00:00Z', show_plan: false, show_dialog: false })
const agentAction = (stageId: string, seq: number, detail: string) =>
  ({ type: 'agent_action', stage_id: stageId, seq, timestamp: '2026-11-01T10:00:00Z', data: { tool: 'text', detail } })

function setup(stageIds: string[], status = 'failed') {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    const json = (value: unknown) => ({ ok: true, json: async () => value }) as Response
    if (url.includes('/api/status')) return json({ flow_name: 'f', run_id: 'r', flow_pause_state: 'none', stages: stageIds.map((id) => stageView(id, status)), capabilities: {}, accounting: { health: 'ok', has_data: false, show_money: true } })
    if (url.includes('/api/events')) return json(stageIds.map((id, i) => agentAction(id, i + 1, `Event of ${id}`)))
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    return json([])
  })
}

const banner = () => document.querySelector('.attention-banner') as HTMLElement
const viewInFeed = () => within(banner()).getByRole('button', { name: 'View in Feed' })
const activeTab = () => within(screen.getByRole('tablist', { name: 'Workspace' })).getByRole('tab', { selected: true })

beforeEach(() => {
  vi.stubGlobal('WebSocket', Socket)
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

it('View in Feed opens the Feed of a stage that failed before a plan', async () => {
  setup(['s1'])
  render(<App />)
  await screen.findByText('Stage failed')
  fireEvent.click(viewInFeed())
  expect(await screen.findByText('Event of s1')).toBeInTheDocument()
  expect(activeTab()).toHaveTextContent('Feed')
  expect(screen.queryByText('Stage failed')).toBeNull()
})

it('View in Feed follows the attention queue to the stage the Failed view shows', async () => {
  setup(['s1', 's2'])
  render(<App />)
  await screen.findByText('Stage failed')
  // The queue starts on s1; Previous wraps to s2.
  fireEvent.click(screen.getByRole('button', { name: 'Previous item' }))
  expect(document.getElementById('detail-title')).toHaveTextContent('s2')
  fireEvent.click(viewInFeed())
  expect(await screen.findByText('Event of s2')).toBeInTheDocument()
  expect(screen.queryByText('Event of s1')).toBeNull()
})

it('the Feed stays open when the status refreshes and the stage is still failed', async () => {
  setup(['s1'])
  render(<App />)
  await screen.findByText('Stage failed')
  fireEvent.click(viewInFeed())
  await screen.findByText('Event of s1')
  await act(async () => Socket.last.onmessage?.({ data: JSON.stringify({ type: 'revised', stage_id: 's1' }) }))
  await waitFor(() => expect(screen.getByText('Event of s1')).toBeInTheDocument())
  expect(screen.queryByText('Stage failed')).toBeNull()
})

it('View in Feed keeps Feed focus on', async () => {
  setup(['s1'])
  render(<App />)
  await screen.findByText('Stage failed')
  fireEvent.click(screen.getByRole('button', { name: 'Focus on this view' }))
  fireEvent.click(viewInFeed())
  expect(await screen.findByText('Event of s1')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Exit Feed focus' })).toBeInTheDocument()
})

it('Hook failed banner has no View in Feed button', async () => {
  setup(['s1'], 'hook_failed')
  render(<App />)
  await waitFor(() => expect(banner()).toHaveTextContent('Hook failed'))
  expect(within(banner()).queryByRole('button', { name: 'View in Feed' })).toBeNull()
})
