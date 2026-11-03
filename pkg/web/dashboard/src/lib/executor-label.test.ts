import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { executorLabel, executorTooltip, rawCommandTooltipsEnabled, resetUnknownCommandLabels, stepTooltip } from './executor-label'

function setRawTooltipVariable(value: string | null): void {
  const style = document.documentElement.style
  if (value === null) style.removeProperty('--afm-raw-command-tooltip')
  else style.setProperty('--afm-raw-command-tooltip', value)
}

beforeEach(() => {
  resetUnknownCommandLabels()
  setRawTooltipVariable(null)
})
afterEach(() => setRawTooltipVariable(null))

describe('executorLabel', () => {
  test('uses the server command_name and keeps the raw command', () => {
    expect(executorLabel('codex-as-claude', 'Codex Reviewer')).toEqual({ label: 'Codex Reviewer', raw: 'codex-as-claude' })
  })

  test('without command_name numbers each raw command once per page with the default prefix', () => {
    expect(executorLabel('./a.sh', undefined).label).toBe('Unknown-1')
    expect(executorLabel('./b.sh', undefined).label).toBe('Unknown-2')
    expect(executorLabel('./a.sh', '').label).toBe('Unknown-1')
  })

  test('uses the configured prefix from /api/status', () => {
    expect(executorLabel('./a.sh', undefined, 'test')).toEqual({ label: 'test-1', raw: './a.sh' })
    expect(executorLabel('./b.sh', undefined, 'test').label).toBe('test-2')
  })

  test('an empty command has no label and does not consume a number', () => {
    expect(executorLabel('', undefined)).toEqual({ label: '', raw: '' })
    expect(executorLabel('./a.sh', undefined).label).toBe('Unknown-1')
  })

  test('does not number a command that has a server name', () => {
    executorLabel('./named.sh', 'Named')
    expect(executorLabel('./a.sh', undefined).label).toBe('Unknown-1')
  })
})

describe('raw command tooltips (--afm-raw-command-tooltip)', () => {
  test('are on by default and for any value other than none', () => {
    expect(rawCommandTooltipsEnabled()).toBe(true)
    setRawTooltipVariable('block')
    expect(rawCommandTooltipsEnabled()).toBe(true)
  })

  test('the skin variable none turns them off', () => {
    setRawTooltipVariable(' none ')
    expect(rawCommandTooltipsEnabled()).toBe(false)
  })

  test('executorTooltip is the full name plus the raw command on a second line', () => {
    expect(executorTooltip({ label: 'Codex Reviewer', raw: 'codex-as-claude' })).toBe('Codex Reviewer\nCommand: codex-as-claude')
    expect(executorTooltip({ label: 'Codex Reviewer', raw: '' })).toBe('Codex Reviewer')
  })

  test('executorTooltip drops the raw command when the skin hides it', () => {
    setRawTooltipVariable('none')
    expect(executorTooltip({ label: 'Codex Reviewer', raw: 'codex-as-claude' })).toBe('Codex Reviewer')
  })

  test('stepTooltip is "step N · raw", and only "step N" when hidden or without a command', () => {
    expect(stepTooltip(1, 'make test')).toBe('step 1 · make test')
    expect(stepTooltip(1, '')).toBe('step 1')
    setRawTooltipVariable('none')
    expect(stepTooltip(1, 'make test')).toBe('step 1')
  })
})
