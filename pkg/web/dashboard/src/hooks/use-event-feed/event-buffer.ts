import { SIGNIFICANT_EVENT_TYPES, type AfmEvent } from '../../types'

export const MAX_EVENTS = 10000

// Типы, для которых приём дедупит по feedDedupeKey. agent_action/script_output намеренно исключены —
// легитимные одинаковые повторы не должны схлопываться.
const CONTENT_DEDUPE_ON_INGEST = new Set(['agent_note', 'script_failed'])

// dedupeKey — стабильный ключ дедупликации между историей из /api/events и live-событиями WebSocket.
function dedupeKey(e: AfmEvent): string {
  if (e.seq !== undefined) return `seq:${e.seq}|${e.type}`
  return `${e.type}|${e.stageId}|${JSON.stringify(e.payload)}`
}

// isDialogContentEvent — событие принадлежит «семейству диалога»: dialog_question, dialog_answer, а также
// auto_answered ТОЛЬКО когда это настоящий авто-ответ на вопрос.
function isDialogContentEvent(e: AfmEvent): boolean {
  if (e.type === 'dialog_question' || e.type === 'dialog_answer') return true
  if (e.type !== 'auto_answered') return false
  const p = e.payload
  return typeof p === 'object' && p !== null && (p as { dialog?: unknown }).dialog === true
}

// dialogContentKey зеркалит серверный ключ из именованных полей; текст различает повторно использованные
// phase/id, порядок JSON-ключей не важен.
function dialogContentKey(e: AfmEvent): string {
  const p = e.payload
  const obj = typeof p === 'object' && p !== null ? (p as Record<string, unknown>) : {}
  const field = (v: unknown): string => (typeof v === 'string' ? v : v === undefined || v === null ? '' : String(v))
  return `${e.type}|${e.stageId}|${field(obj.phase)}|${field(obj.id)}|${field(obj.title)}|${field(obj.question_title)}|${field(obj.answer)}`
}

// Ключ считается один раз на объект события: JSON.stringify тысяч payload на каждое слияние дорог.
const feedKeyCache = new WeakMap<AfmEvent, string>()

// feedDedupeKey — единый дедуп-ключ ленты для приёма и слияния истории: семейство диалога → контент-ключ из
// именованных полей, всё остальное → dedupeKey.
function feedDedupeKey(e: AfmEvent): string {
  let key = feedKeyCache.get(e)
  if (key === undefined) {
    key = isDialogContentEvent(e) ? dialogContentKey(e) : dedupeKey(e)
    feedKeyCache.set(e, key)
  }
  return key
}

// ingestKey — ключ событий, которые дедупятся при приёме; для остальных null.
function ingestKey(e: AfmEvent): string | null {
  return isDialogContentEvent(e) || CONTENT_DEDUPE_ON_INGEST.has(e.type) ? feedDedupeKey(e) : null
}

function statusOf(event: AfmEvent): string {
  const data = event.payload
  if (typeof data === 'string') return data
  if (typeof data === 'object' && data !== null && typeof (data as { status?: unknown }).status === 'string') {
    return (data as { status: string }).status
  }
  return ''
}

// Два события — один и тот же переход статуса стадии (та же стадия, тот же целевой статус).
function isSameStatusEvent(a: AfmEvent, b: AfmEvent): boolean {
  return (
    a.type === 'stage_status_changed' &&
    b.type === 'stage_status_changed' &&
    a.stageId === b.stageId &&
    statusOf(a) === statusOf(b)
  )
}

