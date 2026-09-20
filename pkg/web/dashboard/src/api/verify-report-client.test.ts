import { afterEach, describe, expect, test, vi } from 'vitest'
import { VerifyReportError, getVerifyReport } from './verify-report-client'

describe('verify-report-client', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('getVerifyReport fetches by stage id + verification id and returns content', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ content: '# Report\n\npass' }),
    } as Response)

    const content = await getVerifyReport('build', 'v-1')

    expect(content).toBe('# Report\n\npass')
    expect(fetchSpy).toHaveBeenCalledWith('/api/stages/build/verify/v-1/report')
  })

  test('getVerifyReport encodes ids so they can never inject an extra path segment', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ content: '' }),
    } as Response)

    await getVerifyReport('a/b', 'v/1')

    expect(fetchSpy).toHaveBeenCalledWith('/api/stages/a%2Fb/verify/v%2F1/report')
  })

  test('getVerifyReport throws a typed error on a non-ok response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 404 } as Response)

    await expect(getVerifyReport('build', 'missing')).rejects.toBeInstanceOf(VerifyReportError)
  })

  test('getVerifyReport tolerates a missing/non-string content field', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({}),
    } as Response)

    await expect(getVerifyReport('build', 'v-1')).resolves.toBe('')
  })
})
