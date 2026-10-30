import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { App } from './App'
import * as api from '../api/run-client'
import * as workspace from '../hooks/use-workspace-view'
import * as markdown from '../components/plan-panel/markdown'

class Socket {
  static last: Socket
  onmessage: ((event: { data: string }) => void) | null = null
  constructor() { Socket.last = this }
  close() {}
}
const stage = (id: string, status = 'running') => ({ id, name: id, status, updated_at: '2026-10-29T10:00:00Z', show_plan: true, show_dialog: true, has_dialog: true })
const question = { id: 'q1', thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: ['user'], kind: 'request', body: 'Please decide', seq: 1, created_at: '2026-10-29T10:00:00Z' }
function setup() {
  const state = { runId: 'correction-a', status: 'running', updatedAt: '2026-10-29T10:00:00Z', flowPause: 'none', showDialog: true }
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input)
    const json = (value: unknown) => ({ ok: true, json: async () => value }) as Response
    if (url.includes('/api/status')) return json({ flow_name: 'regression', run_id: state.runId, collaboration_mode: 'team', flow_pause_state: state.flowPause, stages: [{ ...stage('s1', state.status), updated_at: state.updatedAt, show_dialog: state.showDialog }, stage('s2', 'pending')], capabilities: { collaboration: true }, accounting: { health: 'ok', has_data: false, show_money: true } })
    if (url.includes('/api/mesh/participants')) return json({ participants: [
      { id: 'user', kind: 'user', scope: 'run', reachable: true },
      { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', reachable: true },
      { id: 'run:qa', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA reviewer', reachable: true },
    ] })
    if (url.includes('/api/mesh/stages/s1/messages')) return json({ messages: [question], threads: [{ id: 'mt-1', scope: 'run', members: ['user', 'stage:s1', 'run:qa'], status: 'open' }], next_cursor: 1, has_more: false })
    if (url.includes('/api/mesh/stages/')) return json({ messages: [], threads: [], next_cursor: 0, has_more: false })
    if (url.includes('/api/mesh/board')) return json({ entries: [] })
    if (url.includes('/api/mesh/threads')) return json({ threads: [] })
    if (url.includes('/api/events')) return json([{ type: 'agent_action', stage_id: 's1', seq: 1, timestamp: '2026-10-29T10:00:00Z', data: { tool: 'text', detail: 'Existing **markdown**' } }])
    if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
    return json([])
  })
  return state
}
function refresh() {
  act(() => Socket.last.onmessage?.({ data: JSON.stringify({ type: 'stage_status_changed', stage_id: 's1', data: { status: 'running' } }) }))
}
async function refreshStatus() {
  const statusCalls = () => vi.mocked(fetch).mock.calls.filter(([url]) => String(url).includes('/api/status')).length
  const before = statusCalls()
  // Consecutive identical status events are deduplicated; revised also triggers a status refresh.
  await act(async () => Socket.last.onmessage?.({ data: JSON.stringify({ type: 'revised', stage_id: 's1' }) }))
  expect(statusCalls()).toBeGreaterThan(before)
}
const composer = () => screen.getByRole('combobox', { name: 'Message the mesh' }) as HTMLTextAreaElement
const tab = (name: string | RegExp) => within(screen.getByRole('tablist', { name: 'Workspace' })).getByRole('tab', { name })
const header = () => within(document.querySelector('.workspace-header') as HTMLElement)
function kebabPause() {
  fireEvent.click(within(document.querySelector('[data-stage-id="s1"]') as HTMLElement).getByRole('button', { name: 'More actions' }))
  fireEvent.click(within(document.querySelector('.stage-kebab-menu') as HTMLElement).getByRole('button', { name: 'Pause' }))
}
function paste(node: HTMLElement) {
  fireEvent.paste(node, { clipboardData: { items: [{ kind: 'file', type: 'image/png', getAsFile: () => new File(['x'], 'shot.png', { type: 'image/png' }) }] } })
}
beforeEach(() => {
  vi.stubGlobal('WebSocket', Socket)
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:app-image')
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

it.each(['running', 'paused', 'failed', 'hook_failed'])('only the real-stage Feed owns header lifecycle controls for %s', async (status) => {
  const state = setup()
  state.status = status
  const original = workspace.useWorkspaceView
  let view: ReturnType<typeof original>['state']['view'] = 'feed'
  vi.spyOn(workspace, 'useWorkspaceView').mockImplementation((...args) => {
    const actual = original(...args)
    return { ...actual, state: { ...actual.state, view } }
  })
  const app = render(<App />)
  const action = status === 'running' ? 'Pause' : status === 'paused' ? 'Continue' : 'Retry'
  await waitFor(() => expect(document.querySelector('.workspace-header')).not.toBeNull())
  expect(within(document.querySelector('.workspace-header') as HTMLElement).getByRole('button', { name: action })).toBeInTheDocument()
  for (const next of ['attention', 'plan-history', 'dialog-history', 'assistant', 'cost', 'team', 'board'] as const) {
    view = next
    app.rerender(<App />)
    const header = document.querySelector('.workspace-header')
    if (header !== null) expect(within(header as HTMLElement).queryByRole('button', { name: action })).toBeNull()
  }
})

it('persists reply/image/mode drafts through People, views and stages, and releases them on run change', async () => {
  const state = setup()
  vi.spyOn(api, 'uploadAttachment').mockResolvedValue({ path: '/attachments/shot.png' })
  render(<App />)
  fireEvent.click(await screen.findByRole('button', { name: '↩ Reply in conversation' }))
  const input = composer()
  fireEvent.change(input, { target: { value: 'my reply' } })
  paste(input)
  await waitFor(() => expect(input.value).toContain('[Screenshot:'))
  const text = input.value
  fireEvent.click(screen.getByRole('button', { name: 'Show people' }))
  expect(composer()).toBe(input)
  fireEvent.click(screen.getByRole('button', { name: 'Show people' }))
  fireEvent.click(screen.getByRole('button', { name: 'Show people' }))
  const people = screen.getByRole('navigation', { name: 'People' })
  fireEvent.click(within(people).getByText('QA reviewer'))
  expect(composer()).toBe(input)
  expect(input).toHaveValue(text)
  expect(screen.getByRole('button', { name: 'Cancel reply in conversation' })).toBeInTheDocument()
  expect(screen.getByAltText('Pasted screenshot')).toHaveAttribute('src', 'blob:app-image')
  expect(within(screen.getByRole('group', { name: 'Recipients' })).getByText('@qa')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  fireEvent.change(screen.getByPlaceholderText('Describe what the agent should change…'), { target: { value: 'independent revise' } })
  for (const name of ['Cost', 'Team']) {
    fireEvent.click(tab(name))
    fireEvent.click(tab('Feed'))
    expect(screen.getByPlaceholderText('Describe what the agent should change…')).toHaveValue('independent revise')
  }
  fireEvent.click(screen.getByRole('tab', { name: 'Message' }))
  fireEvent.click(document.querySelector('[data-stage-id="s2"] .stage-row')!)
  expect(composer()).toHaveValue('')
  fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row')!)
  expect(composer()).toHaveValue(text)
  expect(screen.getByAltText('Pasted screenshot')).toHaveAttribute('src', 'blob:app-image')
  expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:app-image')
  state.runId = 'correction-b'
  refresh()
  await waitFor(() => expect(composer()).toHaveValue(''), { timeout: 4000 })
  expect(screen.queryByAltText('Pasted screenshot')).toBeNull()
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:app-image')
})

it('keeps interrupted uploads retryable after ordinary/reply and stage switches in the App draft store', async () => {
  setup()
  let finish!: (value: { path: string }) => void
  const upload = vi.spyOn(api, 'uploadAttachment').mockImplementationOnce(() => new Promise((resolve) => { finish = resolve })).mockResolvedValue({ path: '/attachments/retried.png' })
  render(<App />)
  await screen.findByRole('button', { name: '↩ Reply in conversation' })
  paste(composer())
  fireEvent.click(screen.getByRole('button', { name: '↩ Reply in conversation' }))
  fireEvent.click(screen.getByRole('button', { name: 'Cancel reply in conversation' }))
  expect(screen.getByRole('button', { name: /Retry upload/ })).toBeInTheDocument()
  await act(async () => finish({ path: '/attachments/stale.png' }))
  expect(composer().value).not.toContain('stale.png')
  fireEvent.click(screen.getByRole('button', { name: /Retry upload/ }))
  await waitFor(() => expect(composer().value).toContain('retried.png'))
  upload.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  paste(composer())
  fireEvent.click(document.querySelector('[data-stage-id="s2"] .stage-row')!)
  fireEvent.click(document.querySelector('[data-stage-id="s1"] .stage-row')!)
  expect(screen.getByRole('button', { name: /Retry upload/ })).toBeInTheDocument()
  await act(async () => finish({ path: '/attachments/stale-stage.png' }))
  expect(composer().value).not.toContain('stale-stage.png')
})

it('does not render existing Feed markdown on Message or Revise keystrokes', async () => {
  setup()
  const renderMarkdown = vi.spyOn(markdown, 'renderPlainMarkdown')
  render(<App />)
  await screen.findByText('Existing', { exact: false })
  await screen.findByRole('button', { name: '↩ Reply in conversation' })
  for (const mode of ['Message', 'Revise']) {
    fireEvent.click(screen.getByRole('tab', { name: mode }))
    const count = renderMarkdown.mock.calls.length
    const input = mode === 'Message' ? composer() : screen.getByPlaceholderText('Describe what the agent should change…')
    for (const value of ['a', 'ab', 'abc']) fireEvent.change(input, { target: { value } })
    expect(renderMarkdown).toHaveBeenCalledTimes(count)
  }
})

it('uses the Hold resume path when an empty blur races header Continue', async () => {
  const state = setup()
  localStorage.setItem('afm.pauseOnFocus', 'true')
  vi.spyOn(api, 'pauseStage').mockImplementation(async () => { state.status = 'paused' })
  let finish!: () => void
  const resume = vi.spyOn(api, 'continueStage').mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
  render(<App />)
  await screen.findByRole('tab', { name: 'Revise' })
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const input = screen.getByPlaceholderText('Describe what the agent should change…')
  fireEvent.focus(input)
  await waitFor(() => expect(api.pauseStage).toHaveBeenCalledTimes(1))
  refresh()
  const header = () => within(document.querySelector('.workspace-header') as HTMLElement)
  await waitFor(() => expect(header().getByRole('button', { name: 'Continue' })).toBeEnabled())
  const button = header().getByRole('button', { name: 'Continue' })
  act(() => { fireEvent.blur(input); fireEvent.click(button) })
  await waitFor(() => expect(resume).toHaveBeenCalledTimes(1))
  await act(async () => finish())
  fireEvent.click(header().getByRole('button', { name: 'Continue' }))
  expect(resume).toHaveBeenCalledTimes(1)
  expect(header().queryByRole('alert')).toBeNull()
})

it('shows the reply-needed count only in the matching stage Feed', async () => {
  setup()
  render(<App />)
  expect(await screen.findByText('1 reply needed')).toBeInTheDocument()
  fireEvent.click(tab('s1'))
  expect(screen.queryByText('1 reply needed')).toBeNull()
  fireEvent.click(tab('Feed'))
  expect(await screen.findByText('1 reply needed')).toBeInTheDocument()
  await act(async () => { fireEvent.click(document.querySelector('[data-stage-id="s2"] .stage-row')!) })
  expect(screen.queryByText('1 reply needed')).toBeNull()
})


it.each(['running', 'failed'])('disables header operations during a flow-level pause for %s', async (status) => {
  const state = setup()
  state.status = status
  state.flowPause = 'paused'
  render(<App />)
  await screen.findByRole('tab', { name: 'Feed' })
  fireEvent.click(tab('Feed'))
  const header = document.querySelector('.workspace-header') as HTMLElement
  expect(within(header).getByRole('button', { name: status === 'running' ? 'Pause' : 'Retry' })).toBeDisabled()
})

it.each([
  ['hook_failed', 'Skip', 'Retry', 'skipHookStage'],
  ['paused', 'Continue', 'Continue', 'continueStage'],
] as const)('shares a pending %s Plan action with Feed and keeps it locked after success', async (status, planAction, feedAction, endpoint) => {
  const state = setup()
  state.status = status
  let finish!: () => void
  const request = vi.spyOn(api, endpoint).mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
  const retryHook = vi.spyOn(api, 'retryHookStage').mockResolvedValue(undefined)
  render(<App />)
  const planButton = await screen.findByRole('button', { name: planAction })
  expect(planButton).toHaveAttribute('id', planAction === 'Skip' ? 'btn-skip-hook' : 'btn-continue')
  fireEvent.click(planButton)
  expect(request).toHaveBeenCalledTimes(1)
  fireEvent.click(tab('Feed'))
  const header = () => within(document.querySelector('.workspace-header') as HTMLElement)
  expect(header().getByRole('button', { name: feedAction })).toBeDisabled()
  fireEvent.click(header().getByRole('button', { name: feedAction }))
  await act(async () => finish())
  expect(header().getByRole('button', { name: feedAction })).toBeDisabled()
  await refreshStatus()
  expect(header().getByRole('button', { name: feedAction })).toBeDisabled()
  fireEvent.click(tab(status === 'paused' ? /^Paused/ : /^Hook failed/))
  expect(screen.getByRole('button', { name: planAction })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: planAction }))
  expect(request).toHaveBeenCalledTimes(1)
  expect(retryHook).not.toHaveBeenCalled()
  state.updatedAt = '2026-10-29T10:01:00Z'
  await refreshStatus()
  expect(screen.getByRole('button', { name: planAction })).toBeEnabled()
  fireEvent.click(tab('Feed'))
  expect(header().getByRole('button', { name: feedAction })).toBeEnabled()
})

it.each(['header', 'kebab'] as const)('shares Pause between header and kebab with %s first, until a new episode', async (first) => {
  const state = setup()
  let finish!: () => void
  const pause = vi.spyOn(api, 'pauseStage').mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve })).mockResolvedValue(undefined)
  await act(async () => { render(<App />) })
  const clickHeader = () => fireEvent.click(header().getByRole('button', { name: 'Pause' }))
  if (first === 'header') { clickHeader(); kebabPause() }
  else { kebabPause(); clickHeader() }
  expect(pause).toHaveBeenCalledTimes(1)
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
  await act(async () => finish())
  await refreshStatus()
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
  kebabPause()
  expect(pause).toHaveBeenCalledTimes(1)
  state.updatedAt = '2026-10-29T10:01:00Z'
  await refreshStatus()
  expect(header().getByRole('button', { name: 'Pause' })).toBeEnabled()
  kebabPause()
  await act(async () => {})
  expect(pause).toHaveBeenCalledTimes(2)
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
})

