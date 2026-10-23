import { useEffect, useMemo, useRef, useState } from 'react'
import type { AfmEvent } from '../../types'
import { mergeCapped, toEvent } from '../use-event-feed'
import { useRunEpoch } from '../use-run-epoch'

// Кап отдельный от глобальной ленты — вкладка Feed одной стадии не обязана видеть больше 2000: этого
// достаточно, чтобы показать полный ход стадии, не раздувая память на очень долгих ранах.
const MAX_STAGE_EVENTS = 2000

// useStageEvents — источник ленты для вкладки Feed: последние ≤2000 событий ОДНОЙ стадии, независимо от
// глобального окна в 10000.
// История привязана к (stage, runEpoch): у каждого рана свой seq, а stage ID при авто-повторе тот же.
type StageHistory = { stageId: string; runEpoch: number; events: AfmEvent[] }

const EMPTY_HISTORY: StageHistory = { stageId: '', runEpoch: 0, events: [] }

export function useStageEvents(stageId: string | null, globalEvents: AfmEvent[], runId: string): AfmEvent[] {
  const runEpoch = useRunEpoch(runId)
  const [history, setHistory] = useState<StageHistory>(EMPTY_HISTORY)
  const gen = useRef(0)

  useEffect(() => {
    // Всегда увеличиваем gen, включая stageId=null, чтобы старый fetch не записал историю после смены
    // выбора стадии или рана.
    const myGen = ++gen.current

    if (stageId === null) {
      setHistory(EMPTY_HISTORY)
      return
    }

    fetch(`/api/events?stage=${encodeURIComponent(stageId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((raw: unknown) => {
        if (gen.current !== myGen || !Array.isArray(raw)) return
        // Фильтруем историю по stageId даже при ответе старого сервера, проигнорировавшего ?stage.
        setHistory({ stageId, runEpoch, events: raw.map(toEvent).filter((e) => e.stageId === stageId) })
      })
      .catch(() => {
        // Сеть/старый сервер — деградируем к чистому live-хвосту (см. ниже).
      })
  }, [stageId, runEpoch])

  return useMemo(() => {
    if (stageId === null) return []
    // history может ещё принадлежать ПРЕДЫДУЩЕЙ стадии или рану (см. комментарий выше
    // StageHistory) — считаем её пустой, пока фетч новой не резолвится.
    const hist = history.stageId === stageId && history.runEpoch === runEpoch ? history.events : []
    const live = globalEvents.filter((e) => e.stageId === stageId)
    return mergeCapped(hist, live, MAX_STAGE_EVENTS)
  }, [stageId, runEpoch, history, globalEvents])
}
