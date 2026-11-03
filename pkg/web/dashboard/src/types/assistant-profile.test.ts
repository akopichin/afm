import { beforeEach, describe, expect, test } from 'vitest'
import { resetUnknownCommandLabels } from '../lib/executor-label'
import { describeExecutor, describeUnavailable, toAssistantProfile, type AssistantProfileWire } from './assistant-profile'

const wire: AssistantProfileWire = { id: 'reviewer', title: '', command: 'claude', available: true }

beforeEach(() => resetUnknownCommandLabels())

describe('toAssistantProfile', () => {
  test('an untitled profile is labelled by its id', () => {
    expect(toAssistantProfile(wire).title).toBe('reviewer')
  })

  test('a titled profile keeps its title', () => {
    expect(toAssistantProfile({ ...wire, title: 'Code reviewer' }).title).toBe('Code reviewer')
  })
})

describe('executor name of a profile', () => {
  test('toAssistantProfile keeps the server command_name beside the raw command', () => {
    const profile = toAssistantProfile({ ...wire, command: 'codex-as-claude', command_name: 'Codex Reviewer' })
    expect(profile.commandName).toBe('Codex Reviewer')
    expect(profile.command).toBe('codex-as-claude')
  })

  test('describeExecutor shows the readable name and where it was decided', () => {
    const profile = toAssistantProfile({ ...wire, command: 'codex-as-claude', command_name: 'Codex Reviewer', command_source: 'profile' })
    expect(describeExecutor(profile)).toBe('Codex Reviewer (profile)')
    expect(describeExecutor(toAssistantProfile({ ...wire, command_name: 'Claude Code', command_source: 'client' }))).toBe('Claude Code (run default)')
    expect(describeExecutor(toAssistantProfile({ ...wire, command_name: 'Claude Code' }))).toBe('Claude Code')
  })

  test('describeExecutor numbers a profile the server sent no name for, with the status prefix', () => {
    const profile = toAssistantProfile({ ...wire, command: './x/agent.sh', command_source: 'profile' })
    expect(describeExecutor(profile, 'test')).toBe('test-1 (profile)')
  })
})

describe('describeUnavailable', () => {
  test('names an untitled unavailable profile by its id', () => {
    const missing = toAssistantProfile({ ...wire, id: 'missing', available: false, unavailable_reason: 'no policy' })
    expect(describeUnavailable([missing])).toBe('missing: no policy')
  })
})
