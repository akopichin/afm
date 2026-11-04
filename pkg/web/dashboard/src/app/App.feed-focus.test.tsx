import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { App } from './App'
import * as api from '../api/run-client'

class Socket {
  static last: Socket
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() { Socket.last = this }
  close() {}
}

type StageRow = [id: string, status: string]
const stageView = ([id, status]: StageRow) => ({
  id, name: id, status, updated_at: '2026-11-01T10:00:00Z', show_plan: true,
  show_dialog: status === 'awaiting_user_input', has_dialog: status === 'awaiting_user_input',
})
const question = { id: 'q1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'request', body: 'Please decide', seq: 1, created_at: '2026-11-01T10:00:00Z' }
const runThread = { id: 'mt-run', title: 'Run question', scope: 'run', members: ['user', 'run:qa'], status: 'open' }

// A team-mode run with the mesh on: the stage Feed has its composer, conversations and the People panel.
// The `assistants` mode replaces Team with the Run conversations panel, which lists the run's own thread.
function setup(stages: StageRow[] = [['s1', 'running'], ['s2', 'pending']], mode: 'team' | 'assistants' = 'team') {
  const state = { stages }
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    const json = (value: unknown) => ({ ok: true, json: async () => value }) as Response
    if (url.includes('/api/status')) return json({ flow_name: 'focus', run_id: 'focus-run', collaboration_mode: mode, flow_pause_state: 'none', stages: state.stages.map(stageView), capabilities: { collaboration: true }, accounting: { health: 'ok', has_data: false, show_money: true } })
    if (url.includes('/api/mesh/participants')) return json({ participants: [
      { id: 'user', kind: 'user', scope: 'run', reachable: true },
      { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', reachable: true },
      { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true },
    ] })
    if (url.includes('/api/mesh/stages/s1/messages')) return json({ messages: [question], threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'stage:s1', 'run:qa'], status: 'open' }], next_cursor: 1, has_more: false })
    if (url.includes('/api/mesh/stages/')) return json({ messages: [], threads: [], next_cursor: 0, has_more: false })
    if (url.includes('/api/mesh/board')) return json({ entries: [] })
    if (/\/api\/mesh\/threads\/[^/?]+/.test(url)) return json({ messages: [], next_cursor: 0 })
    if (url.includes('/api/mesh/threads')) return json({ threads: mode === 'assistants' ? [runThread] : [] })
    if (url.includes('/api/events')) return json([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-11-01T10:00:00Z', data: { tool: 'text', detail: 'Existing update' } }])
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    return json([])
  })
  return state
}
async function refreshStatus() {
  await act(async () => Socket.last.onmessage?.({ data: JSON.stringify({ type: 'revised', stage_id: 's1' }) }))
}

