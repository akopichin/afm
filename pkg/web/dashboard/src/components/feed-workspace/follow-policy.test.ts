import { describe, it, expect } from 'vitest'
import { followReducer, isNearBottom } from './follow-policy'

const USERS = ['up', 'down', null] as const

describe('followReducer', () => {
  // The invariant: geometry alone is no intent. Whatever the list did on its own, follow stays as the reader left it.
  it('a scroll without user input never changes follow, near the bottom or far from it', () => {
    for (const following of [true, false]) {
      for (const nearBottom of [true, false]) {
        expect(followReducer(following, { type: 'scroll', nearBottom, user: null })).toBe(following)
      }
    }
  })

  it('a user scroll down that ends near the bottom resumes follow; up away from the bottom releases it', () => {
    expect(followReducer(false, { type: 'scroll', nearBottom: true, user: 'down' })).toBe(true)
    expect(followReducer(true, { type: 'scroll', nearBottom: false, user: 'up' })).toBe(false)
    expect(followReducer(false, { type: 'scroll', nearBottom: false, user: 'up' })).toBe(false)
    expect(followReducer(true, { type: 'scroll', nearBottom: true, user: 'down' })).toBe(true)
  })

  it('a user scroll that does not change the side of the bottom keeps the state', () => {
    expect(followReducer(false, { type: 'scroll', nearBottom: false, user: 'down' })).toBe(false)
    expect(followReducer(true, { type: 'scroll', nearBottom: false, user: 'down' })).toBe(true)
    // A nudge up inside the threshold is not a way back to the bottom.
    expect(followReducer(false, { type: 'scroll', nearBottom: true, user: 'up' })).toBe(false)
    expect(followReducer(true, { type: 'scroll', nearBottom: true, user: 'up' })).toBe(true)
  })

  it('releases on expanding a row and on navigating to a quote, whatever the position', () => {
    expect(followReducer(true, { type: 'expanded' })).toBe(false)
    expect(followReducer(true, { type: 'navigate' })).toBe(false)
  })

  it('a collapse that settled at the bottom follows again; otherwise the reading state stays', () => {
    expect(followReducer(false, { type: 'collapsed', nearBottom: true })).toBe(true)
    expect(followReducer(false, { type: 'collapsed', nearBottom: false })).toBe(false)
    expect(followReducer(true, { type: 'collapsed', nearBottom: false })).toBe(true)
  })

  it('Jump to latest and a reset follow', () => {
    expect(followReducer(false, { type: 'jump-to-latest' })).toBe(true)
    expect(followReducer(false, { type: 'reset' })).toBe(true)
  })

  it('only the listed intents can turn follow on', () => {
    const resumes = USERS.flatMap((user) => [true, false].map((nearBottom) => followReducer(false, { type: 'scroll', nearBottom, user })))
    expect(resumes.filter(Boolean)).toHaveLength(1)
  })
})

describe('isNearBottom', () => {
  it('is true within 40px of the end', () => {
    expect(isNearBottom({ scrollHeight: 1000, scrollTop: 561, clientHeight: 400 })).toBe(true)
    expect(isNearBottom({ scrollHeight: 1000, scrollTop: 560, clientHeight: 400 })).toBe(false)
  })
})
