import { describe, expect, it } from 'vitest'
import { computeReceiptPhase, meshDeliveries, sideAgentFailures, sideAgentOutcome, RECEIPT_LABEL } from './mesh-receipt'
import type { MeshParticipant } from '../../api/mesh-client'

const reachable: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', reachable: true }
const helper: MeshParticipant = { id: 'helper:t1', kind: 'helper', scope: 'stage', stage_id: 's1', reachable: true }
const runAgent: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', reachable: true }
const terminal: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
const noMeshCapability: MeshParticipant = { id: 'stage:s2', kind: 'stage_agent', scope: 'stage', stage_id: 's2', reachable: false, unreachable_reason: 'executor lacks mesh capability' }

const base = { participant: reachable, stagePaused: false, answered: false, inFlight: false, delivered: false }

describe('computeReceiptPhase', () => {
  it('an answer wins over every other signal', () => {
    expect(computeReceiptPhase({ ...base, participant: terminal, stagePaused: true, inFlight: true, answered: true })).toBe('answered')
  })

  it('an in-flight send shows "awaiting delivery" even with no participant data yet', () => {
    expect(computeReceiptPhase({ ...base, participant: undefined, inFlight: true })).toBe('awaiting')
  })

  it('a known unreachable participant shows "unavailable"', () => {
    expect(computeReceiptPhase({ ...base, participant: terminal })).toBe('unavailable')
  })

  it('an unreachable reason naming mesh capability shows "messaging unavailable"', () => {
    expect(computeReceiptPhase({ ...base, participant: noMeshCapability })).toBe('messaging_unavailable')
  })

  it('a paused stage shows "paused" ahead of a delivery guess', () => {
    expect(computeReceiptPhase({ ...base, stagePaused: true })).toBe('paused')
  })

  it('a helper participant defaults to "waiting for an inbox check"', () => {
    expect(computeReceiptPhase({ ...base, participant: helper })).toBe('waiting_inbox')
  })

  it('a run_agent participant defaults to "waiting for an inbox check"', () => {
    expect(computeReceiptPhase({ ...base, participant: runAgent })).toBe('waiting_inbox')
  })

  it('a participant not yet visible in the roster also defaults to "waiting for an inbox check" (not unavailable)', () => {
    expect(computeReceiptPhase({ ...base, participant: undefined })).toBe('waiting_inbox')
  })

  it('a reachable stage agent without a delivery receipt is only accepted — never "delivered" from the roster', () => {
    expect(computeReceiptPhase({ ...base, participant: reachable })).toBe('waiting_inbox')
  })

  it('"delivered" only with the real delivery receipt of that recipient', () => {
    expect(computeReceiptPhase({ ...base, participant: reachable, delivered: true })).toBe('delivered')
    expect(computeReceiptPhase({ ...base, participant: helper, delivered: true })).toBe('delivered')
  })

  it('every phase has a literal label', () => {
    expect(RECEIPT_LABEL.awaiting).toBe('Awaiting delivery')
    expect(RECEIPT_LABEL.delivered).toBe('Delivered')
    expect(RECEIPT_LABEL.waiting_inbox).toBe('Accepted · waiting for an inbox check')
    expect(RECEIPT_LABEL.answered).toBe('Answered')
    expect(RECEIPT_LABEL.paused).toBe('Paused · waiting for Continue')
    expect(RECEIPT_LABEL.unavailable).toBe('Unavailable')
    expect(RECEIPT_LABEL.messaging_unavailable).toBe('Messaging unavailable')
  })
})

describe('meshDeliveries', () => {
  it('collects the delivered/acknowledged receipts of each message and recipient from the run events', () => {
    const ev = (payload: unknown) => ({ type: 'mesh_receipt', payload, stageId: '', timestamp: '' })
    const d = meshDeliveries([
      ev({ message_id: 'm1', participant_id: 'stage:s1', status: 'delivered' }),
      ev({ message_id: 'm1', participant_id: 'run:qa', status: 'acknowledged' }),
      ev({ message_id: 'm1', participant_id: 'side:r1', status: 'undeliverable' }),
      { type: 'mesh_message', payload: { message_id: 'm2' }, stageId: '', timestamp: '' },
    ])
    expect(d.has('m1|stage:s1')).toBe(true)
    expect(d.has('m1|run:qa')).toBe(true)
    expect(d.has('m1|side:r1')).toBe(false)
    expect(d.has('m2|stage:s1')).toBe(false)
  })
})

describe('sideAgentOutcome', () => {
  const original = { id: 'm1', thread_id: 'mt1', sender_id: 'user', recipient_ids: ['side:r1'], kind: 'message', body: 'help', seq: 1, created_at: '' }
  it('distinguishes linked success, linked passive failure and a durable failure notice', () => {
    const reply = { ...original, id: 'm2', sender_id: 'side:r1', recipient_ids: ['user'], in_reply_to: 'm1', kind: 'reply', auto_result: true }
    expect(sideAgentOutcome(original, [original, reply], new Set())).toBe('answered')
    expect(sideAgentOutcome(original, [original, { ...reply, kind: 'result' }], new Set())).toBe('could_not_answer')
    const failures = sideAgentFailures([{ type: 'side_agent_mesh_failure', payload: { source_message_id: 'm1' }, stageId: '', timestamp: '' }])
    expect(sideAgentOutcome(original, [original], failures)).toBe('could_not_answer')
    expect(sideAgentOutcome({ ...original, recipient_ids: ['run:qa'] }, [original, reply], failures)).toBeNull()
  })

  it('treats an undeliverable side receipt as failure before the linked reply refreshes', () => {
    const receipts = [
      { type: 'mesh_receipt', payload: { message_id: 'm1', participant_id: 'side:r1', status: 'undeliverable' }, stageId: '', timestamp: '' },
      { type: 'mesh_receipt', payload: { message_id: 'm1', participant_id: 'run:qa', status: 'delivered' }, stageId: '', timestamp: '' },
    ]
    const failures = sideAgentFailures(receipts)
    expect(failures.has('m1')).toBe(true)
    expect(sideAgentOutcome(original, [original], failures)).toBe('could_not_answer')
    expect(meshDeliveries(receipts).has('m1|side:r1')).toBe(false)
    expect(meshDeliveries(receipts).has('m1|run:qa')).toBe(true)
  })

  it('recovers an old side failure from the message when the capped event replay has no notice', () => {
    const failed = { ...original, side_agent_failed: true }
    expect(sideAgentOutcome(failed, [failed], new Set())).toBe('could_not_answer')
    // The composer only knows the sent id; the stage message poll carries the durable flag.
    expect(sideAgentOutcome({ id: 'm1', recipient_ids: ['side:r1'] }, [failed], new Set(), 'side:r1')).toBe('could_not_answer')
    expect(sideAgentOutcome({ id: 'm1', recipient_ids: ['run:qa'] }, [failed], new Set())).toBeNull()
  })

  it('does not call a passive auto result a failed side request after orphan cleanup', () => {
    const passive = { ...original, kind: 'result', auto_result: true, side_agent_failed: true }
    const failures = new Set(['m1'])
    expect(sideAgentOutcome(passive, [passive], failures)).toBeNull()
    expect(sideAgentOutcome({ id: 'm1', recipient_ids: ['side:r1'] }, [passive], failures)).toBeNull()
  })
})
