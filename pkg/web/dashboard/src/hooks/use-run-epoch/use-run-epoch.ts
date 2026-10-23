import { useState } from 'react'

// useRunEpoch считает смены рана в открытой вкладке: растёт, когда непустой run_id заменяется другим непустым
// (авто-повтор флоу стартует новый ран на том же порту). Первый run_id ('' → A) — это загрузка страницы, а не
// смена: epoch остаётся прежним, и уже накопленные данные не сбрасываются. Состояние ведётся во время
// рендера: React сразу перерисует компонент, поэтому кадр с данными прошлого рана не показывается.
export function useRunEpoch(runId: string): number {
  const [state, setState] = useState({ runId, epoch: 0 })
  if (runId !== '' && runId !== state.runId) {
    const changed = state.runId !== ''
    setState({ runId, epoch: changed ? state.epoch + 1 : state.epoch })
  }
  return state.epoch
}
