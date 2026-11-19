import { describe, expect, it } from 'vitest'
import { HELPER_TRIGGERS, HELPER_TRIGGER_LABELS, helperTriggerLabel } from './helper-trigger'

describe('helperTriggerLabel', () => {
  it('labels every known trigger', () => {
    expect(HELPER_TRIGGERS.map((t) => helperTriggerLabel(t))).toEqual([
      'One shot', 'On stage completion', 'At checkpoints', 'On message', 'Pre-completion review',
    ])
    expect(Object.keys(HELPER_TRIGGER_LABELS).sort()).toEqual([...HELPER_TRIGGERS].sort())
  })

  it('keeps an unknown trigger as text', () => {
    expect(helperTriggerLabel('on_full_moon')).toBe('on_full_moon')
    expect(helperTriggerLabel('toString')).toBe('toString')
  })

  it('appends the loop period', () => {
    expect(helperTriggerLabel('checkpoints', '5m')).toBe('At checkpoints · every 5m')
    expect(helperTriggerLabel('one_shot', '')).toBe('One shot')
  })
})
