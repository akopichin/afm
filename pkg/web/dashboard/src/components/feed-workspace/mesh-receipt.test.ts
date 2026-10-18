import { describe, expect, it } from 'vitest'
import { computeReceiptPhase, RECEIPT_LABEL } from './mesh-receipt'
import type { MeshParticipant } from '../../api/mesh-client'

const reachable: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', reachable: true }
const helper: MeshParticipant = { id: 'helper:t1', kind: 'helper', scope: 'stage', stage_id: 's1', reachable: true }
const runAgent: MeshParticipant = { id: 'run:qa', kind: 'run_agent', scope: 'run', reachable: true }
const terminal: MeshParticipant = { id: 'stage:s1', kind: 'stage_agent', scope: 'stage', stage_id: 's1', reachable: false, unreachable_reason: 'stage s1 has reached a terminal status' }
const noMeshCapability: MeshParticipant = { id: 'stage:s2', kind: 'stage_agent', scope: 'stage', stage_id: 's2', reachable: false, unreachable_reason: 'executor lacks mesh capability' }

const base = { participant: reachable, stagePaused: false, answered: false, inFlight: false }

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

  it('a reachable, actively-running participant shows "delivered"', () => {
    expect(computeReceiptPhase({ ...base, participant: reachable })).toBe('delivered')
  })

  it('every phase has a literal label', () => {
    expect(RECEIPT_LABEL.awaiting).toBe('Awaiting delivery')
    expect(RECEIPT_LABEL.delivered).toBe('Delivered')
    expect(RECEIPT_LABEL.waiting_inbox).toBe('Waiting for an inbox check')
    expect(RECEIPT_LABEL.answered).toBe('Answered')
    expect(RECEIPT_LABEL.paused).toBe('Paused · waiting for Continue')
    expect(RECEIPT_LABEL.unavailable).toBe('Unavailable')
    expect(RECEIPT_LABEL.messaging_unavailable).toBe('Messaging unavailable')
  })
})
