import { useCallback, useEffect, useRef, useState } from 'react'
import { getMeshBoard, type MeshBoardEntry } from '../../api/mesh-client'
import { startSerialPoll, type SerialPoll } from '../../lib/serial-poll'

const POLL_INTERVAL_MS = 5000

// MeshBoardStatus — what the board data can be trusted for:
//   loading — no fetch has landed yet (not "zero tasks");
//   ready   — the last fetch succeeded, entries are current (an empty list is a real empty board);
//   error   — the first fetch failed, there is no snapshot to show;
//   stale   — a later fetch failed, entries are the last successful snapshot.
export type MeshBoardStatus = 'loading' | 'ready' | 'error' | 'stale'

export type MeshBoard = {
  entries: MeshBoardEntry[]
  status: MeshBoardStatus
  // reload — fetch now (after posting a task, or Retry after a failure), without waiting for the next poll.
  reload: () => Promise<void>
}

type BoardSnapshot = { entries: MeshBoardEntry[]; loaded: boolean; failed: boolean }

const INITIAL: BoardSnapshot = { entries: [], loaded: false, failed: false }

function statusOf({ loaded, failed }: BoardSnapshot): MeshBoardStatus {
  if (!failed) return loaded ? 'ready' : 'loading'
  return loaded ? 'stale' : 'error'
}

// useMeshBoard — the shared board (GET /api/mesh/board). Owned by App.tsx (ONE poll for the dashboard); the
// Team view's Board tab and the run-wide Board tab both read this value. It polls only while the mesh
// capability is `enabled`, so a run without the mesh never requests the board.
export function useMeshBoard(enabled = true): MeshBoard {
  const [snapshot, setSnapshot] = useState<BoardSnapshot>(INITIAL)

  const pollRef = useRef<SerialPoll | null>(null)

  useEffect(() => {
    if (!enabled) return
    // Serial: reload() after a post and the interval share one queue, so an older read never lands last.
    const poll = startSerialPoll(
      (isStopped) =>
        getMeshBoard().then(
          (res) => {
            if (!isStopped()) setSnapshot({ entries: res.entries ?? [], loaded: true, failed: false })
          },
          () => {
            // Keep the last snapshot; only flag that it may be out of date until a fetch succeeds again.
            if (!isStopped()) setSnapshot((prev) => ({ ...prev, failed: true }))
          },
        ),
      POLL_INTERVAL_MS,
    )
    pollRef.current = poll
    return () => {
      poll.stop()
      pollRef.current = null
    }
  }, [enabled])

  const reload = useCallback(() => pollRef.current?.now() ?? Promise.resolve(), [])

  return { entries: snapshot.entries, status: statusOf(snapshot), reload }
}
