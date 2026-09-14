import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useFeedScope } from './use-feed-scope'

describe('useFeedScope', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  it('по умолчанию stage, если в localStorage ничего не сохранено', () => {
    const { result } = renderHook(() => useFeedScope())
    expect(result.current.scope).toBe('stage')
  })

  it('читает сохранённый scope all из localStorage при монтировании', () => {
    window.localStorage.setItem('afm-feed-scope', 'all')
    const { result } = renderHook(() => useFeedScope())
    expect(result.current.scope).toBe('all')
  })

  it('toggle() переключает scope и сохраняет его в localStorage', () => {
    const { result } = renderHook(() => useFeedScope())

    act(() => { result.current.toggle() })

    expect(result.current.scope).toBe('all')
    expect(window.localStorage.getItem('afm-feed-scope')).toBe('all')
  })

  it('toggle() дважды возвращает исходный scope', () => {
    const { result } = renderHook(() => useFeedScope())

    act(() => { result.current.toggle() })
    act(() => { result.current.toggle() })

    expect(result.current.scope).toBe('stage')
    expect(window.localStorage.getItem('afm-feed-scope')).toBe('stage')
  })
})
