import { describe, expect, test } from 'vitest'
import {
  AFM_EVENT_TYPES,
  extractStageStatus,
  SIGNIFICANT_EVENT_TYPES,
} from './afm-event'
import { ACTIVE_STAGE_STATUSES, STAGE_STATUS_LABELS } from './stage'

describe('extractStageStatus', () => {
  test('string payload with a valid status returns that status', () => {
    expect(extractStageStatus('running')).toBe('running')
  })

  test('string payload with an unrecognized status returns null', () => {
    expect(extractStageStatus('bogus_status')).toBeNull()
  })

  test('object payload with a valid status field returns that status', () => {
    expect(extractStageStatus({ status: 'done' })).toBe('done')
  })

  test('object payload with an unrecognized status value returns null', () => {
    expect(extractStageStatus({ status: 'bogus_status' })).toBeNull()
  })

  test('object payload with a non-string status field returns null', () => {
    expect(extractStageStatus({ status: 42 })).toBeNull()
  })

  test('null payload returns null', () => {
    expect(extractStageStatus(null)).toBeNull()
  })

  test('number payload returns null', () => {
    expect(extractStageStatus(42)).toBeNull()
  })

  test('array payload returns null', () => {
    expect(extractStageStatus(['running'])).toBeNull()
  })

  test('object payload without a status field returns null', () => {
    expect(extractStageStatus({ foo: 'bar' })).toBeNull()
  })
})

describe('STAGE_STATUS_LABELS', () => {
  test('contains exactly the documented status -> label pairs', () => {
    expect(STAGE_STATUS_LABELS).toEqual({
      pending: 'Pending',
      planning: 'Planning',
      awaiting_approval: 'Awaiting approval',
      revising: 'Revising',
      ready: 'Ready',
      running: 'Running',
      done: 'Done',
      failed: 'Failed',
      retrying: 'Retrying',
      awaiting_user_input: 'Awaiting reply',
    })
  })
})

describe('ACTIVE_STAGE_STATUSES', () => {
  test('contains exactly the documented "in progress" statuses', () => {
    expect(ACTIVE_STAGE_STATUSES).toEqual(
      new Set(['running', 'planning', 'revising', 'retrying', 'awaiting_user_input']),
    )
  })
})

describe('SIGNIFICANT_EVENT_TYPES', () => {
  test('contains exactly the documented refresh-triggering event types', () => {
    expect(SIGNIFICANT_EVENT_TYPES).toEqual(
      new Set([
        'stage_status_changed',
        'approved',
        'revised',
        'retry_scheduled',
        'retry_exhausted',
        'manual_retry',
        'ask_user',
        'user_answered',
        'agent_completed',
      ]),
    )
  })
})

describe('AFM_EVENT_TYPES', () => {
  test('contains exactly the documented set of known WS event types', () => {
    expect(AFM_EVENT_TYPES).toEqual([
      'stage_status_changed',
      'approved',
      'revised',
      'retry_scheduled',
      'retry_exhausted',
      'manual_retry',
      'ask_user',
      'user_answered',
      'agent_action',
      'agent_completed',
      'supervisor_decision',
    ])
  })
})
