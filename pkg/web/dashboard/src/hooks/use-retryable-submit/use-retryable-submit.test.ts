import { act, renderHook } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { removeSubmittedText, useRetryableSubmit } from './use-retryable-submit'

// deferred — a promise the test settles by hand, to model a slow or lost response.
function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

type Payload = { body: string }

describe('useRetryableSubmit', () => {
  test('a retry of the same payload after a failure reuses the request id', async () => {
    const send = vi.fn((_p: Readonly<Payload>, _id: string) => Promise.reject(new Error('response lost')))
    const { result } = renderHook(() => useRetryableSubmit<Payload>('t'))

    await act(async () => result.current.submit({ body: 'hi' }, send))
    expect(result.current.error).toBe('response lost')
    await act(async () => result.current.submit({ body: 'hi' }, send))

    expect(send).toHaveBeenCalledTimes(2)
    expect(send.mock.calls[1]?.[1]).toBe(send.mock.calls[0]?.[1])
  })

  test('a changed payload is a new intent and gets a new request id', async () => {
    const send = vi.fn((_p: Readonly<Payload>, _id: string) => Promise.reject(new Error('lost')))
    const { result } = renderHook(() => useRetryableSubmit<Payload>('t'))

    await act(async () => result.current.submit({ body: 'hi' }, send))
    await act(async () => result.current.submit({ body: 'hi, edited' }, send))

    expect(send.mock.calls[1]?.[1]).not.toBe(send.mock.calls[0]?.[1])
  })

  test('after a success the same text again is a new message with a new id', async () => {
    const send = vi.fn((_p: Readonly<Payload>, _id: string) => Promise.resolve('ok'))
    const { result } = renderHook(() => useRetryableSubmit<Payload>('t'))

    await act(async () => result.current.submit({ body: 'ok' }, send))
    await act(async () => result.current.submit({ body: 'ok' }, send))

    expect(send.mock.calls[1]?.[1]).not.toBe(send.mock.calls[0]?.[1])
  })

  test('a success under a fence retires its older failed intents: resending an old text gets a new id', async () => {
    const send = vi.fn((p: Readonly<Payload>, _id: string) => (p.body === 'B' ? Promise.resolve('ok') : Promise.reject(new Error('lost'))))
    const { result, rerender } = renderHook(({ fence }) => useRetryableSubmit<Payload>('t', fence), { initialProps: { fence: 'a' } })

    await act(async () => result.current.submit({ body: 'A' }, send))
    rerender({ fence: 'b' })
    await act(async () => result.current.submit({ body: 'other' }, send))
    rerender({ fence: 'a' })
    await act(async () => result.current.submit({ body: 'B' }, send))
    await act(async () => result.current.submit({ body: 'A' }, send))
    expect(send.mock.calls[3]?.[1]).not.toBe(send.mock.calls[0]?.[1])

    rerender({ fence: 'b' })
    await act(async () => result.current.submit({ body: 'other' }, send))
    expect(send.mock.calls[4]?.[1]).toBe(send.mock.calls[1]?.[1])
  })

  test('sends an immutable snapshot of the payload', async () => {
    const send = vi.fn((p: Readonly<Payload>, _id: string) => Promise.resolve(p))
    const { result } = renderHook(() => useRetryableSubmit<Payload>('t'))
    await act(async () => result.current.submit({ body: 'hi' }, send))
    expect(Object.isFrozen(send.mock.calls[0]?.[0])).toBe(true)
  })

  test('a second submit while one is in flight is ignored', async () => {
    const pending = deferred<string>()
    const send = vi.fn(() => pending.promise)
    const { result } = renderHook(() => useRetryableSubmit<Payload>('t'))

    act(() => {
      result.current.submit({ body: 'a' }, send)
      result.current.submit({ body: 'a' }, send)
    })
    expect(result.current.pending).toBe(true)
    expect(send).toHaveBeenCalledTimes(1)
    await act(async () => pending.resolve('ok'))
    expect(result.current.pending).toBe(false)
  })

  test('a late response after the fence changed runs no handler and does not block the new selection', async () => {
    const pending = deferred<string>()
    const onSuccess = vi.fn()
    const { result, rerender } = renderHook(({ fence }) => useRetryableSubmit<Payload>('t', fence), { initialProps: { fence: 'a' } })

    act(() => result.current.submit({ body: 'x' }, () => pending.promise, { onSuccess }))
    expect(result.current.pending).toBe(true)
    rerender({ fence: 'b' })
    expect(result.current.pending).toBe(false)

    await act(async () => pending.resolve('ok'))
    expect(onSuccess).not.toHaveBeenCalled()
    rerender({ fence: 'a' })
    expect(result.current.pending).toBe(false)
  })

  test('a late failure after the fence changed shows no error for the new selection', async () => {
    const pending = deferred<string>()
    const onFailure = vi.fn()
    const { result, rerender } = renderHook(({ fence }) => useRetryableSubmit<Payload>('t', fence), { initialProps: { fence: 'a' } })

    act(() => result.current.submit({ body: 'x' }, () => pending.promise, { onFailure }))
    rerender({ fence: 'b' })
    await act(async () => pending.reject(new Error('boom')))

    expect(onFailure).not.toHaveBeenCalled()
    expect(result.current.error).toBeNull()
  })

  test("a stale selection's late failure keeps the current selection's retry id", async () => {
    const sendA = deferred<string>()
    const send = vi.fn((_p: Readonly<Payload>, _id: string) => (send.mock.calls.length === 1 ? sendA.promise : Promise.reject(new Error('lost'))))
    const { result, rerender } = renderHook(({ fence }) => useRetryableSubmit<Payload>('t', fence), { initialProps: { fence: 'a' } })

    act(() => result.current.submit({ body: 'to a' }, send))
    rerender({ fence: 'b' })
    await act(async () => result.current.submit({ body: 'to b' }, send))
    expect(result.current.error).toBe('lost')
    await act(async () => sendA.reject(new Error('late')))

    await act(async () => result.current.submit({ body: 'to b' }, send))
    expect(send.mock.calls[2]?.[1]).toBe(send.mock.calls[1]?.[1])

    rerender({ fence: 'a' })
    await act(async () => result.current.submit({ body: 'to a' }, send))
    expect(send.mock.calls[3]?.[1]).toBe(send.mock.calls[0]?.[1])
  })

  test('the same payload under another fence is a different intent', async () => {
    const send = vi.fn((_p: Readonly<Payload>, _id: string) => Promise.reject(new Error('lost')))
    const { result, rerender } = renderHook(({ fence }) => useRetryableSubmit<Payload>('t', fence), { initialProps: { fence: 'a' } })

    await act(async () => result.current.submit({ body: 'hi' }, send))
    rerender({ fence: 'b' })
    await act(async () => result.current.submit({ body: 'hi' }, send))

    expect(send.mock.calls[1]?.[1]).not.toBe(send.mock.calls[0]?.[1])
  })

  test('no handler runs after unmount', async () => {
    const pending = deferred<string>()
    const onSuccess = vi.fn()
    const { result, unmount } = renderHook(() => useRetryableSubmit<Payload>('t'))
    act(() => result.current.submit({ body: 'x' }, () => pending.promise, { onSuccess }))
    unmount()
    await act(async () => pending.resolve('ok'))
    expect(onSuccess).not.toHaveBeenCalled()
  })
})

describe('removeSubmittedText', () => {
  test('clears exactly the submitted text', () => {
    expect(removeSubmittedText('hello', 'hello')).toBe('')
  })
  test('keeps what was typed after the submitted text', () => {
    expect(removeSubmittedText('hello\nand more', 'hello')).toBe('and more')
  })
  test('leaves a draft that was edited elsewhere untouched', () => {
    expect(removeSubmittedText('hi there', 'hello')).toBe('hi there')
  })
})