it('keeps header and kebab Pause behind Hold ownership before the paused status poll', async () => {
  const state = setup()
  localStorage.setItem('afm.pauseOnFocus', 'true')
  let finish!: () => void
  const pause = vi.spyOn(api, 'pauseStage').mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
  const resume = vi.spyOn(api, 'continueStage').mockResolvedValue(undefined)
  await act(async () => { render(<App />) })
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  fireEvent.focus(screen.getByPlaceholderText('Describe what the agent should change…'))
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
  kebabPause()
  expect(pause).toHaveBeenCalledTimes(1)
  await act(async () => finish())
  expect(document.querySelector('#detail-status')).toHaveAttribute('data-status', 'running')
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
  fireEvent.click(header().getByRole('button', { name: 'Pause' }))
  kebabPause()
  expect(pause).toHaveBeenCalledTimes(1)
  state.status = 'paused'
  await refreshStatus()
  expect(header().getByRole('button', { name: 'Continue' })).toBeEnabled()
  await act(async () => { fireEvent.click(header().getByRole('button', { name: 'Continue' })) })
  expect(resume).toHaveBeenCalledTimes(1)
})

it('drops pending Hold ownership on a new run with the same stage id and ignores the old response', async () => {
  const state = setup()
  localStorage.setItem('afm.pauseOnFocus', 'true')
  let finish!: () => void
  const pause = vi.spyOn(api, 'pauseStage').mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve })).mockResolvedValue(undefined)
  const resume = vi.spyOn(api, 'continueStage').mockResolvedValue(undefined)
  await act(async () => { render(<App />) })
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  fireEvent.focus(screen.getByPlaceholderText('Describe what the agent should change…'))
  expect(pause).toHaveBeenCalledTimes(1)
  state.runId = 'correction-b'
  await refreshStatus()
  await act(async () => finish())
  expect(header().getByRole('button', { name: 'Pause' })).toBeEnabled()
  expect(resume).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('tab', { name: 'Revise' }))
  const input = screen.getByPlaceholderText('Describe what the agent should change…')
  await act(async () => { fireEvent.blur(input) })
  expect(resume).not.toHaveBeenCalled()
  await act(async () => { fireEvent.focus(input) })
  expect(pause).toHaveBeenCalledTimes(2)
  expect(header().getByRole('button', { name: 'Pause' })).toBeDisabled()
})