// Pins the viewport for the 1180px drawers (People, Run conversations); the Stages rail stays beside the workspace.
function stubDrawerViewport(narrow: boolean) {
  const listeners = new Set<() => void>()
  let isNarrow = narrow
  vi.stubGlobal('matchMedia', (query: string) => ({
    media: query,
    get matches() { return isNarrow && query.includes('1180') },
    addEventListener: (_: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
  }))
  return { resize(next: boolean) { act(() => { isNarrow = next; listeners.forEach((notify) => notify()) }) } }
}

const composer = () => screen.getByRole('combobox', { name: 'Message the mesh' }) as HTMLTextAreaElement
const tab = (name: string | RegExp) => within(screen.getByRole('tablist', { name: 'Workspace' })).getByRole('tab', { name })
const header = () => document.getElementById('header') as HTMLElement
const shell = () => document.querySelector('.dashboard-body') as HTMLElement
const inFocus = () => shell().classList.contains('dashboard-body--feed-focus')
const pressF = (target: Element = document.activeElement ?? document.body) => fireEvent.keyDown(target, { key: 'f', code: 'KeyF' })
const pressEscape = (target: Element = document.activeElement ?? document.body) => fireEvent.keyDown(target, { key: 'Escape', code: 'Escape' })
// focus — moving focus updates App state (useIsEditing), like any other user event.
const focus = (el: HTMLElement) => act(() => el.focus())
async function openStageFeed(): Promise<void> {
  await screen.findByRole('button', { name: '↩ Reply in conversation' })
}

beforeEach(() => {
  vi.stubGlobal('WebSocket', Socket)
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:focus-image')
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

it('Focus hides the run chrome and keeps the same Feed, composer and draft context in both directions', async () => {
  setup()
  vi.spyOn(api, 'uploadAttachment').mockResolvedValue({ path: '/attachments/shot.png' })
  render(<App />)
  fireEvent.click(await screen.findByRole('button', { name: '↩ Reply in conversation' }))
  const input = composer()
  fireEvent.change(input, { target: { value: 'keep this draft' } })
  fireEvent.paste(input, { clipboardData: { items: [{ kind: 'file', type: 'image/png', getAsFile: () => new File(['x'], 'shot.png', { type: 'image/png' }) }] } })
  await waitFor(() => expect(input.value).toContain('[Screenshot:'))
  fireEvent.click(within(screen.getByRole('navigation', { name: 'People' })).getByText('QA reviewer'))
  input.style.height = '110px'
  input.setSelectionRange(2, 6)
  const draft = input.value
  const feed = document.querySelector('.feed-scroll')
  const people = document.getElementById('stage-people')
  const rail = document.getElementById('rail-slot')
  function expectContextKept(): void {
    expect(composer()).toBe(input)
    expect(input).toHaveValue(draft)
    expect([input.selectionStart, input.selectionEnd]).toEqual([2, 6])
    expect(input.style.height).toBe('110px')
    expect(screen.getByRole('button', { name: 'Remove reply in conversation' })).toBeInTheDocument()
    expect(screen.getByAltText('Pasted screenshot')).toBeInTheDocument()
    expect(within(screen.getByRole('group', { name: 'Recipients' })).getByText('@qa')).toBeInTheDocument()
    expect(document.querySelector('.feed-scroll')).toBe(feed)
    expect(document.getElementById('stage-people')).toBe(people)
    expect(document.getElementById('rail-slot')).toBe(rail)
  }

  fireEvent.click(screen.getByRole('button', { name: 'Focus on Feed' }))
  expect(inFocus()).toBe(true)
  expect(header()).toHaveAttribute('hidden')
  expect(screen.getByRole('button', { name: 'Exit Feed focus' })).toHaveAttribute('aria-pressed', 'true')
  expectContextKept()

  fireEvent.click(screen.getByRole('button', { name: 'Exit Feed focus' }))
  expect(inFocus()).toBe(false)
  expect(header()).not.toHaveAttribute('hidden')
  expectContextKept()
})

it('F types into the composer; Escape from the composer leaves focus mode and keeps typing there', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  const input = composer()
  focus(input)
  expect(pressF(input)).toBe(true)
  expect(inFocus()).toBe(false)

  pressF(document.body)
  expect(inFocus()).toBe(true)
  focus(input)
  fireEvent.change(input, { target: { value: 'draft' } })
  expect(pressEscape(input)).toBe(false)
  expect(inFocus()).toBe(false)
  expect(document.activeElement).toBe(input)
  expect(input).toHaveValue('draft')
})

it('Escape closes the mention menu first and leaves focus mode on the next press', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  pressF(document.body)
  const input = composer()
  focus(input)
  fireEvent.change(input, { target: { value: '@' } })
  expect(await screen.findByRole('listbox')).toBeInTheDocument()

  pressEscape(input)
  expect(screen.queryByRole('listbox')).toBeNull()
  expect(inFocus()).toBe(true)

  pressEscape(input)
  expect(inFocus()).toBe(false)
})

it('Escape closes the composer attachment menu first and leaves focus mode on the next press', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  pressF(document.body)
  const attach = within(document.querySelector('.feed-composer') as HTMLElement).getByRole('button', { name: 'Attach' })
  focus(attach)
  fireEvent.click(attach)
  expect(screen.getByRole('menu')).toBeInTheDocument()

  pressEscape(attach)
  expect(screen.queryByRole('menu')).toBeNull()
  expect(inFocus()).toBe(true)

  pressEscape(attach)
  expect(inFocus()).toBe(false)
})

