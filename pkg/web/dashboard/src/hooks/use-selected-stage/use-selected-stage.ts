import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { ACTIVE_STAGE_STATUSES, type Stage } from '../../types'

// Follow a live stage through completion. A stage selected after it finished
// stays selected; if the next stage starts between polls, retry on later polls.
export function useSelectedStage(stages: Stage[], attentionStageId: string | null): [string | null, Dispatch<SetStateAction<string | null>>] {
  const [selectedStageId, setSelectedStageId] = useState<string | null>(null)
  const watchingId = useRef<string | null>(null)
  const wasLive = useRef(false)

  // Keep the rail selection after the workspace resolves an attention item.
  useEffect(() => {
    if (attentionStageId !== null) setSelectedStageId(attentionStageId)
  }, [attentionStageId])

  useEffect(() => {
    if (stages.length === 0) return

    const current = stages.find((stage) => stage.id === selectedStageId) ?? null

    if (current === null) {
      const active = stages.find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status))
      const failed = stages.find((stage) => stage.status === 'failed')
      const next = active ?? failed ?? null

      if (next !== null) {
        setSelectedStageId(next.id)
      }

      return
    }

    if (watchingId.current !== selectedStageId) {
      watchingId.current = selectedStageId
      wasLive.current = current.status !== 'done'
    } else if (current.status !== 'done') {
      wasLive.current = true
    }

    if (wasLive.current && current.status === 'done') {
      const fromIndex = stages.findIndex((stage) => stage.id === selectedStageId)
      const nextActive = stages.slice(fromIndex + 1).find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status)) ?? null

      if (nextActive !== null) {
        setSelectedStageId(nextActive.id)
      }
    }
  }, [stages, selectedStageId])

  return [selectedStageId, setSelectedStageId]
}