it('routes Plan Continue through Hold when empty blur already started resuming', async () => {
  const state = setup()
  state.showDialog = false
  localStorage.setItem('afm.pauseOnFocus', 'true')
  vi.spyOn(api, 'pauseStage').mockImplementation(async () => { state.status = 'paused' })
  let finish!: () => void
  const resume = vi.spyOn(api, 'continueStage').mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
  render(<App />)
  fireEvent.click(await screen.findByRole('tab', { name: 'Revise' }))
  const input = screen.getByPlaceholderText('Describe what the agent should change…')
  fireEvent.focus(input)
  await waitFor(() => expect(api.pauseStage).toHaveBeenCalledTimes(1))
  refresh()
  await screen.findByRole('button', { name: 'Continue' })
  fireEvent.blur(input)
  await waitFor(() => expect(resume).toHaveBeenCalledTimes(1))
  fireEvent.click(tab(/^Paused/))
  const planButton = screen.getByRole('button', { name: 'Continue' })
  expect(planButton).toHaveAttribute('id', 'btn-continue')
  fireEvent.click(planButton)
  expect(resume).toHaveBeenCalledTimes(1)
  await act(async () => finish())
  fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
  expect(resume).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('alert')).toBeNull()
})

it('resets lifecycle locks on a new run even when stage id, status and updatedAt are identical', async () => {
  const state = setup()
  state.status = 'failed'
  const retry = vi.spyOn(api, 'retryStage').mockResolvedValue(undefined)
  render(<App />)
  fireEvent.click(await screen.findByRole('tab', { name: 'Feed' }))
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry' })) })
  expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled()
  state.runId = 'correction-b'
  refresh()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled())
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry' })) })
  expect(retry).toHaveBeenCalledTimes(2)
  expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled()
})

