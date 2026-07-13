import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useTitleFlash } from './use-title-flash'

describe('useTitleFlash', () => {
  beforeEach(() => { Object.defineProperty(document, 'hidden', { value: true, configurable: true }); document.title = 'afm Dashboard' })
  afterEach(() => { vi.useRealTimers() })
  it('мигает title когда вкладка скрыта и active=true', () => {
    vi.useFakeTimers()
    renderHook(() => useTitleFlash(true))
    const orig = 'afm Dashboard'
    expect(document.title).toBe(orig)
    vi.advanceTimersByTime(1500)
    expect(document.title).not.toBe(orig) // сменился на flash
  })
  it('не мигает при active=false', () => {
    vi.useFakeTimers()
    renderHook(() => useTitleFlash(false))
    vi.advanceTimersByTime(3000)
    expect(document.title).toBe('afm Dashboard')
  })
})