it('an open modal owns F and Escape: it closes first and focus mode stays until the next Escape', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  pressF(document.body)
  fireEvent.click(within(screen.getByRole('navigation', { name: 'People' })).getByRole('button', { name: 'Add run agent…' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add run agent' })
  const control = within(dialog).getByRole('button', { name: 'Cancel' })
  focus(control)

  pressF(control)
  expect(inFocus()).toBe(true)
  pressEscape(control)
  expect(screen.queryByRole('dialog', { name: 'Add run agent' })).toBeNull()
  expect(inFocus()).toBe(true)

  pressEscape(document.body)
  expect(inFocus()).toBe(false)
})

it('F outside the stage Feed does nothing', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  fireEvent.click(tab('Team'))
  pressF(document.body)
  expect(inFocus()).toBe(false)
  expect(screen.queryByRole('button', { name: 'Focus on Feed' })).toBeNull()
})

it('F from a Stages rail item enters focus mode, hands keyboard focus to Exit focus, and exit restores the panels', async () => {
  setup()
  render(<App />)
  await openStageFeed()
  const row = document.querySelector<HTMLElement>('[data-stage-id="s1"] .stage-row')!
  focus(row)
  pressF(row)
  expect(inFocus()).toBe(true)
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Exit Feed focus' }))

  pressF()
  expect(inFocus()).toBe(false)
  expect(document.getElementById('rail-slot')).toHaveProperty('inert', false)
  expect(document.getElementById('stage-people')).toHaveClass('open')
  for (const node of document.querySelectorAll<HTMLElement>('.workspace, .stage-feed-layout, .feed-workspace, #stage-people')) {
    expect(node.closest('[inert]')).toBeNull()
  }
  expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Focus on Feed' }))
})

it('entering focus mode closes the People drawer; it stays closed after exit and opens again from People', async () => {
  stubDrawerViewport(true)
  setup()
  render(<App />)
  await openStageFeed()
  fireEvent.click(screen.getByRole('button', { name: 'Show people' }))
  expect(document.getElementById('stage-people')).toHaveClass('open')

  fireEvent.click(screen.getByRole('button', { name: 'Focus on Feed' }))
  expect(document.getElementById('stage-people')).not.toHaveClass('open')
  pressEscape(document.body)
  expect(inFocus()).toBe(false)
  expect(document.getElementById('stage-people')).not.toHaveClass('open')

  fireEvent.click(screen.getByRole('button', { name: 'Show people' }))
  expect(screen.getByRole('dialog', { name: 'Stage people' })).toBeInTheDocument()
})

// Opens Run conversations on a wide screen, starts an answer in its conversation and enters focus mode.
async function focusWithRunConversationsOpen(): Promise<{ panel: HTMLElement; draft: HTMLTextAreaElement }> {
  fireEvent.click(await screen.findByRole('button', { name: /^Run conversations/ }))
  const panel = document.getElementById('run-conversations') as HTMLElement
  fireEvent.click(await within(panel).findByText('Run question'))
  const draft = await within(panel).findByPlaceholderText<HTMLTextAreaElement>(/^Message /)
  fireEvent.change(draft, { target: { value: 'my answer' } })
  fireEvent.click(screen.getByRole('button', { name: 'Focus on Feed' }))
  expect(inFocus()).toBe(true)
  return { panel, draft }
}

// After focus mode the panel is back as the same open drawer, with its conversation and draft.
function expectRunConversationsDrawer(panel: HTMLElement, draft: HTMLTextAreaElement): void {
  expect(document.getElementById('run-conversations')).toBe(panel)
  expect(panel).toHaveClass('open')
  expect(panel).toHaveAttribute('role', 'dialog')
  expect(panel).toHaveAttribute('aria-modal', 'true')
  expect(document.querySelector('.run-conversations-scrim')).not.toBeNull()
  expect(within(panel).getByPlaceholderText(/^Message /)).toBe(draft)
  expect(draft).toHaveValue('my answer')
}

it('Run conversations resized into a drawer in focus mode is not a modal: F leaves focus and the drawer comes back', async () => {
  const viewport = stubDrawerViewport(false)
  setup(undefined, 'assistants')
  render(<App />)
  const { panel, draft } = await focusWithRunConversationsOpen()

  viewport.resize(true)
  expect(panel).not.toHaveAttribute('role')
  expect(panel).not.toHaveAttribute('aria-modal')
  expect(document.querySelector('.run-conversations-scrim')).toBeNull()
  expect(screen.getByRole('button', { name: /^Run conversations/ })).toHaveAttribute('aria-expanded', 'true')

  pressF(document.body)
  expect(inFocus()).toBe(false)
  expectRunConversationsDrawer(panel, draft)
  expect(panel.contains(document.activeElement)).toBe(true)
})

it('Run conversations resized into a drawer in focus mode leaves the first Escape to focus mode and stays open', async () => {
  const viewport = stubDrawerViewport(false)
  setup(undefined, 'assistants')
  render(<App />)
  const { panel, draft } = await focusWithRunConversationsOpen()
  viewport.resize(true)

  pressEscape(document.body)
  expect(inFocus()).toBe(false)
  expectRunConversationsDrawer(panel, draft)
})

it('Run conversations resized into a drawer in focus mode does not hold back a new attention item', async () => {
  const viewport = stubDrawerViewport(false)
  const state = setup([['s1', 'running'], ['s2', 'running']], 'assistants')
  render(<App />)
  const { panel, draft } = await focusWithRunConversationsOpen()
  viewport.resize(true)

  state.stages = [['s1', 'running'], ['s2', 'failed']]
  await refreshStatus()
  await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('s2'))
  expect(inFocus()).toBe(false)
  expectRunConversationsDrawer(panel, draft)
})

