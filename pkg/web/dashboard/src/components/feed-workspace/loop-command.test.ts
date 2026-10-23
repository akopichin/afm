import { describe, expect, it } from 'vitest'
import type { MeshParticipant } from '../../api/mesh-client'
import { isLoopCommandDraft, loopIntervalSlot, loopSubcommand, parseLoopCommand, resolveLoopTarget } from './loop-command'

describe('isLoopCommandDraft', () => {
  it.each(['/loop', '/loop ', '/loop 5m @qa check', '/loop\n5m', '/loop\tlist', ' /loop 5m', '\n/loop 5m'])('treats %j as a loop command', (draft) => {
    expect(isLoopCommandDraft(draft)).toBe(true)
  })
  // Leading whitespace is still recognized (above): a paste that starts with a blank line/space must not fall
  // through to an ordinary message, which would send the literal `/loop ...` text instead of running it.
  it.each(['/loops', '/loop5m', 'say /loop 5m', '/Loop 5m', 'loop', ''])('keeps %j an ordinary message', (draft) => {
    expect(isLoopCommandDraft(draft)).toBe(false)
  })
})

describe('parseLoopCommand — add', () => {
  it('reads the interval, the single target and the prompt verbatim', () => {
    expect(parseLoopCommand('/loop 5m @agent проверь логи')).toEqual({
      ok: true,
      command: { kind: 'add', every: '5m', untilStageEnd: false, target: 'agent', prompt: 'проверь логи' },
    })
  })
  it('reads --until-stage-end between the interval and the target', () => {
    expect(parseLoopCommand('/loop 1h30m --until-stage-end @qa run tests')).toEqual({
      ok: true,
      command: { kind: 'add', every: '1h30m', untilStageEnd: true, target: 'qa', prompt: 'run tests' },
    })
  })
  it('keeps later @words and line breaks in the prompt — they are text, not recipients', () => {
    const parsed = parseLoopCommand('/loop 90s @qa ask @docs\nto review  twice\n')
    expect(parsed).toEqual({ ok: true, command: { kind: 'add', every: '90s', untilStageEnd: false, target: 'qa', prompt: 'ask @docs\nto review  twice' } })
  })
  it('accepts fractional and sub-minute Go durations of at least one second', () => {
    for (const every of ['1s', '1.5s', '1000ms', '0.5m', '90s']) {
      expect(parseLoopCommand(`/loop ${every} @qa x`)).toMatchObject({ ok: true, command: { every } })
    }
  })

  it.each([
    ['/loop', /interval/i],
    ['/loop   ', /interval/i],
    ['/loop soon @qa x', /duration/i],
    ['/loop 5 @qa x', /duration/i],
    ['/loop 500ms @qa x', /at least 1s/i],
    ['/loop 0s @qa x', /at least 1s/i],
    ['/loop 5m', /target/i],
    ['/loop 5m check the logs', /target/i],
    ['/loop 5m @ check', /target/i],
    ['/loop 5m --until-stage-end', /target/i],
    ['/loop 5m @qa', /prompt/i],
    ['/loop 5m @qa   \n ', /prompt/i],
    ['/loop 5m --forever @qa x', /--until-stage-end/],
    ['/loop 5m @qa --until-stage-end x', null],
  ])('rejects %j', (draft, error) => {
    const parsed = parseLoopCommand(draft)
    if (error === null) {
      // A flag after the target is simply prompt text.
      expect(parsed).toMatchObject({ ok: true, command: { untilStageEnd: false, prompt: '--until-stage-end x' } })
      return
    }
    expect(parsed.ok).toBe(false)
    expect(parsed.ok === false && parsed.error).toMatch(error)
  })

  // A resolved @target stays visible while the prompt is still being typed: the composer reads it off the
  // failed parse instead of hiding the target row just because the command as a whole isn't done yet.
  it('carries the already-resolved target on the one failure that can follow it — a missing prompt', () => {
    expect(parseLoopCommand('/loop 5m @qa')).toMatchObject({ ok: false, target: 'qa' })
    expect(parseLoopCommand('/loop 5m @qa   \n ')).toMatchObject({ ok: false, target: 'qa' })
  })
  it('carries no target for every other failure — there is none to show yet', () => {
    for (const draft of ['/loop', '/loop soon @qa x', '/loop 5m', '/loop 5m check the logs', '/loop 5m @ check']) {
      expect(parseLoopCommand(draft)).not.toHaveProperty('target')
    }
  })
})

describe('isLoopCommandDraft/parseLoopCommand with leading whitespace (e.g. a paste)', () => {
  it('recognizes and parses a loop command preceded by blanks or a blank line the same as without them', () => {
    for (const lead of [' ', '  ', '\n', '\n\n']) {
      expect(parseLoopCommand(`${lead}/loop 5m @qa проверь логи`)).toEqual({
        ok: true,
        command: { kind: 'add', every: '5m', untilStageEnd: false, target: 'qa', prompt: 'проверь логи' },
      })
    }
    expect(parseLoopCommand(' /loop list')).toEqual({ ok: true, command: { kind: 'list' } })
    expect(parseLoopCommand(' /loop stop L1')).toEqual({ ok: true, command: { kind: 'stop', loopId: 'L1' } })
  })
})

