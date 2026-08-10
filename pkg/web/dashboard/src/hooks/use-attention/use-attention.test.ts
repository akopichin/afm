import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAttention, anyAwaiting } from './use-attention'
import type { Stage } from '../../types'

const stage = (status: Stage['status']): Stage =>
  ({ id: 's', name: 'n', status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false })

describe('useAttention', () => {
  it('dialog для awaiting_user_input, plan для awaiting_approval, null иначе', () => {
    expect(renderHook(() => useAttention(stage('awaiting_user_input'))).result.current).toEqual({ needsAttention: true, kind: 'dialog' })
    expect(renderHook(() => useAttention(stage('awaiting_approval'))).result.current).toEqual({ needsAttention: true, kind: 'plan' })
    expect(renderHook(() => useAttention(stage('running'))).result.current).toEqual({ needsAttention: false, kind: null })
    expect(renderHook(() => useAttention(null)).result.current).toEqual({ needsAttention: false, kind: null })
  })
  it('anyAwaiting ищет по массиву стадий', () => {
    expect(anyAwaiting([stage('running'), stage('awaiting_user_input')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('done')])).toBe(false)
  })
})