// reliableAnchors — индексы live-событий из наибольшей строго возрастающей (по позиции в истории) цепочки
// якорей. Остальные покрытые события — выбросы: сервер сортирует по времени, и seq-less событие может стоять
// в двух снимках совсем по-разному.
function reliableAnchors(positions: (number | undefined)[]): Set<number> {
  const tails: number[] = [] // tails[k] — live-индекс якоря, заканчивающего лучшую цепочку длины k+1
  const previous = new Map<number, number>()
  positions.forEach((position, i) => {
    if (position === undefined) return
    let low = 0
    let high = tails.length
    while (low < high) {
      const mid = (low + high) >> 1
      if ((positions[tails[mid] as number] as number) < position) low = mid + 1
      else high = mid
    }
    if (low > 0) previous.set(i, tails[low - 1] as number)
    tails[low] = i
  })

  const reliable = new Set<number>()
  let i = tails[tails.length - 1]
  while (i !== undefined) {
    reliable.add(i)
    i = previous.get(i)
  }
  return reliable
}

// Статусы стоят в истории по времени, а не по потоку, поэтому хвост считается по последнему не-статусному событию.
function lastActionIndex(history: AfmEvent[]): number {
  for (let i = history.length - 1; i >= 0; i--) {
    if (history[i]?.type !== 'stage_status_changed') return i
  }
  return history.length - 1
}

// mergeCapped сливает историю с live по надёжным якорям (не-статусные live-события, покрытые историей):
// серия live-only встаёт сразу после позиции предыдущего надёжного якоря, без него — перед первым; серия после
// последнего якоря (или без якорей вовсе) — в хвост, после восстановленного суффикса истории.
export function mergeCapped(history: AfmEvent[], live: AfmEvent[], cap: number): AfmEvent[] {
  const historyIndex = new Map<string, number>()
  history.forEach((e, i) => historyIndex.set(feedDedupeKey(e), i))
  const positions = live.map((e) => historyIndex.get(feedDedupeKey(e)))
  // Сервер ставит stage_status_changed по времени, а не по порядку потока: статус никогда не якорь.
  const anchorPositions = live.map((e, i) => (e.type === 'stage_status_changed' ? undefined : positions[i]))
  const reliable = reliableAnchors(anchorPositions)

  // Серии, вставляемые после позиции истории (-1 — в самое начало).
  const runsAfter = new Map<number, AfmEvent[]>()
  const insertAfter = (position: number, run: AfmEvent[]) =>
    runsAfter.set(position, [...(runsAfter.get(position) ?? []), ...run])

  let previousPosition: number | undefined
  let run: AfmEvent[] = []
  live.forEach((e, i) => {
    const position = positions[i]
    if (position === undefined) {
      run.push(e)
      return
    }
    // Покрытое, но ненадёжное событие уже есть в истории: оно не добавляется в серию и не разделяет её.
    if (!reliable.has(i)) return
    if (run.length > 0) insertAfter(previousPosition ?? position - 1, run)
    run = []
    previousPosition = position
  })
  if (run.length > 0) insertAfter(lastActionIndex(history), run)

  const merged: AfmEvent[] = [...(runsAfter.get(-1) ?? [])]
  history.forEach((e, i) => {
    merged.push(e, ...(runsAfter.get(i) ?? []))
  })
  return merged.slice(-cap)
}

// ArrivalCounts — сколько событий принято вживую за ран: по стадиям и отдельно mesh_*. Счётчики только растут:
// вытеснение с капа и слияние истории их не меняют, поэтому по ним считают «новое с момента X».
export type ArrivalCounts = { stages: ReadonlyMap<string, number>; mesh: number }

export const NO_ARRIVALS: ArrivalCounts = { stages: new Map(), mesh: 0 }

// EventBuffer хранит окно ленты (≤cap событий) и очередь ещё не опубликованных событий. Публикуемые массивы
// не мутируются: каждый flush отдаёт новый массив, а индекс дедупа покрывает окно и очередь вместе.
export class EventBuffer {
  private window: AfmEvent[] = []
  private pending: AfmEvent[] = []
  // Счётчик по ключу, а не множество: одинаковый ключ может встретиться в окне повторно (после слияния истории).
  private ingestKeys = new Map<string, number>()
  // Сколько самых старых событий (окно, затем очередь) уже вытеснено из логического окна ≤cap: их ключи
  // освобождены сразу при приёме, не дожидаясь flush, иначе повтор до flush ошибочно отбрасывается.
  private releasedCount = 0
  // Map, а не объект: id стадии вроде 'constructor' не должен совпасть с ключом Object.prototype.
  private stageArrivals = new Map<string, number>()
  private meshArrivals = 0

