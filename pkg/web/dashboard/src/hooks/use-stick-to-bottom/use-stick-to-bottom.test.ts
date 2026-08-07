import { describe, it, expect } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useStickToBottom } from './use-stick-to-bottom'

describe('useStickToBottom', () => {
  it('stick=true by default; ref is a callback, jumpToBottom is a function', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
    expect(result.current.stick).toBe(true)
    expect(typeof result.current.jumpToBottom).toBe('function')
    expect(typeof result.current.ref).toBe('function')
  })

  it('scrolls to the bottom immediately when the node attaches after content already exists', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const el = document.createElement('div')
    Object.defineProperty(el, 'scrollHeight', { value: 500, configurable: true })
    el.scrollTop = 0

    // Simulates DialogChannel: the container mounts (ref callback fires) only after
    // history/pending content is already in the DOM, unlike a container that's
    // always mounted from the first render.
    act(() => {
      result.current.ref(el)
    })

    expect(el.scrollTop).toBe(500)
  })

  it('re-attaching to a new node (e.g. after the container remounts) tracks the new node, not the old one', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())

    const first = document.createElement('div')
    Object.defineProperty(first, 'scrollHeight', { value: 100, configurable: true })

    act(() => {
      result.current.ref(first)
    })
    expect(first.scrollTop).toBe(100)

    act(() => {
      result.current.ref(null)
    })

    const second = document.createElement('div')
    Object.defineProperty(second, 'scrollHeight', { value: 900, configurable: true })
    second.scrollTop = 0

    act(() => {
      result.current.ref(second)
    })
    expect(second.scrollTop).toBe(900)
  })
})
