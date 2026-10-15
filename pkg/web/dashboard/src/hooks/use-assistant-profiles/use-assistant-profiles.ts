import { useEffect, useState } from 'react'
import { listAssistantProfiles } from '../../api/run-client'
import type { AssistantProfile } from '../../types/assistant-profile'

// Profiles are config-driven (flow.yaml/collaboration.profiles) — they don't change mid-run, so a slow poll
// is enough to pick up a restart with an edited config without needing a dedicated push channel.
const POLL_INTERVAL_MS = 15000

// useAssistantProfiles feeds the side-agent composer's @mention menu (T4.2) with the configured helper
// roles — GET /api/assistant-profiles, gated by the collaboration capability (independent of side_agent
// itself: a run can have collaboration without the side-agent dashboard chat, or vice versa). enabled=false
// → empty list, no network calls, mirroring useStageAssistants' posture.
export function useAssistantProfiles(enabled: boolean): AssistantProfile[] {
  const [profiles, setProfiles] = useState<AssistantProfile[]>([])

  useEffect(() => {
    if (!enabled) {
      setProfiles([])
      return
    }
    let cancelled = false
    const load = (): void => {
      listAssistantProfiles()
        .then((res) => {
          if (!cancelled) setProfiles(res.profiles)
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
  }, [enabled])

  return profiles
}
