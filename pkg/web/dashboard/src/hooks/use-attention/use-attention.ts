import { useMemo } from 'react'
import { isGateBlocked } from '../../types'
import type { Stage, StageStatus } from '../../types'

export type AttentionKind = 'dialog' | 'plan' | 'failed' | 'paused' | 'assistants'
export type Attention = { needsAttention: boolean; kind: AttentionKind | null }
export type AttentionEntry = { stage: Stage; kind: AttentionKind }

// ATTENTION_STATUSES — statuses that need attention by themselves. The two helper-gate statuses are not here:
// they are background work unless isGateBlocked, which attentionKindFor decides.
export const ATTENTION_STATUSES: ReadonlySet<StageStatus> = new Set<StageStatus>([
  'awaiting_user_input',
  'awaiting_approval',
  'failed',
  'hook_failed',
  'paused',
])

// attentionKindFor — the one place mapping a stage to the attention it represents, if any. Takes the whole
// stage (not just its status): 'awaiting_assistants' needs the extra awaitingAssistantsBlocked flag to
// decide between "machine work in progress" (no attention) and "a required gate needs a human decision"
// (attention). Exported so callers that need the exact same verdict as useAttention/stagesNeedingAttention
// (e.g. the stage rail's data-attention marker) don't have to re-derive it.
export function attentionKindFor(stage: Stage): AttentionKind | null {
  if (stage.status === 'awaiting_user_input') return 'dialog'
  if (stage.status === 'awaiting_approval') return 'plan'
  if (stage.status === 'failed' || stage.status === 'hook_failed') return 'failed'
  if (stage.status === 'paused') return 'paused'
  if (stage.status === 'awaiting_assistants' || stage.status === 'awaiting_pre_completion') return isGateBlocked(stage) ? 'assistants' : null
  return null
}

export function anyAwaiting(stages: Stage[]): boolean {
  return stages.some((s) => attentionKindFor(s) !== null)
}

export function stagesNeedingAttention(stages: Stage[]): AttentionEntry[] {
  return stages.reduce<AttentionEntry[]>((acc, stage) => {
    const kind = attentionKindFor(stage)
    if (kind !== null) acc.push({ stage, kind })
    return acc
  }, [])
}

export function useAttention(stage: Stage | null): Attention {
  return useMemo(() => {
    if (stage === null) return { needsAttention: false, kind: null }
    const kind = attentionKindFor(stage)
    return { needsAttention: kind !== null, kind }
  }, [stage])
}
