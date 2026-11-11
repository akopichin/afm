import { getVerifyReport } from '../../api/verify-report-client'

const MAX_REPORTS = 20
const MAX_REPORT_CHARS = 2_000_000

export type ReportCache = {
  // peek — the loaded text, or undefined while it is not loaded.
  peek: (stageId: string, verificationId: string) => string | undefined
  // load — one request per report, shared by every caller; rejects when the request fails (a failure is not kept).
  load: (stageId: string, verificationId: string) => Promise<string>
  // clear — forgets every report; answers of requests started before are dropped.
  clear: () => void
}

const cacheKey = (stageId: string, verificationId: string): string => `${stageId}|${verificationId}`

// createReportCache keeps the verify reports a reader opened, so a row that scrolls away and back does not fetch
// again. It is bounded: the oldest reports go first, and a report over the limit is handed to its caller only.
export function createReportCache(fetchReport: (stageId: string, verificationId: string) => Promise<string> = (s, v) => getVerifyReport(s, v)): ReportCache {
  let loaded = new Map<string, string>()
  let pending = new Map<string, Promise<string>>()
  let totalChars = 0

  function store(key: string, text: string): void {
    if (text.length > MAX_REPORT_CHARS) return
    loaded.set(key, text)
    totalChars += text.length
    for (const [oldest, oldestText] of loaded) {
      if (loaded.size <= MAX_REPORTS && totalChars <= MAX_REPORT_CHARS) break
      loaded.delete(oldest)
      totalChars -= oldestText.length
    }
  }

  return {
    peek: (stageId, verificationId) => loaded.get(cacheKey(stageId, verificationId)),
    load(stageId, verificationId) {
      const key = cacheKey(stageId, verificationId)
      const known = loaded.get(key)
      if (known !== undefined) return Promise.resolve(known)
      const inFlight = pending.get(key)
      if (inFlight !== undefined) return inFlight
      // A cleared cache is another scope: its answer reaches the callers but is not stored there.
      const generation = pending
      const request = fetchReport(stageId, verificationId)
        .then((text) => {
          if (pending === generation) store(key, text)
          return text
        })
        .finally(() => {
          if (pending === generation) pending.delete(key)
        })
      pending.set(key, request)
      return request
    },
    clear() {
      loaded = new Map()
      pending = new Map()
      totalChars = 0
    },
  }
}
