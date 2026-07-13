import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useStickToBottom } from './use-stick-to-bottom'

describe('useStickToBottom', () => {
  it('stick=true по умолчанию; jumpToBottom выставляет stick=true', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
    expect(result.current.stick).toBe(true)
    expect(typeof result.current.jumpToBottom).toBe('function')
    expect(result.current.ref.current).toBeNull()
  })
})
