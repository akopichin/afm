import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useThemeMode } from './use-theme-mode'

describe('useThemeMode', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  it('читает начальный режим из data-theme, выставленного бутстрап-скриптом', () => {
    document.documentElement.dataset.theme = 'light'
    const { result } = renderHook(() => useThemeMode())
    expect(result.current.mode).toBe('light')
  })

  it('по умолчанию dark, если data-theme не light', () => {
    const { result } = renderHook(() => useThemeMode())
    expect(result.current.mode).toBe('dark')
  })

  it('toggle() переключает режим, атрибут на <html> и localStorage', () => {
    document.documentElement.dataset.theme = 'dark'
    const { result } = renderHook(() => useThemeMode())

    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(window.localStorage.getItem('afm-mode')).toBe('light')
  })

  it('toggle() дважды возвращает исходный режим', () => {
    document.documentElement.dataset.theme = 'dark'
    const { result } = renderHook(() => useThemeMode())

    act(() => { result.current.toggle() })
    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(window.localStorage.getItem('afm-mode')).toBe('dark')
  })
})
