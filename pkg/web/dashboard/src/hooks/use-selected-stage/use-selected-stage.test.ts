import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useSelectedStage, type Selection } from './use-selected-stage'
import type { Stage, StageStatus } from '../../types'

function stage(id: string, status: StageStatus): Stage {
  return {
    id, name: id, status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false,
    hasDialog: false, showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [],
  }
}

describe('useSelectedStage — stage selections (unchanged real-stage behavior)', () => {
  it('auto-selects the first active stage when nothing is selected yet', () => {
    const stages = [stage('a', 'done'), stage('b', 'running')]
    const { result } = renderHook(() => useSelectedStage(stages, null))
    expect(result.current[0]).toEqual({ kind: 'stage', stageId: 'b' })
  })

  it('auto-advances to the next active stage once the watched stage completes', () => {
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'running'), stage('b', 'pending')] },
    })
    expect(result.current[0]).toEqual({ kind: 'stage', stageId: 'a' })

    rerender({ stages: [stage('a', 'done'), stage('b', 'running')] })
    expect(result.current[0]).toEqual({ kind: 'stage', stageId: 'b' })
  })

  it('does not advance away from a stage the user opened manually after it already finished', () => {
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'done'), stage('b', 'running')] },
    })
    // Manual pick of the already-done stage (as handleSelectStage would do).
    act(() => result.current[1]({ kind: 'stage', stageId: 'a' }))
    rerender({ stages: [stage('a', 'done'), stage('b', 'running')] })
    expect(result.current[0]).toEqual({ kind: 'stage', stageId: 'a' })
  })

  it('jumps to the attention stage id when one is reported', () => {
    const stages = [stage('a', 'running'), stage('b', 'awaiting_approval')]
    const { result } = renderHook(() => useSelectedStage(stages, 'b'))
    expect(result.current[0]).toEqual({ kind: 'stage', stageId: 'b' })
  })
})

describe('TestSelection_ChildSelectable', () => {
  it('accepts an assistant-task selection set explicitly (not derivable from Stage[] alone)', () => {
    const stages = [stage('a', 'running')]
    const { result } = renderHook(() => useSelectedStage(stages, null))

    act(() => result.current[1]({ kind: 'assistant', stageId: 'a', taskId: 't1' }))

    expect(result.current[0]).toEqual({ kind: 'assistant', stageId: 'a', taskId: 't1' })
  })
})

describe('TestSelection_SurvivesCollapseOfAncestor', () => {
  it('keeps an assistant-task selection across unrelated stage-list updates (collapsing the rail branch is pure UI state the hook never sees)', () => {
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'running')] },
    })
    act(() => result.current[1]({ kind: 'assistant', stageId: 'a', taskId: 't1' }))

    // Simulate several poll ticks passing (the kind of re-render a rail-branch collapse/expand would also
    // cause upstream) — the selection must not move.
    rerender({ stages: [stage('a', 'running')] })
    rerender({ stages: [stage('a', 'running')] })
    expect(result.current[0]).toEqual({ kind: 'assistant', stageId: 'a', taskId: 't1' })
  })
})

describe('helper-task completion is not stage completion', () => {
  it('does not auto-advance when a helper task would notionally be "done" — the hook has no notion of task status at all, only stage status', () => {
    // There is no task-status input to this hook by design (AGENTS.md: a helper's outcome never flips its
    // root stage's FSM status) — the only lever that could wrongly fire auto-advance is the STAGE'S OWN
    // status, which stays 'running' here throughout.
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'running')] },
    })
    act(() => result.current[1]({ kind: 'assistant', stageId: 'a', taskId: 't1' }))
    rerender({ stages: [stage('a', 'running')] })
    expect(result.current[0]).toEqual({ kind: 'assistant', stageId: 'a', taskId: 't1' })
  })

  it('a manually-opened COMPLETED helper task is not switched away from even after its root stage finishes', () => {
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'running'), stage('b', 'pending')] },
    })
    act(() => result.current[1]({ kind: 'assistant', stageId: 'a', taskId: 't1' }))

    // Root stage 'a' finishes and 'b' starts while the user is looking at the (now-completed) helper task.
    rerender({ stages: [stage('a', 'done'), stage('b', 'running')] })
    expect(result.current[0]).toEqual({ kind: 'assistant', stageId: 'a', taskId: 't1' })
  })
})

describe('a new child task appearing does not steal the current selection', () => {
  it('stays on the selected stage/assistant regardless of how many helper tasks exist (the hook never reads task data)', () => {
    const { result, rerender } = renderHook(({ stages }: { stages: Stage[] }) => useSelectedStage(stages, null), {
      initialProps: { stages: [stage('a', 'running')] },
    })
    const before: Selection | null = result.current[0]
    rerender({ stages: [stage('a', 'running')] })
    expect(result.current[0]).toEqual(before)
  })
})
