import { useEffect, useState } from 'react'
import { listAssistantProfiles } from '../../api/run-client'
import type { AssistantProfile } from '../../types/assistant-profile'

// Profiles are config-driven (flow.yaml/collaboration.profiles) — they don't change mid-run, so a slow poll
// is enough to pick up a restart with an edited config without needing a dedicated push channel.
const POLL_INTERVAL_MS = 15000

const NO_PROFILES: AssistantProfile[] = []

// useAssistantProfiles feeds the side-agent composer's @mention menu (T4.2) with the configured helper
// roles — GET /api/assistant-profiles, gated by the collaboration capability (independent of side_agent
// itself: a run can have collaboration without the side-agent dashboard chat, or vice versa). enabled=false
// → empty list, no network calls, mirroring useStageAssistants' posture. With stageId the executor and
// availability are resolved in that stage's context; without it they are run-level. A list loaded for one
// stage is never returned for another, so a dialog cannot show the previous stage's executor.
export function useAssistantProfiles(enabled: boolean, stageId?: string): AssistantProfile[] {
  const [loaded, setLoaded] = useState<{ stageId: string | undefined; profiles: AssistantProfile[] }>({ stageId, profiles: NO_PROFILES })

  useEffect(() => {
    if (!enabled) {
      setLoaded({ stageId, profiles: NO_PROFILES })
      return
    }
    let cancelled = false
    const load = (): void => {
      listAssistantProfiles(stageId)
        .then((res) => {
          if (!cancelled) setLoaded({ stageId, profiles: res.profiles })
        })
        .catch(() => {
          /* transient error — keep whatever we had, same posture as useStageAssistants */
        })
    }
    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [enabled, stageId])

  return enabled && loaded.stageId === stageId ? loaded.profiles : NO_PROFILES
}
