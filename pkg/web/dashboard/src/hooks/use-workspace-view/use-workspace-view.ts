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
  openCost: () => void
  openFullFeed: () => void
  openAttention: (stageId?: string) => void
  openHistory: (view: 'plan-history' | 'dialog-history') => void
}

export function useWorkspaceView(
  stages: Stage[],
  suppressed: boolean,
  ownedHandledSigs: string[] = [],
): UseWorkspaceView {
  const [state, dispatch] = useReducer(workspaceReducer, initialWorkspaceState)

  const items = deriveAttentionItems(stages)
  // Синхронизируем на каждое изменение состава/порядка очереди, сигнала
  // suppression или набора owned-paused подписей. Сериализованные ключи держат
  // эффект идемпотентным: тот же снимок не диспатчит sync повторно каждый рендер.
  // useLayoutEffect (а не useEffect): reducer применяет свежие items ДО paint и
  // пользовательского клика — иначе между commit нового snapshot и passive-эффектом
  // openAttention мог бы не найти только что прибывший элемент и потерять клик.
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

  return { state, activeItem: activeItem(state), openFeed, openCost, openFullFeed, openAttention, openHistory }
}

export type { WorkspaceState, WorkspaceView }
