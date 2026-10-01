import { describe, expect, test } from 'vitest'
import {
  SIDE_AGENT_ERROR_CODES,
  errorCodeToMessage,
  parseSideAgentEvent,
  type SideAgentEvent,
} from './side-agent'

describe('parseSideAgentEvent', () => {
  test('parses the envelope and per-type data (snake_case wire → camelCase)', () => {
    const accepted = parseSideAgentEvent({
      schema_version: 1,
      seq: 1,
      conversation_id: 'run-1',
      turn_id: 't-1',
      type: 'turn_accepted',
      timestamp: '2026-10-01T10:00:00Z',
      data: { message: 'hi', client_message_id: 'c-1' },
    })
    expect(accepted).toEqual({
      schemaVersion: 1,
      seq: 1,
      conversationId: 'run-1',
      turnId: 't-1',
      type: 'turn_accepted',
      timestamp: '2026-10-01T10:00:00Z',
      data: { message: 'hi', clientMessageId: 'c-1' },
    })
  })

  test('maps tool_action/turn_started/turn_failed data fields', () => {
    const tool = parseSideAgentEvent({ seq: 2, type: 'tool_action', data: { tool: 'bash', detail: 'ls', log_ref: 'log/1' } })
    expect(tool?.type).toBe('tool_action')
    expect(tool?.data).toEqual({ tool: 'bash', detail: 'ls', logRef: 'log/1' })

    const started = parseSideAgentEvent({ seq: 3, type: 'turn_started', data: { command: 'claude', context_format_version: 'v2' } })
    expect(started?.data).toEqual({ command: 'claude', contextFormatVersion: 'v2' })

    const failed = parseSideAgentEvent({ seq: 4, type: 'turn_failed', data: { code: 'context_limit', message: 'too long' } })
    expect(failed?.data).toEqual({ code: 'context_limit', message: 'too long' })
  })

  test('defaults missing/malformed data fields instead of throwing', () => {
    const text = parseSideAgentEvent({ seq: 5, type: 'assistant_text' })
    expect(text?.data).toEqual({ text: '' })

    const completed = parseSideAgentEvent({ seq: 6, type: 'turn_completed', data: null })
    expect(completed?.data).toEqual({})
  })

  test('drops an event with an unknown type', () => {
    expect(parseSideAgentEvent({ seq: 7, type: 'something_else', data: {} })).toBeNull()
  })

  test('drops an event without a numeric seq (cannot be deduped/cursored)', () => {
    expect(parseSideAgentEvent({ type: 'assistant_text', data: { text: 'x' } })).toBeNull()
    expect(parseSideAgentEvent({ seq: 'nope', type: 'assistant_text', data: { text: 'x' } })).toBeNull()
  })

  test('drops a non-object input', () => {
    expect(parseSideAgentEvent(null)).toBeNull()
    expect(parseSideAgentEvent('not-an-event')).toBeNull()
  })

  test('discriminated union narrows data by type', () => {
    const e = parseSideAgentEvent({ seq: 8, type: 'turn_failed', data: { code: 'busy', message: 'm' } })
    // Сужение по e.type даёт доступ к code без каста (проверка типа на компиляции).
    const ev = e as SideAgentEvent
    if (ev.type === 'turn_failed') {
      expect(ev.data.code).toBe('busy')
    } else {
      throw new Error('expected turn_failed')
    }
  })
})

describe('errorCodeToMessage', () => {
  test('maps every known error code to a non-empty short text', () => {
    for (const code of SIDE_AGENT_ERROR_CODES) {
      expect(errorCodeToMessage(code).length).toBeGreaterThan(0)
    }
  })

  test('maps selected codes to specific human text', () => {
    expect(errorCodeToMessage('busy')).toBe('Another request is in progress')
    expect(errorCodeToMessage('message_too_large')).toBe('Message too large')
    expect(errorCodeToMessage('context_limit')).toBe('Conversation too long for one request')
  })

  test('falls back to a generic message for an unknown/transport code', () => {
    expect(errorCodeToMessage('forbidden_origin')).toBe('Request failed')
    expect(errorCodeToMessage('totally_unknown')).toBe('Request failed')
  })
})
