import { describe, expect, test } from 'vitest'
import { describeUnavailable, toAssistantProfile, type AssistantProfileWire } from './assistant-profile'

const wire: AssistantProfileWire = { id: 'reviewer', title: '', command: 'claude', available: true }

describe('toAssistantProfile', () => {
  test('an untitled profile is labelled by its id', () => {
    expect(toAssistantProfile(wire).title).toBe('reviewer')
  })

  test('a titled profile keeps its title', () => {
    expect(toAssistantProfile({ ...wire, title: 'Code reviewer' }).title).toBe('Code reviewer')
  })
})

describe('describeUnavailable', () => {
  test('names an untitled unavailable profile by its id', () => {
    const missing = toAssistantProfile({ ...wire, id: 'missing', available: false, unavailable_reason: 'no policy' })
    expect(describeUnavailable([missing])).toBe('missing: no policy')
  })
})
