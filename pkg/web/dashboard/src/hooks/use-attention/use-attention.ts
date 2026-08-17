import { useMemo } from 'react'
import type { Stage, StageStatus } from '../../types'

export type AttentionKind = 'dialog' | 'plan' | 'failed' | 'paused'
export type Attention = { needsAttention: boolean; kind: AttentionKind | null }
export type AttentionEntry = { stage: Stage; kind: AttentionKind }

export const ATTENTION_STATUSES: ReadonlySet<StageStatus> = new Set<StageStatus>([
  'awaiting_user_input',
  'awaiting_approval',
  'failed',
  'hook_failed',
  'paused',
])

function attentionKindFor(status: StageStatus): AttentionKind | null {
  if (status === 'awaiting_user_input') return 'dialog'
  if (status === 'awaiting_approval') return 'plan'
  if (status === 'failed' || status === 'hook_failed') return 'failed'
  if (status === 'paused') return 'paused'
  return null
}

export function anyAwaiting(stages: Stage[]): boolean {
  return stages.some((s) => ATTENTION_STATUSES.has(s.status))
}

export function stagesNeedingAttention(stages: Stage[]): AttentionEntry[] {
  return stages.reduce<AttentionEntry[]>((acc, stage) => {
    const kind = attentionKindFor(stage.status)
    if (kind !== null) acc.push({ stage, kind })
    return acc
  }, [])
}

export function useAttention(stage: Stage | null): Attention {
  return useMemo(() => {
    if (stage === null) return { needsAttention: false, kind: null }
    const kind = attentionKindFor(stage.status)
    return { needsAttention: kind !== null, kind }
  }, [stage])
}
