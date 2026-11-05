// Презентация «бокового» разговора в тех же FeedItem/FeedGroup, что и лента стадий — чтобы
// переиспользовать FeedGroupView/Markdown БЕЗ подделки событий стадии.

import { errorCodeToMessage, type SideAgentEvent } from '../../types/side-agent'
import { formatEventGap, groupFeedItems, type FeedGroup, type FeedItem } from '../feed-workspace/feed-view-model'

type Mapped = Pick<FeedItem, 'actor' | 'side' | 'tone' | 'kind' | 'text' | 'mono'> & { markdown?: boolean; authorLabel?: string }
type Row = Mapped & { key: string }

const userMessage = (text: string): Mapped => ({ actor: 'user', side: 'right', tone: 'neutral', kind: 'message', text, mono: false })

// mapSideAgentEvent — единственная точка соответствия «тип события беседы → строки презентации».
// queuedTexts — тексты message_queued по message_id: групповой ход показывает их как отдельные реплики.
function mapSideAgentEvent(event: SideAgentEvent, queuedTexts: ReadonlyMap<string, string>): Row[] {
  const key = `seq:${event.seq}`
  switch (event.type) {
    case 'turn_accepted': {
      const { messageIds } = event.data
      if (messageIds !== undefined) {
        // Реплики группы стоят на месте claim, а не приёма: между ними и ответом нет чужих сообщений.
        return messageIds.flatMap((id) => {
          const text = queuedTexts.get(id)
          return text === undefined ? [] : [{ ...userMessage(text), key: `${key}:${id}` }]
        })
      }
      // Реплика пользователя — справа, обычным текстом (как agent_note в ленте
      // стадий): введённые `*`/`#`/URL не должны менять вид сообщения.
      return event.data.sourceMessageId !== undefined
        ? [{ key, actor: 'user', side: 'left', tone: 'neutral', kind: 'message', text: event.data.message, mono: false,
            authorLabel: `From ${event.data.sourceSenderName || event.data.sourceSenderId || 'Unknown sender'}` }]
        : [{ ...userMessage(event.data.message), key }]
    }
    case 'assistant_text':
      // Ответ агента — слева, как markdown (заголовки/код/таблицы), тем же
      // санитайзером, что и проза стадий.
      return [{ key, actor: 'agent', side: 'left', tone: 'neutral', kind: 'message', text: event.data.text, mono: false, markdown: true }]
    case 'tool_action': {
      const { tool, detail } = event.data
      return [{ key, actor: 'agent', side: 'left', tone: 'neutral', kind: 'tool', text: `${tool}${detail !== '' ? `: ${detail}` : ''}`, mono: true }]
    }
    case 'turn_failed': {
      // Код → короткий человекочитаемый текст.
      const label = errorCodeToMessage(event.data.code)
      const { message } = event.data
      return [{ key, actor: 'system', side: 'left', tone: 'danger', kind: 'status', text: message !== '' ? `${label}: ${message}` : label, mono: false }]
    }
    case 'message_failed': {
      const text = queuedTexts.get(event.data.messageId) ?? ''
      const label = `Message not sent: ${errorCodeToMessage(event.data.code)}`
      return [{ key, actor: 'system', side: 'left', tone: 'danger', kind: 'status', text: text !== '' ? `${label} — ${text}` : label, mono: false }]
    }
    case 'turn_interrupted': {
      const { reason } = event.data
      return [{ key, actor: 'system', side: 'left', tone: 'warning', kind: 'status', text: reason !== '' ? `Interrupted: ${reason}` : 'Interrupted', mono: false }]
    }
    case 'turn_started':
    case 'turn_completed':
    case 'message_queued':
    case 'message_cancelled':
      return []
  }
}

// toSideAgentFeedItems — события беседы → FeedItem[]. gap считается ровно как в ленте стадий
// (formatEventGap).
export function toSideAgentFeedItems(events: SideAgentEvent[]): FeedItem[] {
  const queuedTexts = new Map<string, string>()
  for (const e of events) if (e.type === 'message_queued') queuedTexts.set(e.data.messageId, e.data.message)

  const items: FeedItem[] = []
  events.forEach((event) => {
    const ts = Date.parse(event.timestamp)
    for (const m of mapSideAgentEvent(event, queuedTexts)) {
      const prevTs = items[items.length - 1]?.ts ?? NaN
      items.push({
        key: m.key,
        stageId: '',
        actor: m.actor,
        authorLabel: m.authorLabel,
        side: m.side,
        tone: m.tone,
        kind: m.kind,
        text: m.text,
        gap: formatEventGap(ts, prevTs),
        ts,
        firstEvent: items.length === 0,
        mono: m.mono,
        markdown: m.markdown,
      })
    }
  })
  return items
}

// sideAgentFeedGroups — готовые группы для FeedGroupView (тот же groupFeedItems,
// что у ленты стадий: сливает подряд идущие строки одной стороны/актора).
export function sideAgentFeedGroups(events: SideAgentEvent[]): FeedGroup[] {
  return groupFeedItems(toSideAgentFeedItems(events))
}
