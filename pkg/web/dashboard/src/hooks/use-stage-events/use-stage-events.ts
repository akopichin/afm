import { useEffect, useMemo, useRef, useState } from 'react'
import type { AfmEvent } from '../../types'
import { mergeCapped, toEvent } from '../use-event-feed'

// Кап отдельный от глобальной ленты — вкладка Feed одной стадии не обязана видеть больше 2000: этого
// достаточно, чтобы показать полный ход стадии, не раздувая память на очень долгих ранах.
const MAX_STAGE_EVENTS = 2000

// useStageEvents — источник ленты для вкладки Feed: последние ≤2000 событий ОДНОЙ стадии, независимо от
// глобального окна в 10000.
type StageHistory = { stageId: string; events: AfmEvent[] }

const EMPTY_HISTORY: StageHistory = { stageId: '', events: [] }

export function useStageEvents(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[] {
  const [history, setHistory] = useState<StageHistory>(EMPTY_HISTORY)
  const gen = useRef(0)

  useEffect(() => {
    // Всегда увеличиваем gen, включая stageId=null, чтобы старый fetch не записал историю после смены
    // выбора.
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
        setHistory({ stageId, events: raw.map(toEvent).filter((e) => e.stageId === stageId) })
      })
      .catch(() => {
        // Сеть/старый сервер — деградируем к чистому live-хвосту (см. ниже).
      })
  }, [stageId])

  return useMemo(() => {
    if (stageId === null) return []
    // history может ещё принадлежать ПРЕДЫДУЩЕЙ стадии (см. комментарий выше
    // StageHistory) — считаем её пустой, пока фетч новой стадии не резолвится.
    const hist = history.stageId === stageId ? history.events : []
    const live = globalEvents.filter((e) => e.stageId === stageId)
    return mergeCapped(hist, live, MAX_STAGE_EVENTS)
  }, [stageId, history, globalEvents])
}
