import { describe, expect, test } from 'vitest'
import type { MeshParticipant, MeshThread } from '../api/mesh-client'
import { boardNotifyCandidates, findExistingDirectThread, mentionHandle, mentionHandles, participantInitials, participantLabel, senderLabel, threadLabel } from './mesh-roster'

const side: MeshParticipant = { id: 'side:run-20261018-162731-b5f0', kind: 'side_agent', scope: 'run', alias: 'side', display_name: 'claude', reachable: true }
const commit: MeshParticipant = { id: 'stage:commit', kind: 'stage_agent', scope: 'stage', alias: 'commit', display_name: 'Commit & cleanup', stage_id: 'commit', reachable: true }
const bare: MeshParticipant = { id: 'stage:x', kind: 'stage_agent', scope: 'stage', reachable: true }

describe('mesh-roster', () => {
  test('initials are two letters: capitals, word starts, or the start of a single word', () => {
    expect(participantInitials('QA Engineer')).toBe('QA')
    expect(participantInitials('API reviewer')).toBe('AP')
    expect(participantInitials('Stats endpoint')).toBe('SE')
    expect(participantInitials('Persistence')).toBe('PE')
    expect(participantInitials('claude')).toBe('CL')
  })

  test('participantLabel prefers display_name, then alias, then id', () => {
    expect(participantLabel(side)).toBe('claude')
    expect(participantLabel({ ...side, display_name: '' })).toBe('side')
    expect(participantLabel(bare)).toBe('stage:x')
  })

  test('mentionHandle uses the short server alias instead of the run-scoped id', () => {
    expect(mentionHandle(side)).toBe('side')
    expect(mentionHandle(bare)).toBe('stage:x')
  })

  test('mentionHandles falls back to the id when two participants share an alias, so one handle never means two people', () => {
    const qa1: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', reachable: true }
    const qa2: MeshParticipant = { id: 'run:t-2', kind: 'run_agent', scope: 'run', alias: 'QA', reachable: true }
    const handles = mentionHandles([qa1, qa2, side])
    expect(handles.get('run:t-1')).toBe('run:t-1')
    expect(handles.get('run:t-2')).toBe('run:t-2')
    expect(handles.get(side.id)).toBe('side')
  })

  test('senderLabel names the operator "You" and known participants by name', () => {
    expect(senderLabel('user', [side])).toBe('You')
    expect(senderLabel(side.id, [side])).toBe('claude')
    expect(senderLabel('stage:gone', [side])).toBe('gone')
    expect(senderLabel('run:t-b28cb0b807dbb957913e', [])).toBe('Agent')
  })

  test('threadLabel keeps an explicit title', () => {
    const t: MeshThread = { id: 'mt-1', title: 'Hand-off', scope: 'run', members: ['user', commit.id], status: 'open' }
    expect(threadLabel(t, [commit])).toBe('Hand-off')
  })

  test('an untitled direct thread is named after the other members, never its mt- id', () => {
    const direct: MeshThread = { id: 'mt-e95e00425b9b4640a0bc', title: '', scope: 'run', members: ['user', commit.id], status: 'open' }
    expect(threadLabel(direct, [commit])).toBe('Commit & cleanup')
    const group: MeshThread = { id: 'mt-2', scope: 'run', members: ['user', commit.id, side.id], status: 'open' }
    expect(threadLabel(group, [commit, side])).toBe('Commit & cleanup, claude')
  })

  test('an untitled thread with no other members falls back to its id', () => {
    const lonely: MeshThread = { id: 'mt-3', scope: 'run', members: ['user'], status: 'open' }
    expect(threadLabel(lonely, [])).toBe('mt-3')
  })

  test('direct lookup uses the newest open untitled two-member thread by durable message seq', () => {
    const dm = (id: string, title = '', status = 'open', members = ['user', commit.id]): MeshThread =>
      ({ id, title, scope: 'run', members, status })
    const threads = [dm('older'), dm('task', 'Task title'), dm('newer'), dm('closed', '', 'closed'), dm('group', '', 'open', ['user', commit.id, side.id])]
    const summaries = {
      older: { last_message: { id: 'm10', seq: 10, created_at: '', sender_id: commit.id, preview: '' }, last_other_message_seq: 10, title_seq: 0 },
      newer: { last_message: { id: 'm20', seq: 20, created_at: '', sender_id: commit.id, preview: '' }, last_other_message_seq: 20, title_seq: 0 },
      task: { last_message: { id: 'm30', seq: 30, created_at: '', sender_id: commit.id, preview: '' }, last_other_message_seq: 30, title_seq: 0 },
    }
    expect(findExistingDirectThread(threads, commit.id, summaries)?.id).toBe('newer')
    expect(findExistingDirectThread([dm('task', 'Task title')], commit.id, summaries)).toBeNull()
  })

  test('boardNotifyCandidates keeps reachable run agents and stage agents of working stages', () => {
    const runAgent: MeshParticipant = { id: 'run:t-1', kind: 'run_agent', scope: 'run', reachable: true }
    const goneRunAgent: MeshParticipant = { ...runAgent, id: 'run:t-2', reachable: false }
    const waitingStage: MeshParticipant = { ...commit, id: 'stage:plan', stage_id: 'plan' }
    const unknownStage: MeshParticipant = { ...commit, id: 'stage:ghost', stage_id: 'ghost' }
    const stages = [
      { id: 'commit', status: 'running' },
      { id: 'plan', status: 'awaiting_approval' },
    ]
    expect(boardNotifyCandidates([runAgent, goneRunAgent, commit, waitingStage, unknownStage, side], stages)).toEqual([runAgent, commit])
  })
})
