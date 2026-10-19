import { describe, expect, test } from 'vitest'
import { messageText, parseAgentResultBody } from './agent-result'

describe('parseAgentResultBody', () => {
  test('recognises a pasted v1 result object', () => {
    const body = '{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":"Could not reach the API."}'
    expect(parseAgentResultBody(body)).toEqual({ verdict: 'inconclusive', summary: 'Could not reach the API.' })
    expect(messageText(body, true)).toBe('Could not reach the API.')
  })

  test("a human's JSON is never reinterpreted", () => {
    const body = '{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}'
    expect(messageText(body, false)).toBe(body)
  })

  test.each([
    ['plain text', 'all good'],
    ['other JSON', '{"kind":"delegate","summary":"x","schema_version":1}'],
    ['no summary', '{"schema_version":1,"kind":"result","verdict":"pass"}'],
    ['no schema_version', '{"kind":"result","verdict":"pass","summary":"ok"}'],
    ['another schema version', '{"schema_version":2,"kind":"result","verdict":"pass","summary":"ok"}'],
    ['unknown verdict', '{"schema_version":1,"kind":"result","verdict":"great","summary":"ok"}'],
    ['broken JSON', '{"schema_version":1,'],
  ])('leaves %s alone', (_, body) => {
    expect(parseAgentResultBody(body)).toBeNull()
    expect(messageText(body, true)).toBe(body)
  })
})
