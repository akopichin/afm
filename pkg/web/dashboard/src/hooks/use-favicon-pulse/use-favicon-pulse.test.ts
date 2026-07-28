import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useFaviconPulse } from './use-favicon-pulse'
import { compositeAttentionBadge } from './composite-attention-badge'

vi.mock('./composite-attention-badge', () => ({
  compositeAttentionBadge: vi.fn(),
}))

const mockComposite = vi.mocked(compositeAttentionBadge)
const BADGE_HREF = 'data:image/png;base64,BADGE'

function setIconLink(href: string): HTMLLinkElement {
  document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  const link = document.createElement('link')
  link.rel = 'icon'
  link.href = href
  document.head.appendChild(link)
  return link
}

describe('useFaviconPulse', () => {
  beforeEach(() => {
    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    mockComposite.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  })

  it('не трогает href и не зовёт compositeAttentionBadge при active=false', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    renderHook(() => useFaviconPulse(false))

    await vi.advanceTimersByTimeAsync(3000)

    expect(link.href).toBe(original)
    expect(mockComposite).not.toHaveBeenCalled()
  })

  it('мигает href между оригиналом и бейджем, пока вкладка скрыта и active=true', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    renderHook(() => useFaviconPulse(true))

    await vi.advanceTimersByTimeAsync(0) // дать промису compositeAttentionBadge зарезолвиться
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(original)
  })

  it('останавливает пульс и восстанавливает href, когда вкладка снова видима', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))

    expect(link.href).toBe(original)

    await vi.advanceTimersByTimeAsync(2000)
    expect(link.href).toBe(original) // таймер остановлен, больше не мигает
  })

  it('восстанавливает href и чистит таймер при размонтировании', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    const { unmount } = renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    unmount()
    expect(link.href).toBe(original)

    await vi.advanceTimersByTimeAsync(2000)
    expect(link.href).toBe(original)
  })

  it('не падает и не пульсирует, если compositeAttentionBadge реджектится', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockRejectedValue(new Error('load failed'))

    renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(3000)

    expect(link.href).toBe(original)
  })

  it('ничего не делает, если в DOM нет link[rel="icon"]', async () => {
    vi.useFakeTimers()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())

    expect(() => renderHook(() => useFaviconPulse(true))).not.toThrow()
    await vi.advanceTimersByTimeAsync(3000)
    expect(mockComposite).not.toHaveBeenCalled()
  })
})
