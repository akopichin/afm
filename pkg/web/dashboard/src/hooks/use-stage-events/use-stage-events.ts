import { useEffect, useMemo, useRef, useState } from 'react'
import type { AfmEvent } from '../../types'
import { mergeCapped, toEvent } from '../use-event-feed'
import { useRunEpoch } from '../use-run-epoch'
import { isRunBudgetNotice } from '../../types/budget'

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
        // Фильтруем историю по stageId даже при ответе старого сервера, проигнорировавшего ?stage. Обрезаем до
        // конвертации: toEvent и ключи нужны только удерживаемому хвосту.
        const events = raw
          .filter((item) => isFromStage(item, stageId))
          .slice(-MAX_STAGE_EVENTS)
          .map(toEvent)
        setHistory({ stageId, runEpoch, events })
      })
      .catch(() => {
        // Сеть/старый сервер — деградируем к чистому live-хвосту (см. ниже).
      })
  }, [stageId, runEpoch])

  // События других стадий не должны пересчитывать ленту выбранной: пока её срез глобального окна не
  // изменился, остаётся прежний массив.
  const liveRef = useRef<AfmEvent[]>([])
  const live = useMemo(() => {
    // Notices рана не принадлежат стадии, а общей ленты нет: каждая стадия показывает их у себя.
    const next = globalEvents.filter((e) => e.stageId === stageId || isRunBudgetNotice(e)).slice(-MAX_STAGE_EVENTS)
    if (isSameEvents(liveRef.current, next)) return liveRef.current
    liveRef.current = next
    return next
  }, [stageId, globalEvents])

  return useMemo(() => {
    if (stageId === null) return []
    // history может ещё принадлежать ПРЕДЫДУЩЕЙ стадии или рану (см. комментарий выше
    // StageHistory) — считаем её пустой, пока фетч новой не резолвится.
    const hist = history.stageId === stageId && history.runEpoch === runEpoch ? history : EMPTY_HISTORY
    return mergeCapped(hist.events, live, MAX_STAGE_EVENTS)
  }, [stageId, runEpoch, history, live])
}

function isFromStage(item: unknown, stageId: string): boolean {
  return typeof item === 'object' && item !== null && (item as { stage_id?: unknown }).stage_id === stageId
}

function isSameEvents(a: AfmEvent[], b: AfmEvent[]): boolean {
  return a.length === b.length && a.every((e, i) => e === b[i])
}
