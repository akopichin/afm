import { StrictMode } from 'react'
import { act, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { WorkspaceHeader } from './WorkspaceHeader'
import type { Stage } from '../../types'

function stage(overrides: Partial<Stage> = {}): Stage {
  return {
    id: 's1', name: 'Build', status: 'awaiting_approval', updatedAt: '2026-10-29T10:00:00Z', interactive: false,
    autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false,
    isScript: false, pausedFrom: '', preNote: '', buttons: [], ...overrides,
  }
}

afterEach(() => vi.restoreAllMocks())

describe('WorkspaceHeader', () => {
  it('shows the stage name and status', () => {
    render(<WorkspaceHeader stage={stage()} connected={false} />)
    expect(document.getElementById('detail-title')).toHaveTextContent('Build')
    expect(document.getElementById('detail-status')).toHaveTextContent('Awaiting approval')
  })

  it('shows the thinking indicator only while running and connected', () => {
    const { rerender } = render(<WorkspaceHeader stage={stage({ status: 'running' })} connected={false} />)
    expect(screen.queryByText('thinking')).toBeNull()
    rerender(<WorkspaceHeader stage={stage({ status: 'running' })} connected />)
    expect(screen.getByText('thinking')).toBeInTheDocument()
  })

  it('shows the usage-limit countdown next to the status while the stage waits for the limit (the feed view has no plan panel)', () => {
    const quota = { provider: 'claude', window: 'five_hour', source: 'structured', until: '2099-01-01T00:00:00Z', resetAt: '', pauseReason: '' } as const
    const { container, rerender } = render(<WorkspaceHeader stage={stage({ status: 'awaiting_quota', quota })} connected={false} />)
    expect(document.getElementById('detail-status')).toHaveTextContent('Usage limit')
    expect(container.querySelector('.quota-badge')).not.toBeNull()
    rerender(<WorkspaceHeader stage={stage({ status: 'paused', pausedFrom: 'awaiting_quota', quota: { ...quota, pauseReason: 'no_reset' } })} connected={false} />)
    expect(container.querySelector('.quota-badge')).toBeNull()
  })

  it('renders no attention-nav by default', () => {
    render(<WorkspaceHeader stage={stage()} connected={false} />)
    expect(screen.queryByRole('group', { name: /needing attention/i })).toBeNull()
  })

  it('renders prev/next nav with position and wires the callbacks', () => {
    const onPrev = vi.fn()
    const onNext = vi.fn()
    render(<WorkspaceHeader stage={stage()} connected={false} attentionNav={{ pos: 2, total: 3, onPrev, onNext }} />)
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Previous item' }))
    fireEvent.click(screen.getByRole('button', { name: 'Next item' }))
    expect(onPrev).toHaveBeenCalledOnce()
    expect(onNext).toHaveBeenCalledOnce()
  })
})

it('uses the failed-stage and hook retry routes, and shows request failures', async () => {
  const api = await import('../../api/run-client')
  const retry = vi.spyOn(api, 'retryStage').mockRejectedValue(new Error('Retry unavailable'))
  const retryHook = vi.spyOn(api, 'retryHookStage').mockResolvedValue(undefined)
  const { rerender } = render(<StrictMode><WorkspaceHeader stage={stage({ status: 'failed' })} connected /></StrictMode>)
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Retry unavailable')
  expect(retry).toHaveBeenCalledWith('s1')
  rerender(<StrictMode><WorkspaceHeader stage={stage({ status: 'hook_failed' })} connected /></StrictMode>)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry' })) })
  expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled()
  expect(retryHook).toHaveBeenCalledWith('s1')
  retry.mockRestore()
  retryHook.mockRestore()
})

it('pauses and continues through the API and disables actions while status is stale', async () => {
  const api = await import('../../api/run-client')
  const pause = vi.spyOn(api, 'pauseStage').mockResolvedValue(undefined)
  const resume = vi.spyOn(api, 'continueStage').mockResolvedValue(undefined)
  const { rerender } = render(<WorkspaceHeader stage={stage({ status: 'running' })} connected replyNeeded={2} />)
  expect(screen.getByRole('status')).toHaveTextContent('2 replies needed')
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Pause' })) })
  expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
  expect(pause).toHaveBeenCalledWith('s1')
  rerender(<WorkspaceHeader stage={stage({ status: 'paused' })} connected statusAvailable={false} />)
  expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled()
  rerender(<WorkspaceHeader stage={stage({ status: 'paused' })} connected />)
  expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled()
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Continue' })) })
  expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled()
  expect(resume).toHaveBeenCalledWith('s1')
  expect(screen.queryByText('thinking')).toBeNull()
  pause.mockRestore()
  resume.mockRestore()
})


it('offers Pause for lifecycle waits but never for an executing script', () => {
  const { rerender } = render(<WorkspaceHeader stage={stage({ status: 'running', isScript: true })} connected />)
  expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
  for (const status of ['planning', 'revising', 'retrying', 'awaiting_peer', 'awaiting_quota'] as const) {
    rerender(<WorkspaceHeader stage={stage({ status })} connected />)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeEnabled()
  }
})

it.each(['failed', 'hook_failed', 'running', 'paused'] as const)('gates %s controls independently on flow pause and stale status', (status) => {
  const { rerender } = render(<WorkspaceHeader stage={stage({ status })} connected controlsEnabled={false} />)
  expect(screen.getByRole('button')).toBeDisabled()
  rerender(<WorkspaceHeader stage={stage({ status })} connected statusAvailable={false} />)
  expect(screen.getByRole('button')).toBeDisabled()
  rerender(<WorkspaceHeader stage={stage({ status })} connected />)
  expect(screen.getByRole('button')).toBeEnabled()
  rerender(<WorkspaceHeader stage={stage({ status })} connected showControls={false} />)
  expect(screen.queryByRole('button')).toBeNull()
})

it.each(['done', 'ready', 'pending'] as const)('has no lifecycle actions for %s', (status) => {
  render(<WorkspaceHeader stage={stage({ status })} connected />)
  expect(screen.queryByRole('button')).toBeNull()
})

it.each(['running', 'paused'] as const)('shows a rejected %s lifecycle operation as an alert', async (status) => {
  const api = await import('../../api/run-client')
  vi.spyOn(api, status === 'running' ? 'pauseStage' : 'continueStage').mockRejectedValue(new Error('Operation refused'))
  render(<WorkspaceHeader stage={stage({ status })} connected />)
  fireEvent.click(screen.getByRole('button'))
  expect(await screen.findByRole('alert')).toHaveTextContent('Operation refused')
  expect(screen.getByRole('button')).toBeEnabled()
})

it('deduplicates Retry clicks through success until a new failed episode arrives', async () => {
  const api = await import('../../api/run-client')
  let finish!: () => void
  const retry = vi.spyOn(api, 'retryStage').mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
  const old = stage({ status: 'failed', updatedAt: '2026-10-29T10:00:00Z' })
  const { rerender } = render(<WorkspaceHeader stage={old} connected />)
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(retry).toHaveBeenCalledTimes(1)
  expect(screen.getByRole('button')).toBeDisabled()
  await act(async () => finish())
  expect(screen.getByRole('button')).toBeDisabled()
  rerender(<WorkspaceHeader stage={old} connected />)
  fireEvent.click(screen.getByRole('button'))
  expect(retry).toHaveBeenCalledTimes(1)
  rerender(<WorkspaceHeader stage={{ ...old, updatedAt: '2026-10-29T10:01:00Z' }} connected />)
  expect(screen.getByRole('button')).toBeEnabled()
  fireEvent.click(screen.getByRole('button'))
  expect(retry).toHaveBeenCalledTimes(2)
  await act(async () => finish())
})
