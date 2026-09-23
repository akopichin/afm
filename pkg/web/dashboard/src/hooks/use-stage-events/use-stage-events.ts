import { useEffect, useMemo, useRef, useState } from 'react'
import type { AfmEvent } from '../../types'
import { mergeCapped, toEvent } from '../use-event-feed'

// Кап отдельный от глобальной ленты (MAX_EVENTS=1000 в use-event-feed) — вкладка
// Feed одной стадии не обязана видеть больше 200: этого достаточно, чтобы
// показать полный ход стадии, не раздувая память на очень долгих ранах.
const MAX_STAGE_EVENTS = 200

// useStageEvents — источник ленты для вкладки Feed: последние ≤200 событий ОДНОЙ
// стадии, независимо от глобального окна в 1000. История тянется один раз на
// смену стадии через /api/events?stage=<id> (там она уже ≤200 и отсортирована),
// а живой хвост берётся из globalEvents, отфильтрованных по стадии, и мёржится с
// историей через mergeCapped (тот же дедуп-ключ, что и в use-event-feed). Гонка
// фетча закрыта generation-ref: ответ для ПРЕДЫДУЩЕЙ стадии (включая переключение
// в null) не должен «приехать» в текущую.
export function useStageEvents(stageId: string | null, globalEvents: AfmEvent[]): AfmEvent[] {
  const [history, setHistory] = useState<AfmEvent[]>([])
  const gen = useRef(0)

  useEffect(() => {
    // gen бампается ВСЕГДА, включая ветку stageId===null — иначе фетч,
    // запущенный для предыдущей стадии, мог бы резолвиться уже после
    // переключения на null и всё равно записать историю в setHistory
    // (codex round 2: null — тоже смена «текущей» стадии, а не отсутствие смены).
    const myGen = ++gen.current

    if (stageId === null) {
      setHistory([])
      return
    }

    setHistory([]) // не показываем историю прошлой стадии, пока грузим новую
    fetch(`/api/events?stage=${encodeURIComponent(stageId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((raw: unknown) => {
        if (gen.current !== myGen || !Array.isArray(raw)) return
        setHistory(raw.map(toEvent))
      })
      .catch(() => {
        // Сеть/старый сервер — деградируем к чистому live-хвосту (см. ниже).
      })
  }, [stageId])

  return useMemo(() => {
    if (stageId === null) return []
    const live = globalEvents.filter((e) => e.stageId === stageId)
    return mergeCapped(history, live, MAX_STAGE_EVENTS)
  }, [stageId, history, globalEvents])
}