  constructor(private readonly cap: number = MAX_EVENTS) {}

  // push ставит событие в очередь; false — событие схлопнуто как повтор и в ленту не попадёт.
  push(event: AfmEvent): boolean {
    const last = this.pending[this.pending.length - 1] ?? this.window[this.window.length - 1]
    if (last !== undefined && isSameStatusEvent(last, event)) return false

    const key = ingestKey(event)
    if (key !== null) {
      if (this.ingestKeys.has(key)) return false
      this.ingestKeys.set(key, 1)
    }

    this.pending.push(event)
    this.countArrival(event)
    this.releaseOverflowKeys()
    return true
  }

  // arrivals — снимок счётчиков для публикации (после flush/merge); уже отданные снимки не меняются.
  arrivals(): ArrivalCounts {
    return { stages: new Map(this.stageArrivals), mesh: this.meshArrivals }
  }

  // flush переносит очередь в окно одним новым массивом; null, если очередь пуста.
  flush(): AfmEvent[] | null {
    if (this.pending.length === 0) return null

    // Ключи вытесненных событий уже освобождены в push — здесь только отрезаем их от окна.
    this.window = this.window.concat(this.pending).slice(this.releasedCount)
    this.pending = []
    this.releasedCount = 0
    return this.window
  }

  // merge сливает историю с окном и очередью; очередь при этом считается опубликованной.
  merge(history: AfmEvent[]): { events: AfmEvent[]; hasNewSignificant: boolean } {
    const live = this.pending.length === 0 ? this.window : this.window.concat(this.pending)
    const liveKeys = new Set(live.map(feedDedupeKey))
    const hasNewSignificant = history.some((e) => SIGNIFICANT_EVENT_TYPES.has(e.type) && !liveKeys.has(feedDedupeKey(e)))

    this.window = mergeCapped(history, live, this.cap)
    this.pending = []
    this.rebuildIndex()
    return { events: this.window, hasNewSignificant }
  }

  // discardPending забывает очередь вместе с её ключами; окно остаётся.
  discardPending(): void {
    this.pending = []
    this.rebuildIndex()
  }

  clear(): void {
    this.window = []
    this.pending = []
    this.ingestKeys.clear()
    this.releasedCount = 0
    this.stageArrivals.clear()
    this.meshArrivals = 0
  }

  private countArrival(event: AfmEvent): void {
    this.stageArrivals.set(event.stageId, (this.stageArrivals.get(event.stageId) ?? 0) + 1)
    if (event.type.startsWith('mesh_')) this.meshArrivals++
  }

  // Вытесненное событие больше не блокирует повтор: семантика как у проверки по окну.
  private releaseOverflowKeys(): void {
    const overflow = this.window.length + this.pending.length - this.cap
    while (this.releasedCount < overflow) {
      const index = this.releasedCount
      const evicted = index < this.window.length ? this.window[index] : this.pending[index - this.window.length]
      this.releasedCount++

      const key = evicted === undefined ? null : ingestKey(evicted)
      if (key === null) continue
      const count = this.ingestKeys.get(key) ?? 0
      if (count <= 1) this.ingestKeys.delete(key)
      else this.ingestKeys.set(key, count - 1)
    }
  }

  private rebuildIndex(): void {
    this.releasedCount = 0
    this.ingestKeys.clear()
    for (const e of [...this.window, ...this.pending]) {
      const key = ingestKey(e)
      if (key !== null) this.ingestKeys.set(key, (this.ingestKeys.get(key) ?? 0) + 1)
    }
  }
}

export { dedupeKey, feedDedupeKey, ingestKey }
