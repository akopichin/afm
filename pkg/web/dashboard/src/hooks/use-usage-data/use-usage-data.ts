import { useEffect, useState } from 'react'
import type { UsageMetric, UsagePoint } from '../../types'

// Поллинг метрик потребления GET /api/usage. Соответствует loadUsage + живому апдейту
// usageRefreshTick в текущем app.js: загрузка при монтировании/смене параметров и
// освежение каждые 10000мс, пока хук смонтирован (т.е. пока открыта панель потребления).
const REFRESH_INTERVAL_MS = 10000

type RawPoint = {
  timeBucket?: unknown
  value?: unknown
}

export function useUsageData(metric: UsageMetric, stageFilter: string | null): UsagePoint[] {
  const [points, setPoints] = useState<UsagePoint[]>([])

  useEffect(() => {
    let cancelled = false

    async function load() {
      const stage = stageFilter ?? ''
      const url = `/api/usage?metric=${encodeURIComponent(metric)}&stage=${encodeURIComponent(stage)}`

      let response: Response
      try {
        response = await fetch(url)
      } catch {
        return
      }

      if (!response.ok) return

      // Единственная точка приведения типа для внешнего JSON.
      const data: unknown = await response.json()
      if (cancelled) return

      setPoints(toPoints(data, metric))
    }

    void load()

    const timer = setInterval(() => {
      void load()
    }, REFRESH_INTERVAL_MS)

    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [metric, stageFilter])

  return points
}

function toPoints(raw: unknown, metric: UsageMetric): UsagePoint[] {
  if (!Array.isArray(raw)) return []

  return raw
    .filter((item): item is RawPoint => item !== null && typeof item === 'object')
    .map((item) => ({
      timestamp: typeof item.timeBucket === 'string' ? item.timeBucket : '',
      metric,
      value: typeof item.value === 'number' ? item.value : 0,
    }))
}
