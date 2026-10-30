import { createContext, useCallback, useContext, useState, useSyncExternalStore } from 'react'
import type { Stage } from '../../types'

type StageActionEntry = { episode: string; error: string | null }
type StageActions = { sent: Map<string, StageActionEntry>; listeners: Set<() => void> }
type StageRequest = (stageId: string) => Promise<void>

export function createStageActions(): StageActions {
  return { sent: new Map(), listeners: new Set() }
}

export const StageActionsContext = createContext<StageActions | null>(null)

function episodeOf(stage: Stage): string {
  return `${stage.status}@${stage.updatedAt}`
}

function publish(store: StageActions, id: string, entry: StageActionEntry): void {
  store.sent.set(id, entry)
  store.listeners.forEach((notify) => notify())
}

// A successful command stays locked until polling observes another episode, even across navigation.
export async function sendStageAction(store: StageActions, stage: Stage, request: StageRequest): Promise<void> {
  const episode = episodeOf(stage)
  const last = store.sent.get(stage.id)
  if (last?.episode === episode && last.error === null) return
  const mine = { episode, error: null }
  publish(store, stage.id, mine)
  try {
    await request(stage.id)
  } catch (err) {
    if (store.sent.get(stage.id) === mine) {
      publish(store, stage.id, { episode, error: err instanceof Error ? err.message : String(err) })
    }
  }
}

export function useStageAction(stage: Stage | null): { send: (request: StageRequest) => Promise<void>; locked: boolean; error: string | null } {
  const [local] = useState(createStageActions)
  const store = useContext(StageActionsContext) ?? local
  const id = stage?.id
  const subscribe = useCallback((notify: () => void) => {
    store.listeners.add(notify)
    return () => { store.listeners.delete(notify) }
  }, [store])
  const snapshot = useCallback(() => id === undefined ? undefined : store.sent.get(id), [store, id])
  const sent = useSyncExternalStore(subscribe, snapshot)
  const current = stage !== null && sent?.episode === episodeOf(stage) ? sent : undefined

  async function send(request: StageRequest): Promise<void> {
    if (stage === null) return
    await sendStageAction(store, stage, request)
  }

  return { send, locked: current !== undefined && current.error === null, error: current?.error ?? null }
}
