import { describe, expect, test } from 'vitest'
import { messageText, parseAgentResultBody } from './agent-result'

describe('parseAgentResultBody', () => {
  test('recognises a pasted result object', () => {
    const body = '{"schema_version":1,"kind":"result","verdict":"inconclusive","summary":"Could not reach the API."}'
    expect(parseAgentResultBody(body)).toEqual({ verdict: 'inconclusive', summary: 'Could not reach the API.' })
    expect(messageText(body)).toBe('Could not reach the API.')
  })

  test.each([
    ['plain text', 'all good'],
    ['other JSON', '{"kind":"delegate","summary":"x","schema_version":1}'],
    ['no summary', '{"schema_version":1,"kind":"result","verdict":"pass"}'],
    ['no schema_version', '{"kind":"result","verdict":"pass","summary":"ok"}'],
    ['broken JSON', '{"schema_version":1,'],
  ])('leaves %s alone', (_, body) => {
    expect(parseAgentResultBody(body)).toBeNull()
    expect(messageText(body)).toBe(body)
  })
})
