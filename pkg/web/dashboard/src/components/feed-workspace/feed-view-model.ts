import type { AfmEvent, StageStatus } from '../../types'

// Семантическая презентация ленты: каждое РЕАЛЬНОЕ событие из events.jsonl
// превращается в FeedItem с ролью (agent/user/system → сторона left/right),
// тоном (neutral/success/warning/danger/accent) и текстом. Никаких выдуманных
// «сообщений агента»: текст и его классы 1:1 повторяют прежний EventFeedPanel
// (toFeedLine), меняется только группировка и раскладка (мессенджер), а порядок
// и идентичность событий сохраняются (план §6).

export type FeedActor = 'agent' | 'user' | 'system'
export type FeedTone = 'neutral' | 'success' | 'warning' | 'danger' | 'accent'
export type FeedItemKind = 'tool' | 'script' | 'status' | 'success' | 'message' | 'dialog'

export type FeedItem = {
  key: string
  stageId: string
  actor: FeedActor
  side: 'left' | 'right' // user → right, agent/system → left
  tone: FeedTone
  kind: FeedItemKind
  text: string
  gap: string // статичная длительность: разница с предыдущим событием ленты
  mono: boolean // tool/script/auto-answer — компактная mono-строка
  // dialog_question/dialog_answer: координаты вопроса диалога для последующего
  // перехода к нему (клик по item — задача другой таски, здесь только данные).
  phase?: string
  id?: string
  navigable?: boolean
}

// FeedGroup — последовательные items одной стороны и стадии, слитые визуально в
// один «пузырь» с общей шапкой (стадия + актор). Группировка чисто визуальная.
export type FeedGroup = {
  key: string
  side: 'left' | 'right'
  stageId: string
  actor: FeedActor
  items: FeedItem[]
}

