import { StrictMode } from 'react'
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { useEnteringLast } from './use-entering-last'

const run = (sig: string | null, token: string | null) =>
  renderHook(({ s, t }) => useEnteringLast(s, t), { initialProps: { s: sig, t: token }, wrapper: StrictMode })

describe('useEnteringLast', () => {
  it('does not animate on the first render (baseline)', () => {
    const { result } = run('h3', 's1')
    expect(result.current.animate).toBe(false)
  })

  // The real useEventFeed starts events=[] (signature=null) and applies /api/events
  // asynchronously, so the first non-empty render comes AFTER mount with the same
  // token. null -> non-null must be a silent baseline, not an animation; a later
  // live append then animates.
  it('does NOT animate the async initial history load (null -> history), then animates a live append', () => {
    const { result, rerender } = run(null, 's1')   // empty feed on mount
    rerender({ s: 'h2', t: 's1' })                 // /api/events resolves: history loaded
    expect(result.current.animate).toBe(false)     // initial backfill never animates
    rerender({ s: 'L1', t: 's1' })                 // a genuine live message after
    expect(result.current.animate).toBe(true)
  })

  it('animates when the last message changes (new live message)', () => {
    const { result, rerender } = run('a', 's1')
    rerender({ s: 'b', t: 's1' })
    expect(result.current.animate).toBe(true)
  })

  it('does NOT animate when the last message is unchanged (re-sync, same newest content)', () => {
    const { result, rerender } = run('a', 's1')
    rerender({ s: 'b', t: 's1' })
    act(() => result.current.onEntered())      // its entrance finished
    rerender({ s: 'b', t: 's1' })              // re-sync leaves the newest content identical
    expect(result.current.animate).toBe(false)
  })

  it('does NOT animate an identical repeat of the current last message (owner rule)', () => {
    const { result, rerender } = run('x', 's1')
    rerender({ s: 'x', t: 's1' })              // same content again
    expect(result.current.animate).toBe(false)
  })

  it('re-baselines on resetToken (scope) change — no animation on switch', () => {
    const { result, rerender } = run('a', 's1')
    rerender({ s: 'z', t: 's2' })              // stage switch: new last, but baseline
    expect(result.current.animate).toBe(false)
    rerender({ s: 'z2', t: 's2' })             // a genuine new message after the switch
    expect(result.current.animate).toBe(true)
  })

  it('onEntered clears the flag and it stays cleared until the next change', () => {
    const { result, rerender } = run('a', 's1')
    rerender({ s: 'b', t: 's1' })
    expect(result.current.animate).toBe(true)
    act(() => result.current.onEntered())
    expect(result.current.animate).toBe(false)
    rerender({ s: 'b', t: 's1' })              // unchanged
    expect(result.current.animate).toBe(false)
    rerender({ s: 'c', t: 's1' })              // next new message
    expect(result.current.animate).toBe(true)
  })

  it('re-arms for a newer message that supersedes one still animating', () => {
    const { result, rerender } = run('a', 's1')
    rerender({ s: 'b', t: 's1' })              // b animating
    rerender({ s: 'c', t: 's1' })              // c supersedes before b finished
    expect(result.current.animate).toBe(true)  // still animating — now the new last (c)
  })
})
