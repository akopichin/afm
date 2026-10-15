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
// A helper-task selection (kind: 'assistant') is intentionally inert to the auto-advance DECISION: a helper
// task runs its own lifecycle entirely separate from its root stage's FSM (a helper's outcome never flips
// its stage's status — see AGENTS.md's collaboration section), so "done" below only ever means a STAGE
// finished. Auto-advance must never fire off of a task's status, and a manually-opened task — even one that
// completed long ago — must never be yanked away because the root stage underneath it finished: the user
// asked to see that task's own feed, not the stage's.
//
// IMPORTANT: the watchingId/wasLive bookkeeping below tracks the UNDERLYING STAGE (via selection?.stageId,
// which both selection kinds carry) and keeps updating EVEN WHILE an 'assistant' selection is open — only
// the two actions that actually MOVE the selection (picking a stage because none is selected, and
// auto-advancing because wasLive && done) are gated on "selection is not an assistant task". Stopping the
// bookkeeping itself while a task is open (an earlier version of this hook did exactly that) leaves
// wasLive.current stale.
//
// That alone is not sufficient, though: wasLive must also be CONSUMED (reset false) the moment a "done"
// observation is SUPPRESSED by the assistant gate specifically — otherwise a transition noticed while a
// nested task was open leaves wasLive === true armed indefinitely, and the belated auto-advance fires the
// instant the user later clicks directly back onto that same, already-done stage, contradicting their manual
// click. This consumption must NOT fire just because a plain (non-gated) attempt found no eligible next
// stage yet — "keeps retrying auto-advance across polls instead of sticking forever" (fast back-to-back
// stages, App.test.tsx) depends on wasLive staying armed across renders until a target actually appears, with
// no detour through an assistant selection in between. The two cases are told apart by WHETHER the move was
// skipped due to isAssistantSelection (consume) vs attempted-but-no-target-yet (stay armed, see the
// `current.status === 'done'` branch below).
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

    const isAssistantSelection = selection !== null && selection.kind === 'assistant'
    const current = stages.find((stage) => stage.id === selection?.stageId) ?? null

    if (current === null) {
      // The task's own root stage vanished from the list — never auto-pick a different stage away from it.
      if (isAssistantSelection) return
      const active = stages.find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status))
      const failed = stages.find((stage) => stage.status === 'failed')
      const next = active ?? failed ?? null

      if (next !== null) {
        setSelection({ kind: 'stage', stageId: next.id })
      }

      return
    }

    if (watchingId.current !== current.id) {
      // A genuinely fresh visit to this stage (including a manual click straight onto an already-finished
      // one) always starts from scratch — no borrowed "was it ever live" state from whatever was watched
      // before, and nothing to advance away from on the very render that establishes it.
      watchingId.current = current.id
      wasLive.current = current.status !== 'done'
      return
    }

    if (current.status !== 'done') {
      wasLive.current = true
      return
    }

    // current.status === 'done' and this is NOT a fresh visit (we were already watching this exact stage).
    if (isAssistantSelection) {
      // The move is suppressed while a nested task is open — but this "done" observation must not survive
      // as an armed, stale auto-advance that fires later when the user returns directly to the stage. Spend
      // it here, once, rather than leaving it to fire retroactively and contradict a manual re-selection.
      wasLive.current = false
      return
    }

    if (!wasLive.current) return

    const fromIndex = stages.findIndex((stage) => stage.id === current.id)
    const nextActive = stages.slice(fromIndex + 1).find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status)) ?? null

    if (nextActive !== null) {
      setSelection({ kind: 'stage', stageId: nextActive.id })
    }
    // Deliberately NOT resetting wasLive.current when nextActive is null — this is the plain (non-gated)
    // "no eligible next stage YET" case, which must keep retrying on later renders/polls until one appears
    // (see the "keeps retrying auto-advance across polls" doc comment above), not get spent on a miss.
  }, [stages, selection, attentionStageId])

  return [selection, setSelection]
}
