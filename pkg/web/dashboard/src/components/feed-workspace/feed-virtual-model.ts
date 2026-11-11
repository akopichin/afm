import type { MeshMessage } from '../../api/mesh-client'
import { groupToolRuns, type FeedActor, type FeedGroup, type FeedItem, type FeedTimelineEntry } from './feed-view-model'

// Плоская модель ленты для виртуализатора: одна строка списка вместо группы. Группа события режется по одной
// строке на item, чтобы одна запись с тысячами строк не была одним неделимым блоком.

// RowDeco — оформление строки, которое раньше давала обёртка группы: отступы и шапка задаются по этим полям.
export type RowDeco = {
  // groupKey — запись ленты, из которой вырезана строка; start/end — первая и последняя строка записи.
  groupKey: string
  start: boolean
  end: boolean
  side: 'left' | 'right'
  actor: FeedActor
  stageId: string
  authorLabel?: string
}

export type VirtualFeedRow = RowDeco &
  (
    | { kind: 'event'; key: string; item: FeedItem }
    // Рамочная (user) группа — одна строка: пузырь с общей шапкой нельзя резать.
    | { kind: 'framed'; key: string; group: FeedGroup }
    | { kind: 'conversation'; key: string; message: MeshMessage; previousCreatedAt?: string }
    // Подпись серии tool-вызовов; open — раскрыта ли она, items — все её вызовы.
    | { kind: 'tool-summary'; key: string; items: FeedItem[]; open: boolean }
    // last — последний вызов серии: у него закрывается фон серии.
    | { kind: 'tool-call'; key: string; item: FeedItem; seriesKey: string; last: boolean }
  )

export type VirtualFeedModel = {
  rows: VirtualFeedRow[]
  indexByKey: Map<string, number>
  rowKeyByMessageId: Map<string, string>
}

// buildVirtualFeedRows раскладывает timeline в строки. Серия открыта, если открыт любой её вызов: ключ серии
// зависит от первого вызова, а вытеснение лентой снимает именно его.
export function buildVirtualFeedRows(timeline: FeedTimelineEntry[], openTools: ReadonlySet<string>): VirtualFeedModel {
  const rows: VirtualFeedRow[] = []
  const rowKeyByMessageId = new Map<string, string>()

  for (const entry of timeline) {
    const first = rows.length
    if (entry.kind === 'conversation') {
      const deco = { groupKey: entry.key, side: 'left', actor: 'system', stageId: '' } as const
      entry.messages.forEach((message, index) => {
        rows.push({ ...deco, start: false, end: false, kind: 'conversation', key: message.id, message, previousCreatedAt: entry.messages[index - 1]?.created_at })
        rowKeyByMessageId.set(message.id, message.id)
      })
    } else if (entry.group.actor === 'user') {
      rows.push({ ...groupDeco(entry), start: false, end: false, kind: 'framed', key: entry.group.key, group: entry.group })
    } else {
      const deco = groupDeco(entry)
      for (const run of groupToolRuns(entry.group.items)) {
        if (run.kind === 'item') {
          rows.push({ ...deco, start: false, end: false, kind: 'event', key: run.item.key, item: run.item })
          continue
        }
        const open = run.items.some((item) => openTools.has(item.key))
        rows.push({ ...deco, start: false, end: false, kind: 'tool-summary', key: run.key, items: run.items, open })
        if (open) {
          run.items.forEach((item, index) => rows.push({ ...deco, start: false, end: false, kind: 'tool-call', key: item.key, item, seriesKey: run.key, last: index === run.items.length - 1 }))
        }
      }
    }
    if (rows.length > first) {
      rows[first] = { ...(rows[first] as VirtualFeedRow), start: true }
      rows[rows.length - 1] = { ...(rows[rows.length - 1] as VirtualFeedRow), end: true }
    }
  }

  const indexByKey = new Map(rows.map((row, index) => [row.key, index]))
  return { rows, indexByKey, rowKeyByMessageId }
}

// openSeriesTails — последний вызов каждой раскрытой серии. Считается по неотфильтрованной ленте: фильтр может
// скрыть продолжение серии, и она не должна терять открытое состояние.
export function openSeriesTails(timeline: FeedTimelineEntry[], openTools: ReadonlySet<string>): string[] {
  const tails: string[] = []
  for (const entry of timeline) {
    if (entry.kind !== 'group' || entry.group.actor === 'user') continue
    for (const run of groupToolRuns(entry.group.items)) {
      if (run.kind === 'tools' && run.items.some((item) => openTools.has(item.key))) tails.push(run.items[run.items.length - 1]?.key ?? '')
    }
  }
  return tails
}

function groupDeco(entry: Extract<FeedTimelineEntry, { kind: 'group' }>): Omit<RowDeco, 'start' | 'end'> {
  const { side, actor, stageId, authorLabel } = entry.group
  return { groupKey: entry.key, side, actor, stageId, authorLabel }
}

// headShift — сколько строк снято с головы списка, если следующий набор — тот же хвост плюс дописанные строки;
// иначе null (слияние, фильтр, перенумерация): такую смену состава держит якорь, а не firstItemIndex.
export function headShift(prevKeys: readonly string[], nextKeys: readonly string[]): number | null {
  if (prevKeys.length === 0 || nextKeys.length === 0) return null
  const shift = prevKeys.indexOf(nextKeys[0] as string)
  if (shift < 0) return null
  const kept = prevKeys.length - shift
  if (nextKeys.length < kept) return null
  for (let i = 0; i < kept; i++) if (nextKeys[i] !== prevKeys[shift + i]) return null
  return shift
}

// rowSignature — короткая подпись содержимого строки: меняется, когда строка выросла (дописан текст, вызов в
// серии), и по ней лента, идущая за хвостом, понимает, что нужно снова встать к низу.
export function rowSignature(row: VirtualFeedRow): string {
  switch (row.kind) {
    case 'event':
    case 'tool-call':
      return `${row.key}:${row.item.text.length}`
    case 'tool-summary':
      return `${row.key}:${row.items.length}:${row.open}`
    case 'framed':
      return `${row.key}:${row.group.items.length}`
    case 'conversation':
      return `${row.key}:${row.message.body.length}`
  }
}
