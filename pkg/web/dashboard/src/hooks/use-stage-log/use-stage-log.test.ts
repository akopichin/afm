import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { useStageLog } from './use-stage-log'

describe('useStageLog', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('keeps only text lines from the log endpoint', async () => {
    const log = '10:00:01  text  hello\n10:00:02  tool  Bash\n10:00:03  text  world'

    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      text: async () => log,
    } as Response)

    const { result } = renderHook(() => useStageLog('s1'))

    await waitFor(() => {
      expect(result.current).toHaveLength(2)
    })

    expect(result.current[0]).toEqual({ timestamp: '10:00:01', message: 'hello', level: 'info' })
    expect(result.current[1]?.message).toBe('world')
  })

  test('null stageId yields an empty array without a request', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')

    const { result } = renderHook(() => useStageLog(null))

    expect(result.current).toEqual([])
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  test('fetch rejection leaves entries empty without throwing', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network down'))

    const { result } = renderHook(() => useStageLog('s1'))

    await waitFor(() => {
      expect(result.current).toEqual([])
    })
  })

  test('response.ok === false leaves entries unchanged', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      text: async () => '10:00:01  text  should not appear',
    } as Response)

    const { result } = renderHook(() => useStageLog('s1'))

    await waitFor(() => {
      expect(globalThis.fetch).toHaveBeenCalled()
    })
    expect(result.current).toEqual([])
  })

  test('a fetch resolving after unmount does not update state or throw', async () => {
    let resolveFetch: (value: Response) => void = () => {}
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveFetch = resolve
        }),
    )

    const { unmount } = renderHook(() => useStageLog('s1'))

    unmount()

    expect(() => {
      resolveFetch({
        ok: true,
        text: async () => '10:00:01  text  late',
      } as Response)
    }).not.toThrow()

    // Даём микротаскам отработать — обновление состояния после unmount не должно падать.
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
})
