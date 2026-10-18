import { useCallback, useEffect, useState } from 'react'
import { getMeshBoard, type MeshBoardEntry } from '../../api/mesh-client'

const POLL_INTERVAL_MS = 5000

export type MeshBoard = {
  entries: MeshBoardEntry[]
  // reload — fetch now (after posting a task), without waiting for the next poll.
  reload: () => Promise<void>
}

// useMeshBoard — the shared board (GET /api/mesh/board), polled while the Team view is open: the Board tab
// shows it and the tab label counts its tasks.
export function useMeshBoard(): MeshBoard {
  const [entries, setEntries] = useState<MeshBoardEntry[]>([])

  const reload = useCallback(
    () =>
      getMeshBoard()
        .then((res) => setEntries(res.entries ?? []))
        .catch(() => {
          /* transient — keep whatever we had, same posture as the other mesh polls */
        }),
    [],
  )

  useEffect(() => {
    let cancelled = false
    const tick = (): void => {
      getMeshBoard()
        .then((res) => {
          if (!cancelled) setEntries(res.entries ?? [])
        })
        .catch(() => {})
    }
    tick()
    const timer = setInterval(tick, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  return { entries, reload }
}
