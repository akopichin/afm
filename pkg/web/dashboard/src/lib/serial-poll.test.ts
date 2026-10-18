import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { startSerialPoll } from './serial-poll'

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void
  const promise = new Promise<void>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('startSerialPoll', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  test('ticks that arrive while a load is running are coalesced into one load after it', async () => {
    const first = deferred()
    const load = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined)
    const poll = startSerialPoll(load, 1000)
    expect(load).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(3000) // three ticks while the first load hangs
    expect(load).toHaveBeenCalledTimes(1)

    first.resolve()
    await vi.advanceTimersByTimeAsync(0)
    expect(load).toHaveBeenCalledTimes(2)
    poll.stop()
  })

  test('now() during a running load resolves only after a fresh load completed', async () => {
    const first = deferred()
    const order: string[] = []
    const load = vi
      .fn()
      .mockImplementationOnce(() => first.promise.then(() => void order.push('first')))
      .mockImplementation(() => Promise.resolve().then(() => void order.push('reload')))
    const poll = startSerialPoll(load, 1000)

    const reloaded = poll.now().then(() => void order.push('now resolved'))
    first.resolve()
    await reloaded
    expect(order).toEqual(['first', 'reload', 'now resolved'])
    poll.stop()
  })

  test('stop() ends polling and tells the running load it is stopped', async () => {
    const first = deferred()
    let stoppedSeen: boolean | null = null
    const load = vi.fn((isStopped: () => boolean) => first.promise.then(() => void (stoppedSeen = isStopped())))
    const poll = startSerialPoll(load, 1000)
    poll.stop()
    first.resolve()
    await vi.advanceTimersByTimeAsync(5000)
    expect(stoppedSeen).toBe(true)
    expect(load).toHaveBeenCalledTimes(1)
  })

  test('a failing load does not stop the next tick', async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error('net')).mockResolvedValue(undefined)
    const poll = startSerialPoll(load, 1000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(load).toHaveBeenCalledTimes(2)
    poll.stop()
  })
})
