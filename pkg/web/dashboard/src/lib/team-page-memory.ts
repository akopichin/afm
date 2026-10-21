import { useEffect, useState, type Dispatch, type SetStateAction } from 'react'
import type { ScrollMemory } from '../hooks/use-stick-to-bottom'

// Page-local Team state that must outlive the Team view itself: App renders Team only while it is the active
// view, so a switch to the Feed unmounts it. A page reload starts fresh, like the unread cursors in team-unread.ts.
const memoryByRun = new Map<string, Map<string, unknown>>()

function runMemory(runId: string): Map<string, unknown> {
  let memory = memoryByRun.get(runId)
  if (memory === undefined) {
    memory = new Map()
    memoryByRun.set(runId, memory)
  }
  return memory
}

// useTeamPageState — useState whose value survives a Team unmount within the same page and run.
export function useTeamPageState<T>(runId: string, key: string, initial: T): [T, Dispatch<SetStateAction<T>>] {
  const [value, setValue] = useState<T>(() => {
    const memory = runMemory(runId)
    return memory.has(key) ? (memory.get(key) as T) : initial
  })
  useEffect(() => {
    runMemory(runId).set(key, value)
  }, [runId, key, value])
  return [value, setValue]
}

// teamScrollMemory — where a conversation's log was left, so reopening Team returns the reader to it.
export function teamScrollMemory(runId: string, conversationKey: string): ScrollMemory {
  const key = `scroll:${conversationKey}`
  return {
    load: () => runMemory(runId).get(key) as ReturnType<ScrollMemory['load']>,
    save: (position) => {
      runMemory(runId).set(key, position)
    },
  }
}

// clearTeamPageMemory — forgets every run's Team state (tests start each case from a fresh page).
export function clearTeamPageMemory(): void {
  memoryByRun.clear()
}