it.each(['Message', 'Revise'] as const)('%s keeps a later upload retryable when navigation interrupts it before a send succeeds', async (mode) => {
  setup()
  let finishSend!: () => void
  const sending = new Promise<void>((resolve) => { finishSend = resolve })
  vi.spyOn(api, 'postStageDiscussionMessage').mockImplementation(async () => { await sending; return { messageId: 'sent' } })
  vi.spyOn(api, 'reviseStage').mockImplementation(() => sending)
  let finishUpload!: (value: { path: string }) => void
  vi.spyOn(api, 'uploadAttachment').mockImplementationOnce(() => new Promise((resolve) => { finishUpload = resolve }))
    .mockResolvedValue({ path: '/attachments/retried.png' })
  render(<App />)
  fireEvent.click(await screen.findByRole('tab', { name: mode }))
  const input = mode === 'Message' ? composer() : screen.getByPlaceholderText('Describe what the agent should change…')
  fireEvent.change(input, { target: { value: 'Send before the next image' } })
  fireEvent.click(screen.getByRole('button', { name: mode === 'Message' ? 'Send message' : 'Send revision' }))
  paste(input)
  fireEvent.click(tab('Cost'))
  await act(async () => { finishSend(); await sending })
  fireEvent.click(tab('Feed'))
  const retry = screen.getByRole('button', { name: /Retry upload/ })
  expect(screen.getByText('Upload interrupted — retry to attach')).toBeInTheDocument()
  await act(async () => finishUpload({ path: '/attachments/stale.png' }))
  fireEvent.click(retry)
  const restored = mode === 'Message' ? composer() : screen.getByPlaceholderText('Describe what the agent should change…')
  await waitFor(() => expect((restored as HTMLTextAreaElement).value).toContain('/attachments/retried.png'))
  expect((restored as HTMLTextAreaElement).value).not.toContain('stale.png')
})
