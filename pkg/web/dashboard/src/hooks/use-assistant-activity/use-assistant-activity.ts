import { useEffect, useMemo, useRef, useState } from 'react'
import type { AssistantActivityEntry } from '../../types/assistant-activity'
import { useRunEpoch } from '../use-run-epoch'
import { initialData, startActivityRunner, type ActivityControls, type ActivityData, type ActivityStatus } from './activity-runner'
import { MAX_ACTIVITY_ENTRIES, mergeActivity, type OrderMemory } from './activity-window'

export { MAX_ACTIVITY_ENTRIES, mergeActivity }
export type { ActivityStatus }

// The socket may drop messages while it stays open, so completeness comes from this poll; the socket only
// lowers latency.
const POLL_INTERVAL_MS = 3000

export type AssistantActivity = {
  // Saved rows plus the live entries not saved yet; always ends at the live end.
  entries: AssistantActivityEntry[]
  status: ActivityStatus
  // The last read failed but the entries on screen are kept: loaded ones, or only live ones when the first
  // load failed (then `status` is 'error' too).
  refreshFailing: boolean
  hasOlder: boolean
  loadingOlder: boolean
  // Older history exists but the panel already holds as many rows as it keeps: Load older has nothing to add.
  olderCapped: boolean
  loadOlder: () => void
  // Repeats the load now; for a finished task whose last read failed, which polling no longer retries.
  retry: () => void
}

// useAssistantActivity — saved + live activity of ONE helper task. State and late responses are keyed by
// (runEpoch, taskId); `live` is the run-wide ring from useEventFeed, `connected` its socket state.
// terminal tells the task finished: one last catch-up runs, then polling stops.
export function useAssistantActivity(
  taskId: string | null,
  runId: string,
  live: AssistantActivityEntry[],
  connected: boolean,
  terminal: boolean,
): AssistantActivity {
  const runEpoch = useRunEpoch(runId)
  const key = taskId === null ? '' : `${runEpoch}:${taskId}`
  const [data, setData] = useState<ActivityData>(() => initialData(key))
  const controls = useRef<ActivityControls | null>(null)
  // Order knowledge per (run, task) survives switching to another helper and back.
  const orderMemory = useRef(new Map<string, OrderMemory>())
  // The runner reads the ring when it sends a request or trims the window, not when it was started.
  const liveRef = useRef(live)
  useEffect(() => {
    liveRef.current = live
  }, [live])

  useEffect(() => {
    if (taskId === null) {
      controls.current = null
      return
    }
    const ofTask = () => liveRef.current.filter((e) => e.taskId === taskId).map((e) => e.eventId)
    const remembered = orderMemory.current
    const runner = startActivityRunner(taskId, key, setData, ofTask, remembered.get(key))
    controls.current = runner.controls
    return () => {
      remembered.set(key, runner.stop())
      controls.current = null
    }
  }, [taskId, key])

  // Polling lives only while the task can still produce actions.
  useEffect(() => {
    if (taskId === null || terminal) return
    const timer = setInterval(() => controls.current?.poll(), POLL_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [taskId, key, terminal])

  // Catch up right after a reconnect and once after the task turned terminal.
  const previousConnected = useRef(connected)
  const previousTerminal = useRef(terminal)
  useEffect(() => {
    const reconnected = connected && !previousConnected.current
    const justFinished = terminal && !previousTerminal.current
    previousConnected.current = connected
    previousTerminal.current = terminal
    if (reconnected || justFinished) controls.current?.sync()
  }, [connected, terminal])

  const current = data.key === key ? data : initialData(key)
  const { rows, hasOlder, unplaced, seen } = current.window
  const entries = useMemo(
    () => (taskId === null ? rows : mergeActivity(rows, live.filter((e) => e.taskId === taskId), { atStart: !hasOlder, unplaced, seen })),
    [taskId, rows, hasOlder, unplaced, seen, live],
  )

  return {
    entries,
    status: current.status,
    refreshFailing: current.refreshFailing || (current.status === 'error' && entries.length > 0),
    hasOlder,
    loadingOlder: current.loadingOlder,
    olderCapped: hasOlder && rows.length >= MAX_ACTIVITY_ENTRIES,
    loadOlder: () => controls.current?.loadOlder(),
    retry: () => controls.current?.sync(),
  }
}
