import { useCallback, useEffect, useReducer } from 'react'
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

// useWorkspaceView — тонкая обёртка над чистым workspaceReducer (вся логика
// переходов и тесты — в workspace-view.ts). Синхронизирует очередь attention из
// текущего снимка стадий на каждый ререндер и отдаёт наверх и состояние, и
// действия навигации. `suppressed` — внешний сигнал (пользователь печатает /
// смотрит файл в оверлее), при котором прибывший элемент только светится, а
// фокус не крадётся (rule 9); вычисление этого сигнала — забота вызывающего
// (App.tsx), не воркспейса.
export interface UseWorkspaceView {
  state: WorkspaceState
  activeItem: ReturnType<typeof activeItem>
  openFeed: () => void
  openAttention: (stageId?: string) => void
  openHistory: (view: 'plan-history' | 'dialog-history') => void
}

export function useWorkspaceView(stages: Stage[], suppressed: boolean): UseWorkspaceView {
  const [state, dispatch] = useReducer(workspaceReducer, initialWorkspaceState)

  const items = deriveAttentionItems(stages)
  // Синхронизируем на каждое изменение состава/порядка очереди или сигнала
  // suppression. Сериализованный ключ items держит эффект идемпотентным: тот же
  // снимок не диспатчит sync повторно каждый рендер.
  const itemsKey = items.map(sig).join('|')
  useEffect(() => {
    dispatch({ type: 'sync', items, suppressed })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemsKey, suppressed])

  const openFeed = useCallback(() => dispatch({ type: 'openFeed' }), [])
  const openAttention = useCallback((stageId?: string) => dispatch({ type: 'openAttention', stageId }), [])
  const openHistory = useCallback(
    (view: 'plan-history' | 'dialog-history') => dispatch({ type: 'openHistory', view }),
    [],
  )

  return { state, activeItem: activeItem(state), openFeed, openAttention, openHistory }
}

export type { WorkspaceState, WorkspaceView }
