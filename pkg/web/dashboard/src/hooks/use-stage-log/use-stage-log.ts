import { useEffect, useState } from 'react'
import type { LogEntry } from '../../types'

// Поллинг операционного лога выбранной стадии. Соответствует loadLog в текущем app.js:
// отдельный источник (этот эндпоинт), а не фильтрация WebSocket-событий; интервал 3000мс.
const POLL_INTERVAL_MS = 3000

// Формат строки лога сервера: «HH:MM:SS  TYPE  detail». Оставляем только text-строки
// (tool calls и баннеры уходят в ленту событий), как renderLog в текущем app.js.
const TEXT_LINE_PATTERN = /^(\d{2}:\d{2}:\d{2})\s+text\s+(.*)$/

export function useStageLog(stageId: string | null): LogEntry[] {
  const [entries, setEntries] = useState<LogEntry[]>([])

  useEffect(() => {
    if (stageId === null) {
      setEntries([])
      return
    }

    // Локальная константа фиксирует ненулевое значение для вложенной функции load()
    // — TypeScript не сужает string | null через границу замыкания.
    const id = stageId
    let cancelled = false

    // Сбрасываем сразу при смене стадии: без этого до первого успешного fetch
    // (или на 404 у ещё не начавшей писать лог стадии) в панели оставались
    // записи предыдущей выбранной стадии — реальный баг, замеченный на живом
    // флоу (ретраенные стадии показывали лог активной brainstorm-стадии).
    setEntries([])

    async function load() {
      let response: Response
      try {
        response = await fetch(`/api/stages/${encodeURIComponent(id)}/log`)
      } catch {
        return
      }

      if (!response.ok) {
        if (!cancelled) setEntries([])
        return
      }

      // Лог — текстовый эндпоинт, не JSON.
      const text = await response.text()
      if (cancelled) return

      setEntries(parseLog(text))
    }

    void load()

    const timer = setInterval(() => {
      void load()
    }, POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [stageId])

  return entries
}

function parseLog(text: string): LogEntry[] {
  return text
    .split('\n')
    .map((line) => TEXT_LINE_PATTERN.exec(line))
    .filter((match): match is RegExpExecArray => match !== null)
    .map((match) => ({
      timestamp: match[1] ?? '',
      message: match[2] ?? '',
      level: 'info' as const,
    }))
}