describe('loopSubcommand — Send\'s label while the draft is still being typed', () => {
  it.each([
    ['/loop', null],
    ['/loop 5', 'add'],
    ['/loop 5m @qa x', 'add'],
    ['/loop list', 'list'],
    ['/loop list x', 'list'],
    ['/loop stop', 'stop'],
    ['/loop stop nope', 'stop'],
    [' /loop stop', 'stop'],
  ] as const)('reads %j as %s', (draft, want) => {
    expect(loopSubcommand(draft)).toBe(want)
  })
})

describe('parseLoopCommand — list and stop', () => {
  it('parses list, tolerating surrounding blanks', () => {
    expect(parseLoopCommand('/loop list')).toEqual({ ok: true, command: { kind: 'list' } })
    expect(parseLoopCommand('/loop   list  \n')).toEqual({ ok: true, command: { kind: 'list' } })
  })
  it('parses stop with a normalised loop id', () => {
    expect(parseLoopCommand('/loop stop L1')).toEqual({ ok: true, command: { kind: 'stop', loopId: 'L1' } })
    expect(parseLoopCommand('/loop stop l12 ')).toEqual({ ok: true, command: { kind: 'stop', loopId: 'L12' } })
  })
  it.each([
    ['/loop list now', /no arguments/i],
    ['/loop stop', /which loop/i],
    ['/loop stop everything', /L1/],
    ['/loop stop L0', /L1/],
    ['/loop stop L1 L2', /one id/i],
  ])('rejects %j', (draft, error) => {
    const parsed = parseLoopCommand(draft)
    expect(parsed.ok).toBe(false)
    expect(parsed.ok === false && parsed.error).toMatch(error)
  })
})

describe('loopIntervalSlot — where an interval example goes', () => {
  it('offers the slot right after a bare /loop, with the separating space to add', () => {
    expect(loopIntervalSlot('/loop')).toEqual({ start: 5, end: 5, lead: ' ' })
  })
  it('offers the slot after /loop and its blanks', () => {
    expect(loopIntervalSlot('/loop ')).toEqual({ start: 6, end: 6, lead: '' })
  })
  it('covers the interval typed so far, so an example replaces it', () => {
    expect(loopIntervalSlot('/loop 5')).toEqual({ start: 6, end: 7, lead: '' })
    expect(loopIntervalSlot('/loop 90s')).toEqual({ start: 6, end: 9, lead: '' })
  })
  it('is gone once the interval is finished or another subcommand is being typed', () => {
    expect(loopIntervalSlot('/loop 5m ')).toBeNull()
    expect(loopIntervalSlot('/loop 5m @qa x')).toBeNull()
    expect(loopIntervalSlot('/loop l')).toBeNull()
    expect(loopIntervalSlot('/loop @qa')).toBeNull()
    expect(loopIntervalSlot('/loop list')).toBeNull()
    expect(loopIntervalSlot('/loops')).toBeNull()
    expect(loopIntervalSlot('hello')).toBeNull()
  })
})

describe('resolveLoopTarget', () => {
  const runAgent: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true }
  const helper: MeshParticipant = { id: 'helper:t-2', kind: 'helper', scope: 'stage', stage_id: 's1', alias: 'rev', display_name: 'Reviewer', reachable: true }
  const otherHelper: MeshParticipant = { id: 'helper:t-3', kind: 'helper', scope: 'stage', stage_id: 's2', alias: 'other', display_name: 'Other', reachable: true }
  const stageAgent: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', alias: 's1', display_name: 'Stage', reachable: true }
  const side: MeshParticipant = { id: 'side:r', kind: 'side_agent', scope: 'run', alias: 'assistant', display_name: 'Side', reachable: true }
  const gone: MeshParticipant = { id: 'run:t-9', kind: 'run_agent', scope: 'run', alias: 'old', display_name: 'Old', reachable: false, unreachable_reason: 'task t-9 has reached a terminal status' }
  const twinA: MeshParticipant = { id: 'run:t-4', kind: 'run_agent', scope: 'run', alias: 'twin', reachable: true }
  const twinB: MeshParticipant = { id: 'run:t-5', kind: 'run_agent', scope: 'run', alias: 'twin', reachable: true }
  const roster = [runAgent, helper, otherHelper, stageAgent, side, gone, twinA, twinB]

  it('resolves a run agent by alias and this stage\'s helper by its handle', () => {
    expect(resolveLoopTarget('qa', roster, 's1')).toEqual({ participant: runAgent })
    expect(resolveLoopTarget('REV', roster, 's1')).toEqual({ participant: helper })
  })
  it.each([
    ['nobody', /no one is called @nobody/i],
    ['twin', /matches several/i],
    ['old', /can't receive messages: task t-9/i],
    ['s1', /run agent or a helper of this stage/i],
    ['assistant', /run agent or a helper of this stage/i],
    ['other', /run agent or a helper of this stage/i],
  ])('refuses @%s with a reason', (handle, problem) => {
    const resolved = resolveLoopTarget(handle, roster, 's1')
    expect(resolved).toEqual({ problem: expect.stringMatching(problem) })
  })
})