it.each([
  ['awaiting_approval', 'Approval', 'plan-section'],
  ['awaiting_user_input', 'Question', 'dialog-section'],
])('a waiting %s stays reachable from the stage toolbar in focus mode and its view keeps focus mode with a way back to the Feed', async (status, label, section) => {
  setup([['s1', 'running'], ['s3', status]])
  render(<App />)
  // The waiting stage auto-opens first; the user returns to the Feed of the running stage.
  await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('s3'))
  fireEvent.click(tab('Feed'))
  fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row')!)
  await openStageFeed()

  fireEvent.click(screen.getByRole('button', { name: 'Focus on Feed' }))
  const wait = within(document.querySelector('.workspace-header') as HTMLElement).getByRole('button', { name: new RegExp(label) })
  expect(wait).toHaveTextContent(`${label}1`)
  fireEvent.click(wait)

  await waitFor(() => expect(document.getElementById(section)).not.toBeNull())
  expect(document.getElementById('detail-title')).toHaveTextContent('s3')
  expect(inFocus()).toBe(true)
  expect(header()).toHaveAttribute('hidden')
  expect(screen.getByRole('button', { name: 'Exit focus' })).toHaveAttribute('aria-pressed', 'true')

  // The tab strip is hidden: the toolbar brings the Feed back, and focus mode stays on.
  fireEvent.click(screen.getByRole('button', { name: 'Feed' }))
  expect(document.getElementById(section)).toBeNull()
  expect(inFocus()).toBe(true)
  expect(screen.getByRole('button', { name: 'Exit Feed focus' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Feed' })).toBeNull()
})

it.each([
  ['awaiting_approval', 'plan-section'],
  ['awaiting_user_input', 'dialog-section'],
])('F and Escape work on the %s view and the Focus button names the view', async (status, section) => {
  setup([['s1', 'running'], ['s3', status]])
  render(<App />)
  await waitFor(() => expect(document.getElementById(section)).not.toBeNull())
  expect(inFocus()).toBe(false)

  fireEvent.click(screen.getByRole('button', { name: 'Focus on this view' }))
  expect(inFocus()).toBe(true)
  expect(document.getElementById(section)).not.toBeNull()
  pressEscape(document.body)
  expect(inFocus()).toBe(false)
  pressF(document.body)
  expect(inFocus()).toBe(true)
  pressF(document.body)
  expect(inFocus()).toBe(false)
})

it('a Paused or Failed view and the global views still end focus mode and do not offer it', async () => {
  const state = setup([['s1', 'running'], ['s2', 'running']])
  render(<App />)
  await openStageFeed()
  pressF(document.body)
  expect(inFocus()).toBe(true)
  state.stages = [['s1', 'running'], ['s2', 'paused']]
  await refreshStatus()
  await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('s2'))
  expect(inFocus()).toBe(false)
  pressF(document.body)
  expect(inFocus()).toBe(false)
  expect(screen.queryByRole('button', { name: /Focus on/ })).toBeNull()

  fireEvent.click(tab('Team'))
  pressF(document.body)
  expect(inFocus()).toBe(false)
})

it('a new attention view ends focus mode, and returning to the Feed does not restore it', async () => {
  const state = setup([['s1', 'running'], ['s2', 'running']])
  render(<App />)
  await openStageFeed()
  pressF(document.body)
  expect(inFocus()).toBe(true)

  state.stages = [['s1', 'running'], ['s2', 'failed']]
  await refreshStatus()
  await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('s2'))
  expect(inFocus()).toBe(false)

  fireEvent.click(tab('Feed'))
  expect(inFocus()).toBe(false)
  expect(await screen.findByText('No events for this stage yet')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Focus on Feed' })).toHaveAttribute('aria-pressed', 'false')
})
