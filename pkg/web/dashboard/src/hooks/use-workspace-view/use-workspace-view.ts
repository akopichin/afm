import { useCallback, useLayoutEffect, useReducer } from 'react'
import type { Stage } from '../../types'
import {
  workspaceReducer,
  initialWorkspaceState,
  deriveAttentionItems,
  activeItem,
  sig,
  type WorkspaceState,
  type WorkspaceView,
} from './workspace-view'

// useWorkspaceView — тонкая обёртка над чистым workspaceReducer.
export interface UseWorkspaceView {
  state: WorkspaceState
  activeItem: ReturnType<typeof activeItem>
  openFeed: () => void
  openCost: () => void
  openFullFeed: () => void
  openAttention: (stageId?: string) => void
  openHistory: (view: 'plan-history' | 'dialog-history') => void
  openAssistant: () => void
  openTeam: () => void
}

export function useWorkspaceView(
  stages: Stage[],
  suppressed: boolean,
  ownedHandledSigs: string[] = [],
): UseWorkspaceView {
  const [state, dispatch] = useReducer(workspaceReducer, initialWorkspaceState)

  const items = deriveAttentionItems(stages)
  // Синхронизируем на каждое изменение состава/порядка очереди, сигнала suppression или набора
  // owned-paused подписей.
  const itemsKey = items.map(sig).join('|')
  const ownedKey = ownedHandledSigs.join('|')
  useLayoutEffect(() => {
    dispatch({ type: 'sync', items, suppressed, ownedHandledSigs })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemsKey, suppressed, ownedKey])

  const openFeed = useCallback(() => dispatch({ type: 'openFeed' }), [])
  const openCost = useCallback(() => dispatch({ type: 'openCost' }), [])
  const openFullFeed = useCallback(() => dispatch({ type: 'openFullFeed' }), [])
  const openAttention = useCallback((stageId?: string) => dispatch({ type: 'openAttention', stageId }), [])
  const openHistory = useCallback(
    (view: 'plan-history' | 'dialog-history') => dispatch({ type: 'openHistory', view }),
    [],
  )
  const openAssistant = useCallback(() => dispatch({ type: 'openAssistant' }), [])
  const openTeam = useCallback(() => dispatch({ type: 'openTeam' }), [])

  return { state, activeItem: activeItem(state), openFeed, openCost, openFullFeed, openAttention, openHistory, openAssistant, openTeam }
}

export type { WorkspaceState, WorkspaceView }
