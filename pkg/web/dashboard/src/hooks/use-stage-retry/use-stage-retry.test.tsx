import type { ReactNode } from 'react'
import { act, renderHook } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { createStageActions, StageActionsContext, useStageAction } from './index'
import type { Stage } from '../../types'

const stage = (id: string, status: Stage['status'] = 'failed', updatedAt = '2026-10-29T10:00:00Z') => ({ id, status, updatedAt } as Stage)
function provider() {
  const store = createStageActions()
  return ({ children }: { children: ReactNode }) => <StageActionsContext.Provider value={store}>{children}</StageActionsContext.Provider>
}
function deferred() {
  let resolve!: () => void
  let reject!: (err: Error) => void
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

it('shares one lifecycle command across surfaces, late mounts and navigation after success', async () => {
  const pending = deferred()
  const request = vi.fn(() => pending.promise)
  const alternative = vi.fn().mockResolvedValue(undefined)
  const wrapper = provider()
  const first = renderHook(() => useStageAction(stage('shared', 'hook_failed')), { wrapper })
  const second = renderHook(() => useStageAction(stage('shared', 'hook_failed')), { wrapper })
  let completion!: Promise<void>
  act(() => {
    completion = first.result.current.send(request)
    void first.result.current.send(request)
    void second.result.current.send(alternative)
  })
  const late = renderHook(() => useStageAction(stage('shared', 'hook_failed')), { wrapper })
  expect(request).toHaveBeenCalledTimes(1)
  expect(alternative).not.toHaveBeenCalled()
  for (const hook of [first, second, late]) expect(hook.result.current.locked).toBe(true)
  await act(async () => { pending.resolve(); await completion })
  for (const hook of [first, second, late]) {
    expect(hook.result.current.locked).toBe(true)
    hook.unmount()
  }
  const returned = renderHook(() => useStageAction(stage('shared', 'hook_failed')), { wrapper })
  expect(returned.result.current.locked).toBe(true)
  await act(async () => returned.result.current.send(alternative))
  expect(alternative).not.toHaveBeenCalled()
})

it.each(['running', 'failed'] as const)('unlocks a new %s episode after success even if polling missed the intervening transition', async (status) => {
  const request = vi.fn().mockResolvedValue(undefined)
  const old = stage('s1')
  const { result, rerender } = renderHook(({ current }) => useStageAction(current), { initialProps: { current: old }, wrapper: provider() })
  await act(async () => result.current.send(request))
  rerender({ current: old })
  expect(result.current.locked).toBe(true)
  await act(async () => result.current.send(request))
  expect(request).toHaveBeenCalledTimes(1)
  rerender({ current: stage('s1', status, '2026-10-29T10:01:00Z') })
  expect(result.current).toMatchObject({ locked: false, error: null })
  await act(async () => result.current.send(request))
  expect(request).toHaveBeenCalledTimes(2)
  expect(result.current.locked).toBe(true)
})

it('unlocks after rejection, clears the error on retry and hides it in another episode', async () => {
  const failed = vi.fn().mockRejectedValue(new Error('refused'))
  const { result, rerender } = renderHook(({ current }) => useStageAction(current), { initialProps: { current: stage('s1') }, wrapper: provider() })
  await act(async () => result.current.send(failed))
  expect(result.current).toMatchObject({ locked: false, error: 'refused' })
  const pending = deferred()
  let completion!: Promise<void>
  act(() => { completion = result.current.send(() => pending.promise) })
  expect(result.current).toMatchObject({ locked: true, error: null })
  await act(async () => { pending.reject(new Error('still refused')); await completion })
  expect(result.current).toMatchObject({ locked: false, error: 'still refused' })
  rerender({ current: stage('s1', 'failed', '2026-10-29T10:01:00Z') })
  expect(result.current).toMatchObject({ locked: false, error: null })
})

it.each(['same stage, new episode', 'different stage'] as const)('ignores a late rejection after a newer command: %s', async (change) => {
  const old = deferred()
  const next = deferred()
  const { result, rerender } = renderHook(({ current }) => useStageAction(current), { initialProps: { current: stage('s1') }, wrapper: provider() })
  let oldCompletion!: Promise<void>
  act(() => { oldCompletion = result.current.send(() => old.promise) })
  rerender({ current: stage(change === 'different stage' ? 's2' : 's1', 'failed', '2026-10-29T10:01:00Z') })
  expect(result.current.locked).toBe(false)
  let completion!: Promise<void>
  act(() => { completion = result.current.send(() => next.promise) })
  await act(async () => { old.reject(new Error('old error')); await oldCompletion })
  expect(result.current).toMatchObject({ locked: true, error: null })
  await act(async () => { next.resolve(); await completion })
  expect(result.current).toMatchObject({ locked: true, error: null })
})

it('isolates a pending old run and its late rejection from a new run with identical stage data', async () => {
  const firstStore = createStageActions()
  const secondStore = createStageActions()
  let currentStore = firstStore
  const wrapper = ({ children }: { children: ReactNode }) => <StageActionsContext.Provider value={currentStore}>{children}</StageActionsContext.Provider>
  const { result, rerender } = renderHook(() => useStageAction(stage('s1')), { wrapper })
  const old = deferred()
  let oldCompletion!: Promise<void>
  act(() => { oldCompletion = result.current.send(() => old.promise) })
  currentStore = secondStore
  rerender()
  expect(result.current).toMatchObject({ locked: false, error: null })
  const request = vi.fn().mockResolvedValue(undefined)
  await act(async () => result.current.send(request))
  await act(async () => { old.reject(new Error('old run error')); await oldCompletion })
  expect(result.current).toMatchObject({ locked: true, error: null })
  expect(request).toHaveBeenCalledTimes(1)
})

it('ignores commands without a stage', async () => {
  const request = vi.fn()
  const { result } = renderHook(() => useStageAction(null))
  await act(async () => result.current.send(request))
  expect(request).not.toHaveBeenCalled()
  expect(result.current).toMatchObject({ locked: false, error: null })
})