function statusTone(status: string): FeedTone {
  switch (status as StageStatus) {
    case 'done':
    case 'ready':
      return 'success'
    case 'failed':
    case 'hook_failed':
      return 'danger'
    case 'retrying':
    case 'paused':
      return 'warning'
    case 'awaiting_approval':
    case 'awaiting_user_input':
      return 'accent'
    default:
      return 'neutral'
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function str(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null || value === undefined) return ''
  return String(value)
}

function extractStatusString(data: unknown): string {
  if (typeof data === 'string') return data
  if (isRecord(data) && typeof data.status === 'string') return data.status
  return ''
}

type Mapped = {
  actor: FeedActor
  tone: FeedTone
  kind: FeedItemKind
  text: string
  mono: boolean
  phase?: string
  id?: string
  navigable?: boolean
}

// mapEvent — единственная точка соответствия «тип события → презентация».
// null — событие сознательно не рендерится в ленте (см. ask_user/user_answered ниже).
function mapEvent(event: AfmEvent): Mapped | null {
  const data = event.payload
  const obj = isRecord(data) ? data : {}

  switch (event.type) {
    case 'stage_status_changed': {
      const s = extractStatusString(data)
      return { actor: 'system', tone: statusTone(s), kind: 'status', text: `→ ${s}`, mono: false }
    }
    case 'agent_completed':
      return { actor: 'agent', tone: 'success', kind: 'success', text: `agent ${str(data)} completed`, mono: false }
    case 'agent_action': {
      const tool = str(obj.tool)
      const detail = str(obj.detail)
      // Нарратив агента (tool="text") — чистая проза без "text:"-префикса и без
      // mono: это сообщение агента, а не вызов инструмента. Прочие инструменты
      // (Bash/Read/Write…) — компактная mono-строка "<tool>: <detail>".
      if (tool === 'text') {
        return { actor: 'agent', tone: 'neutral', kind: 'message', text: detail, mono: false }
      }
      return { actor: 'agent', tone: 'neutral', kind: 'tool', text: `${tool}${detail !== '' ? `: ${detail}` : ''}`, mono: true }
    }
    case 'script_output':
      return { actor: 'agent', tone: 'neutral', kind: 'script', text: `[${str(obj.hook)}] ${str(obj.line)}`, mono: true }
    case 'hook_failed':
      return { actor: 'system', tone: 'danger', kind: 'status', text: `${str(obj.hook)}-hook failed: ${str(obj.error)}`, mono: false }
    case 'hook_resolved':
      return { actor: 'system', tone: 'success', kind: 'status', text: `${str(obj.hook)}-hook ${str(obj.resolution)}`, mono: false }
    case 'approved':
      return { actor: 'user', tone: 'success', kind: 'message', text: 'approved', mono: false }
    case 'revised':
      return { actor: 'user', tone: 'warning', kind: 'message', text: `revisions: ${str(data)}`, mono: false }
    case 'retry_scheduled':
      return { actor: 'system', tone: 'warning', kind: 'status', text: `retry: ${str(data)}`, mono: false }
    case 'retry_exhausted':
      return { actor: 'system', tone: 'danger', kind: 'status', text: 'retries exhausted', mono: false }
    case 'manual_retry':
      return { actor: 'system', tone: 'warning', kind: 'status', text: 'manual retry', mono: false }
    case 'ask_user':
    case 'user_answered':
      // Вытеснены dialog_question/dialog_answer (несут реальный текст вопроса/
      // ответа) — эти события больше не рендерим, чтобы не дублировать их пустой
      // заглушкой в ленте.
      return null
    case 'dialog_question': {
      const title = str(obj.title)
      return {
        actor: 'agent',
        tone: 'accent',
        kind: 'dialog',
        text: title !== '' ? title : 'question',
        mono: false,
        navigable: true,
        phase: str(obj.phase),
        id: str(obj.id),
      }
    }
    case 'dialog_answer': {
      const title = str(obj.title)
      return {
        actor: 'user',
        tone: 'accent',
        kind: 'message',
        text: title !== '' ? title : 'reply',
        mono: false,
        navigable: true,
        phase: str(obj.phase),
        id: str(obj.id),
      }
    }
    case 'auto_answered':
      return { actor: 'system', tone: 'neutral', kind: 'dialog', text: `auto-answered ${str(obj.id)}: ${str(obj.answer)}`, mono: true }
    case 'context_warning':
      return { actor: 'system', tone: 'warning', kind: 'status', text: `context warning: ${str(data)}`, mono: false }
    default:
      return { actor: 'system', tone: 'neutral', kind: 'status', text: event.type, mono: false }
  }
}

// Статичная длительность строки: разница между этим событием и предыдущим в
// ленте по порядку отображения (не per-стадийно). Нет предыдущего или
// невалидный timestamp — em dash. (Перенесено 1:1 из EventFeedPanel.)
export function formatEventGap(tsMs: number, prevTsMs: number): string {
  if (Number.isNaN(tsMs) || Number.isNaN(prevTsMs)) return '—'
  const diffSec = Math.max(0, Math.floor((tsMs - prevTsMs) / 1000))
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

// toFeedItems — события → FeedItem[] с уникальными стабильными ключами (тот же
// приём, что был в EventFeedPanel: seq либо `ts|type|stage`, при повторе —
// occurrence-суффикс `#N`, детерминированно при неизменном порядке).
export function toFeedItems(events: AfmEvent[]): FeedItem[] {
  const counts = new Map<string, number>()
  const items: FeedItem[] = []

  events.forEach((event, index) => {
    const m = mapEvent(event)
    if (m === null) return // ask_user/user_answered — не рендерим (см. mapEvent)

    const base = event.seq !== undefined ? `seq:${event.seq}` : `${event.timestamp}|${event.type}|${event.stageId}`
    const n = counts.get(base) ?? 0
    counts.set(base, n + 1)
    const key = n === 0 ? base : `${base}#${n}`

    const ts = Date.parse(event.timestamp)
    const prevTs = index > 0 ? Date.parse(events[index - 1]?.timestamp ?? '') : NaN
    items.push({
      key,
      stageId: event.stageId,
      actor: m.actor,
      side: m.actor === 'user' ? 'right' : 'left',
      tone: m.tone,
      kind: m.kind,
      text: m.text,
      gap: formatEventGap(ts, prevTs),
      mono: m.mono,
      phase: m.phase,
      id: m.id,
      navigable: m.navigable,
    })
  })

  return items
}

// groupFeedItems сливает подряд идущие items одной стороны и стадии в один
// FeedGroup. Разрыв (новый пузырь) — при смене стороны/стадии/актора.
// Порядок items внутри группы сохраняется, идентичность (key) не теряется.
export function groupFeedItems(items: FeedItem[]): FeedGroup[] {
  const groups: FeedGroup[] = []
  for (const item of items) {
    const last = groups[groups.length - 1]
    if (last && last.side === item.side && last.stageId === item.stageId && last.actor === item.actor) {
      last.items.push(item)
    } else {
      groups.push({ key: item.key, side: item.side, stageId: item.stageId, actor: item.actor, items: [item] })
    }
  }
  return groups
}
