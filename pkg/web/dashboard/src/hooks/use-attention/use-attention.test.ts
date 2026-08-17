import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAttention, anyAwaiting, stagesNeedingAttention } from './use-attention'
import type { Stage } from '../../types'

const stage = (status: Stage['status'], id = 's'): Stage =>
  ({ id, name: 'n', status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '' })

describe('useAttention', () => {
  it('dialog для awaiting_user_input, plan для awaiting_approval, failed для failed/hook_failed, null иначе', () => {
    expect(renderHook(() => useAttention(stage('awaiting_user_input'))).result.current).toEqual({ needsAttention: true, kind: 'dialog' })
    expect(renderHook(() => useAttention(stage('awaiting_approval'))).result.current).toEqual({ needsAttention: true, kind: 'plan' })
    expect(renderHook(() => useAttention(stage('failed'))).result.current).toEqual({ needsAttention: true, kind: 'failed' })
    expect(renderHook(() => useAttention(stage('hook_failed'))).result.current).toEqual({ needsAttention: true, kind: 'failed' })
    expect(renderHook(() => useAttention(stage('running'))).result.current).toEqual({ needsAttention: false, kind: null })
    expect(renderHook(() => useAttention(null)).result.current).toEqual({ needsAttention: false, kind: null })
  })
  it('anyAwaiting ищет по массиву стадий, включая failed/hook_failed', () => {
    expect(anyAwaiting([stage('running'), stage('awaiting_user_input')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('failed')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('hook_failed')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('done')])).toBe(false)
  })
})

describe('stagesNeedingAttention', () => {
  it('возвращает только стадии из ATTENTION_STATUSES с их kind, в исходном порядке', () => {
    const running = stage('running', 'a')
    const question = stage('awaiting_user_input', 'b')
    const plan = stage('awaiting_approval', 'c')
    const failed = stage('failed', 'd')
    const hookFailed = stage('hook_failed', 'e')
    const done = stage('done', 'f')

    expect(stagesNeedingAttention([running, question, plan, failed, hookFailed, done])).toEqual([
      { stage: question, kind: 'dialog' },
      { stage: plan, kind: 'plan' },
      { stage: failed, kind: 'failed' },
      { stage: hookFailed, kind: 'failed' },
    ])
  })

  it('пустой массив, если ни одна стадия не требует внимания', () => {
    expect(stagesNeedingAttention([stage('running'), stage('done')])).toEqual([])
  })
})
