import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { ACTIVE_STAGE_STATUSES, type Stage } from '../../types'

// Selection — what the workspace currently shows: a whole stage, or one specific helper-task feed nested
// under a stage (collaboration feature, Этап 3, dev-plan.md §14.2). A tagged union, not a concatenated
// string: encoding this as `${stageId}:${taskId}` would make "is this a task or a stage" depend on string
// shape instead of the type checker, and would silently misparse the moment either id could contain ':'.
export type Selection =
  | { kind: 'stage'; stageId: string }
  | { kind: 'assistant'; stageId: string; taskId: string }

// Follow a live stage through completion. A stage selected after it finished
// stays selected; if the next stage starts between polls, retry on later polls.
//
// A helper-task selection (kind: 'assistant') is intentionally inert to all of this: a helper task runs its
// own lifecycle entirely separate from its root stage's FSM (a helper's outcome never flips its stage's
// status — see AGENTS.md's collaboration section), so "done" below only ever means a STAGE finished.
// Auto-advance must never fire off of a task's status, and a manually-opened task — even one that completed
// long ago — must never be yanked away because the root stage underneath it finished: the user asked to see
// that task's own feed, not the stage's.
export function useSelectedStage(
  stages: Stage[],
  attentionStageId: string | null,
): [Selection | null, Dispatch<SetStateAction<Selection | null>>] {
  const [selection, setSelection] = useState<Selection | null>(null)
  const watchingId = useRef<string | null>(null)
  const wasLive = useRef(false)

  // Keep the rail selection after the workspace resolves an attention item. Attention is always about a
  // whole stage (plan approval/dialog/failure/pause) — never a helper task.
  useEffect(() => {
    if (attentionStageId !== null) setSelection({ kind: 'stage', stageId: attentionStageId })
  }, [attentionStageId])

  useEffect(() => {
    // An attention item is already being routed to by the effect above THIS render — let it win outright
    // rather than race it. Without this guard, a render where attentionStageId is already non-null on first
    // mount (selection still null) would have both effects fire in the same commit and this one would
    // clobber the attention target with "first active stage" since it still sees the stale null selection.
    if (attentionStageId !== null) return
    if (stages.length === 0) return
    // A helper task is selected — never auto-move away from it (see doc comment above).
    if (selection !== null && selection.kind === 'assistant') return

    const current = stages.find((stage) => stage.id === selection?.stageId) ?? null

    if (current === null) {
      const active = stages.find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status))
      const failed = stages.find((stage) => stage.status === 'failed')
      const next = active ?? failed ?? null

      if (next !== null) {
        setSelection({ kind: 'stage', stageId: next.id })
      }

      return
    }

    if (watchingId.current !== current.id) {
      watchingId.current = current.id
      wasLive.current = current.status !== 'done'
    } else if (current.status !== 'done') {
      wasLive.current = true
    }

    if (wasLive.current && current.status === 'done') {
      const fromIndex = stages.findIndex((stage) => stage.id === current.id)
      const nextActive = stages.slice(fromIndex + 1).find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status)) ?? null

      if (nextActive !== null) {
        setSelection({ kind: 'stage', stageId: nextActive.id })
      }
    }
  }, [stages, selection, attentionStageId])

  return [selection, setSelection]
}
