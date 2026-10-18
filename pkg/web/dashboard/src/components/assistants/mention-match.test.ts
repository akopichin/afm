import { describe, expect, it } from 'vitest'
import { detectMentionTrigger, filterMentionGroups, filterMentionProfiles } from './mention-match'

describe('detectMentionTrigger', () => {
  it('detects an in-progress mention right after @', () => {
    expect(detectMentionTrigger('@fro', 4)).toEqual({ start: 0, query: 'fro' })
  })

  it('detects a mention in the middle of text, preceded by whitespace', () => {
    expect(detectMentionTrigger('please ask @qa about this', 14)).toEqual({ start: 11, query: 'qa' })
  })

  it('does not trigger on an email-like @ (no preceding whitespace)', () => {
    expect(detectMentionTrigger('contact user@example.com please', 17)).toBeNull()
  })

  it('does not trigger once a space ends the mention attempt', () => {
    expect(detectMentionTrigger('@fro ', 5)).toBeNull()
  })

  it('returns null when there is no @ at all', () => {
    expect(detectMentionTrigger('hello world', 5)).toBeNull()
  })

  it('triggers with an empty query right after a bare @', () => {
    expect(detectMentionTrigger('@', 1)).toEqual({ start: 0, query: '' })
  })
})

describe('filterMentionProfiles', () => {
  const profiles = [
    { id: 'frontendDeveloper', title: 'Frontend reviewer' },
    { id: 'qaEngineer', title: 'QA engineer' },
  ]

  it('returns everything for an empty query', () => {
    expect(filterMentionProfiles(profiles, '')).toHaveLength(2)
  })

  it('filters case-insensitively by id', () => {
    expect(filterMentionProfiles(profiles, 'FRO').map((p) => p.id)).toEqual(['frontendDeveloper'])
  })

  it('filters case-insensitively by title', () => {
    expect(filterMentionProfiles(profiles, 'engineer').map((p) => p.id)).toEqual(['qaEngineer'])
  })

  it('returns nothing when nothing matches', () => {
    expect(filterMentionProfiles(profiles, 'zzz')).toHaveLength(0)
  })
})

describe('filterMentionGroups', () => {
  const groups = [
    { label: 'This run', items: [{ id: 'run:qa', title: 'QA reviewer' }] },
    { label: 'This stage', items: [{ id: 'stage:s1', title: 'Stage agent' }] },
    { label: 'Attach to stage', items: [{ id: 'frontendDeveloper', title: 'Frontend reviewer' }] },
  ]

  it('returns every non-empty group for an empty query', () => {
    expect(filterMentionGroups(groups, '')).toEqual(groups)
  })

  it('filters each group independently and drops groups left empty', () => {
    const result = filterMentionGroups(groups, 'frontend')
    expect(result).toEqual([{ label: 'Attach to stage', items: [{ id: 'frontendDeveloper', title: 'Frontend reviewer' }] }])
  })

  it('returns an empty array when nothing matches in any group', () => {
    expect(filterMentionGroups(groups, 'zzz')).toEqual([])
  })
})
