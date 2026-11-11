import { describe, it, expect } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { emptyRowState, isRowStateEmpty, isSeriesOpen, reportKey, syncRowState, toggleRaw, toggleReport, toggleSeries, useFeedRowState, bumpImageAttempt } from './feed-row-state'

const live = (over: Partial<Parameters<typeof syncRowState>[1]> = {}) => ({ itemKeys: new Set<string>(), reportKeys: new Set<string>(), messageIds: new Set<string>(), imageUrls: new Set<string>(), seriesTails: [], ...over })

describe('series state', () => {
  it('opens by first and last call and closes by every member', () => {
    const open = toggleSeries(emptyRowState('s1'), ['a', 'b', 'c'])
    expect([...open.openTools]).toEqual(['a', 'c'])
    expect(isSeriesOpen(open, ['a', 'b', 'c'])).toBe(true)
    expect(toggleSeries(open, ['a', 'b', 'c']).openTools.size).toBe(0)
  })

  it('stays open after the first call is evicted and closes with the whole series', () => {
    const open = toggleSeries(emptyRowState('s1'), ['a', 'b', 'c'])
    const evicted = syncRowState(open, live({ itemKeys: new Set(['b', 'c', 'd']) }))
    expect(isSeriesOpen(evicted, ['b', 'c', 'd'])).toBe(true)
    const gone = syncRowState(evicted, live({ itemKeys: new Set(['x']) }))
    expect(gone.openTools.size).toBe(0)
  })

  it('remembers the tail of an open series, so eviction at a constant length keeps it open', () => {
    let state = toggleSeries(emptyRowState('s1'), ['a', 'b'])
    let members = ['a', 'b']
    for (let i = 0; i < 6; i++) {
      members = [...members.slice(1), `n${i}`]
      state = syncRowState(state, live({ itemKeys: new Set(members), seriesTails: [members.at(-1) as string] }))
      expect(isSeriesOpen(state, members)).toBe(true)
    }
  })

  it('returns the same state when nothing changes', () => {
    const sync = (state: ReturnType<typeof emptyRowState>) => syncRowState(state, live({ itemKeys: new Set(['a', 'b']), seriesTails: ['b'] }))
    // The first sync only notes how many occurrences of each key there are.
    const settled = sync(toggleSeries(emptyRowState('s1'), ['a', 'b']))
    expect(sync(settled)).toBe(settled)
  })

  it('keeps at most 500 keys, the oldest go first', () => {
    let state = emptyRowState('s1')
    for (let i = 0; i < 510; i++) state = toggleRaw(state, `m${i}`)
    expect(state.openRaw.size).toBe(500)
    expect(state.openRaw.has('m0')).toBe(false)
    expect(state.openRaw.has('m509')).toBe(true)
  })
})

describe('reports, raw and images', () => {
  it('forgets image retries of pictures that are no longer in the data', () => {
    let state = bumpImageAttempt(bumpImageAttempt(emptyRowState('s1'), '/a.png'), '/b.png')
    expect(isRowStateEmpty(state)).toBe(false)
    expect(syncRowState(state, live({ imageUrls: new Set(['/a.png', '/b.png']) }))).toBe(state)
    state = syncRowState(state, live({ imageUrls: new Set(['/b.png']) }))
    expect([...state.imageAttempts.keys()]).toEqual(['/b.png'])
  })

  it('keeps at most 200 image URLs, the oldest go first', () => {
    let state = emptyRowState('s1')
    for (let i = 0; i < 210; i++) state = bumpImageAttempt(state, `/img-${i}.png`)
    expect(state.imageAttempts.size).toBe(200)
    expect(state.imageAttempts.has('/img-0.png')).toBe(false)
    expect(state.imageAttempts.has('/img-209.png')).toBe(true)
  })

  it('toggles reports and raw by data id and drops them when the data is gone', () => {
    let state = toggleReport(emptyRowState('s1'), reportKey('s1', 'v1'))
    state = toggleRaw(state, 'm1')
    expect(syncRowState(state, live({ reportKeys: new Set(['s1|v1']), messageIds: new Set(['m1']) }))).toBe(state)
    const cleaned = syncRowState(state, live())
    expect(cleaned.openReports.size + cleaned.openRaw.size).toBe(0)
  })

  it('counts image retries per URL', () => {
    let state = emptyRowState('s1')
    for (let i = 0; i < 2; i++) state = bumpImageAttempt(state, '/a.png')
    expect(state.imageAttempts.get('/a.png')).toBe(2)
    expect(state.imageAttempts.get('/b.png')).toBeUndefined()
  })
})

describe('useFeedRowState', () => {
  it('survives re-renders and starts empty for another stage', () => {
    const { result, rerender } = renderHook(({ scope }) => useFeedRowState(scope), { initialProps: { scope: 's1' } })
    act(() => result.current.actions.toggleRaw('m1'))
    rerender({ scope: 's1' })
    expect(result.current.state.openRaw.has('m1')).toBe(true)
    rerender({ scope: 's2' })
    expect(result.current.state.scope).toBe('s2')
    expect(result.current.state.openRaw.size).toBe(0)
  })
})
