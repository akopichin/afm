import { useCallback, useEffect, useRef, useState } from 'react'
import { getMeshBoard, type MeshBoardEntry, type MeshBoardTask } from '../../api/mesh-client'
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
  // tasks — the typed board tasks; empty unless typedTasks (then the tasks are the "task/<slug>" entries).
  tasks: MeshBoardTask[]
  typedTasks: boolean
  status: MeshBoardStatus
  // reload — fetch now (after posting a task, or Retry after a failure), without waiting for the next poll.
  reload: () => Promise<void>
}

// BoardSnapshot — what the polls of one run learned; runId keeps one run's board out of another's.
type BoardSnapshot = { runId: string; entries: MeshBoardEntry[]; tasks: MeshBoardTask[]; typedTasks: boolean; loaded: boolean; failed: boolean }

function initialSnapshot(runId: string): BoardSnapshot {
  return { runId, entries: [], tasks: [], typedTasks: false, loaded: false, failed: false }
}

function statusOf({ loaded, failed }: BoardSnapshot): MeshBoardStatus {
  if (!failed) return loaded ? 'ready' : 'loading'
  return loaded ? 'stale' : 'error'
}

// useMeshBoard — the shared board (GET /api/mesh/board). Owned by App.tsx (ONE poll for the dashboard); the
// Team view's Board tab and the run-wide Board tab both read this value. It polls only while the mesh
// capability is `enabled`, so a run without the mesh never requests the board. A new runId starts from
// "loading": the old run's tasks are never shown, and a response of the old run's poll is discarded.
export function useMeshBoard(enabled = true, runId = ''): MeshBoard {
  const [stored, setSnapshot] = useState<BoardSnapshot>(() => initialSnapshot(runId))
  const snapshot = stored.runId === runId ? stored : initialSnapshot(runId)

  const pollRef = useRef<SerialPoll | null>(null)

  useEffect(() => {
    if (!enabled) return
    // Serial: reload() after a post and the interval share one queue, so an older read never lands last.
    const poll = startSerialPoll(
      (isStopped) =>
        getMeshBoard().then(
          (res) => {
            if (!isStopped()) setSnapshot({ runId, entries: res.entries, tasks: res.tasks, typedTasks: res.typedTasks, loaded: true, failed: false })
          },
          () => {
            // Keep the last snapshot; only flag that it may be out of date until a fetch succeeds again.
            if (!isStopped()) setSnapshot((prev) => ({ ...(prev.runId === runId ? prev : initialSnapshot(runId)), failed: true }))
          },
        ),
      POLL_INTERVAL_MS,
    )
    pollRef.current = poll
    return () => {
      poll.stop()
      pollRef.current = null
    }
  }, [enabled, runId])

  const reload = useCallback(() => pollRef.current?.now() ?? Promise.resolve(), [])

  return { entries: snapshot.entries, tasks: snapshot.tasks, typedTasks: snapshot.typedTasks, status: statusOf(snapshot), reload }
}
