import { describe, it, expect, vi } from 'vitest'
import { createReportCache } from './report-cache'

describe('createReportCache', () => {
  it('shares one request between callers and serves the loaded text without another one', async () => {
    const fetchReport = vi.fn().mockResolvedValue('body')
    const cache = createReportCache(fetchReport)
    const [a, b] = await Promise.all([cache.load('s1', 'v1'), cache.load('s1', 'v1')])
    expect([a, b]).toEqual(['body', 'body'])
    expect(cache.peek('s1', 'v1')).toBe('body')
    await cache.load('s1', 'v1')
    expect(fetchReport).toHaveBeenCalledTimes(1)
  })

  it('does not keep a failure: the next open asks again', async () => {
    const fetchReport = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce('ok')
    const cache = createReportCache(fetchReport)
    await expect(cache.load('s1', 'v1')).rejects.toThrow('boom')
    expect(cache.peek('s1', 'v1')).toBeUndefined()
    await expect(cache.load('s1', 'v1')).resolves.toBe('ok')
  })

  it('keeps at most 20 reports, the oldest go first', async () => {
    const cache = createReportCache((_, v) => Promise.resolve(`text ${v}`))
    for (let i = 0; i < 25; i++) await cache.load('s1', `v${i}`)
    expect(cache.peek('s1', 'v0')).toBeUndefined()
    expect(cache.peek('s1', 'v4')).toBeUndefined()
    expect(cache.peek('s1', 'v5')).toBe('text v5')
    expect(cache.peek('s1', 'v24')).toBe('text v24')
  })

  it('bounds the stored text at 2 million characters, the newest report included', async () => {
    const cache = createReportCache((_, v) => Promise.resolve('x'.repeat(v === 'big' ? 3_000_000 : 1_500_000)))
    await cache.load('s1', 'a')
    await cache.load('s1', 'b')
    expect(cache.peek('s1', 'a')).toBeUndefined()
    expect(cache.peek('s1', 'b')).toHaveLength(1_500_000)
    // An oversized report reaches its caller but is not stored, and does not push the stored ones out.
    await expect(cache.load('s1', 'big')).resolves.toHaveLength(3_000_000)
    expect(cache.peek('s1', 'big')).toBeUndefined()
    expect(cache.peek('s1', 'b')).toHaveLength(1_500_000)
  })

  it('ignores the answer of a request that started before clear', async () => {
    let resolve: (text: string) => void = () => {}
    const cache = createReportCache(() => new Promise<string>((r) => (resolve = r)))
    const stale = cache.load('s1', 'v1')
    cache.clear()
    resolve('old')
    await expect(stale).resolves.toBe('old')
    expect(cache.peek('s1', 'v1')).toBeUndefined()
  })
})
