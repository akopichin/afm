import { describe, expect, it } from 'vitest'
import type { MeshMessage } from '../../api/mesh-client'
import { deliveryKey, messageDelivered } from './mesh-receipt'

const message: MeshMessage = { id: 'm1', sender_id: 'user', recipient_ids: ['stage:impl', 'run:qa'], thread_id: 't1', kind: 'message', body: 'Check this', seq: 1, created_at: '2026-10-29T10:00:00Z' }

describe('feed delivery label', () => {
  it('does not call acceptance or a partial recipient receipt delivery', () => {
    expect(messageDelivered(message, [message], new Set())).toBe(false)
    expect(messageDelivered(message, [message], new Set([deliveryKey('m1', 'run:qa')]))).toBe(false)
  })

  it('requires a receipt or an actual reply for every agent recipient', () => {
    const reply = { ...message, id: 'm2', sender_id: 'stage:impl', in_reply_to: 'm1' }
    expect(messageDelivered(message, [message, reply], new Set([deliveryKey('m1', 'run:qa')]))).toBe(true)
    expect(messageDelivered(message, [message, { ...reply, sender_id: 'run:other' }], new Set([deliveryKey('m1', 'run:qa')]))).toBe(false)
  })

  it('treats a displayed human-addressed message as delivered without inventing recipients', () => {
    expect(messageDelivered({ ...message, recipient_ids: ['user'] }, [], new Set())).toBe(true)
    expect(messageDelivered({ ...message, recipient_ids: [] }, [], new Set())).toBe(false)
  })
})
