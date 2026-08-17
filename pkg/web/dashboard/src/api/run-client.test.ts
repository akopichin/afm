import { afterEach, describe, expect, test, vi } from 'vitest'
import { continueStage, pauseStage, retryStage } from './run-client'

describe('run-client pause/continue', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('retryStage POSTs to /retry with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await retryStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/retry',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('pauseStage POSTs to /pause with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await pauseStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/pause',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  test('continueStage POSTs to /continue with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)

    await continueStage('s1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/stages/s1/continue',
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })
})
