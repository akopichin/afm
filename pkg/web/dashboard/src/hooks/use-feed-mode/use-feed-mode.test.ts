import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useFeedMode } from './use-feed-mode'

describe('useFeedMode', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  it('по умолчанию feed, если в localStorage ничего не сохранено', () => {
    const { result } = renderHook(() => useFeedMode())
    expect(result.current.mode).toBe('feed')
  })

  it('читает сохранённый режим log из localStorage при монтировании', () => {
    window.localStorage.setItem('afm-feed-mode', 'log')
    const { result } = renderHook(() => useFeedMode())
    expect(result.current.mode).toBe('log')
  })

  it('toggle() переключает режим и сохраняет его в localStorage', () => {
    const { result } = renderHook(() => useFeedMode())

    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('log')
    expect(window.localStorage.getItem('afm-feed-mode')).toBe('log')
  })

  it('toggle() дважды возвращает исходный режим', () => {
    const { result } = renderHook(() => useFeedMode())

    act(() => { result.current.toggle() })
    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('feed')
    expect(window.localStorage.getItem('afm-feed-mode')).toBe('feed')
  })
})
