import { describe, expect, it } from 'vitest'
import { parseAssistantActivity, parseAssistantActivityPage } from './assistant-activity'

const wire = { event_id: 'act-1', task_id: 't1', participant_id: 'p1', turn: 2, occurred_at: '2026-10-27T12:00:00Z', tool: 'text', detail: 'hi' }

describe('parseAssistantActivity', () => {
  it('maps the wire fields and defaults the optional ones', () => {
    expect(parseAssistantActivity(wire)).toEqual({
      eventId: 'act-1', taskId: 't1', participantId: 'p1', turn: 2, occurredAt: '2026-10-27T12:00:00Z',
      tool: 'text', detail: 'hi', truncated: false, cursor: undefined,
    })
  })

  it('accepts a string or numeric cursor and the truncated flag', () => {
    expect(parseAssistantActivity({ ...wire, cursor: '120', truncated: true })).toMatchObject({ cursor: '120', truncated: true })
    expect(parseAssistantActivity({ ...wire, cursor: 120 })?.cursor).toBe('120')
    expect(parseAssistantActivity({ ...wire, cursor: 'abc' })?.cursor).toBeUndefined()
  })

  it('rejects a payload without the identity fields', () => {
    expect(parseAssistantActivity({ ...wire, event_id: '' })).toBeNull()
    expect(parseAssistantActivity({ ...wire, task_id: undefined })).toBeNull()
    expect(parseAssistantActivity('x')).toBeNull()
  })
})

describe('parseAssistantActivityPage', () => {
  it('skips malformed rows and reads the cursors', () => {
    const page = parseAssistantActivityPage({ entries: [wire, { nope: true }], after: '55', before: '0', has_more: true, has_older: false })
    expect(page.entries).toHaveLength(1)
    expect(page).toMatchObject({ after: '55', before: '0', hasMore: true, hasOlder: false })
  })

  it('treats a null entries list as empty', () => {
    expect(parseAssistantActivityPage({ entries: null }).entries).toEqual([])
  })
})
