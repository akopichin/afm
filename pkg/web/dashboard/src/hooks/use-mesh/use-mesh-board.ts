import { useCallback, useEffect, useRef, useState } from 'react'
import { getMeshBoard, type MeshBoardEntry } from '../../api/mesh-client'
import { startSerialPoll, type SerialPoll } from '../../lib/serial-poll'

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

  const pollRef = useRef<SerialPoll | null>(null)

  useEffect(() => {
    // Serial: reload() after a post and the interval share one queue, so an older read never lands last.
    const poll = startSerialPoll(
      (isStopped) =>
        getMeshBoard().then(
          (res) => {
            if (!isStopped()) setEntries(res.entries ?? [])
          },
          () => {
            /* transient — keep whatever we had, same posture as the other mesh polls */
          },
        ),
      POLL_INTERVAL_MS,
    )
    pollRef.current = poll
    return () => {
      poll.stop()
      pollRef.current = null
    }
  }, [])

  const reload = useCallback(() => pollRef.current?.now() ?? Promise.resolve(), [])

  return { entries, reload }
}
