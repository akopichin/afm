import { useMemo } from 'react'
import type { Stage, StageStatus } from '../../types'

export type AttentionKind = 'dialog' | 'plan'
export type Attention = { needsAttention: boolean; kind: AttentionKind | null }

export const ATTENTION_STATUSES: ReadonlySet<StageStatus> = new Set<StageStatus>([
  'awaiting_user_input',
  'awaiting_approval',
])

export function anyAwaiting(stages: Stage[]): boolean {
  return stages.some((s) => ATTENTION_STATUSES.has(s.status))
}

export function useAttention(stage: Stage | null): Attention {
  return useMemo(() => {
    if (stage === null) return { needsAttention: false, kind: null }
    const kind: AttentionKind | null =
      stage.status === 'awaiting_user_input' ? 'dialog'
      : stage.status === 'awaiting_approval' ? 'plan'
      : null
    return { needsAttention: kind !== null, kind }
  }, [stage])
}
