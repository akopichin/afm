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

  it('не создаёт orphaned-интервал при hidden→visible→hidden, пока compositeAttentionBadge ещё в полёте', async () => {
    vi.useFakeTimers()
    setIconLink('/favicon.svg')

    const resolvers: Array<(value: string) => void> = []
    mockComposite.mockImplementation(
      () =>
        new Promise<string>((resolve) => {
          resolvers.push(resolve)
        }),
    )

    renderHook(() => useFaviconPulse(true))
    expect(mockComposite).toHaveBeenCalledTimes(1) // первая активация onVisibility() при монтировании (hidden=true)

    // Вкладка на мгновение стала видимой: onVisibility -> stop(), но timer
    // ещё не создан (первый промис всё ещё в полёте) — no-op.
    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))

    // И снова скрыта до того, как первый промис успел зарезолвиться —
    // второй вызов onVisibility запускает второй compositeAttentionBadge.
    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(mockComposite).toHaveBeenCalledTimes(2)

    // Оба промиса резолвятся, пока вкладка всё ещё скрыта — оба .then
    // проходят проверку `!document.hidden`, но без guard'а по timer
    // второй .then перезаписал бы ссылку на первый интервал, оставив его
    // тикать вечно (orphaned timer).
    resolvers.forEach((resolve) => resolve(BADGE_HREF))
    await vi.advanceTimersByTimeAsync(0)

    expect(vi.getTimerCount()).toBe(1)
  })
})
