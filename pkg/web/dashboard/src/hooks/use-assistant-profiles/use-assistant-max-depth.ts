import { useEffect, useState } from 'react'
import { listAssistantProfiles } from '../../api/run-client'

// Same slow poll as useAssistantProfiles — max_depth is resolved once per run (collaboration.max_depth is
// not hot-reloaded, dev-plan.md §3.3), a restart is the only way it ever changes.
const POLL_INTERVAL_MS = 15000

// useAssistantMaxDepth feeds AssistantBreadcrumb's "depth N of max_depth" (T4a.8) — a SEPARATE, minimal hook
// rather than extending useAssistantProfiles' own return shape: that hook's contract (a plain
// AssistantProfile[]) is already depended on by the side-agent @mention menu and its own test suite: giving
// it a second, unrelated responsibility would mean touching code and tests for a different feature to land
// this one. Both hooks hit the SAME GET /api/assistant-profiles endpoint independently — a 15s poll of a
// small, cached response is not worth sharing a cache over.
export function useAssistantMaxDepth(enabled: boolean): number | null {
  const [maxDepth, setMaxDepth] = useState<number | null>(null)

  useEffect(() => {
    if (!enabled) {
      setMaxDepth(null)
      return
    }
    let cancelled = false
    const load = (): void => {
      listAssistantProfiles()
        .then((res) => {
          if (!cancelled) setMaxDepth(res.maxDepth)
        })
        .catch(() => {
          /* transient error — keep whatever we had, same posture as useAssistantProfiles */
        })
    }
    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [enabled])

  return maxDepth
}
