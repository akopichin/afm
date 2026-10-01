// Презентация «бокового» разговора (Side agent) в тех же FeedItem/FeedGroup, что
// и лента стадий — чтобы переиспользовать FeedGroupView/Markdown БЕЗ подделки
// событий стадии (у беседы собственный журнал, см. pkg/sideagent). Mapper 1:1
// переводит SideAgentEvent → FeedItem; группировка и рендер — те же примитивы
// (groupFeedItems/FeedGroupView), что у FeedWorkspace.

import { errorCodeToMessage, type SideAgentEvent } from '../../types/side-agent'
import { formatEventGap, groupFeedItems, type FeedGroup, type FeedItem } from '../feed-workspace/feed-view-model'

// mapSideAgentEvent — единственная точка соответствия «тип события беседы →
// презентация». null — событие сознательно не рендерится строкой (turn_started/
// turn_completed не несут видимого пользователю контента: статус запроса и имя
// команды показываются модалкой отдельно).
type Mapped = Pick<FeedItem, 'actor' | 'side' | 'tone' | 'kind' | 'text' | 'mono'> & { markdown?: boolean }

function mapSideAgentEvent(event: SideAgentEvent): Mapped | null {
  switch (event.type) {
    case 'turn_accepted':
      // Реплика пользователя — справа, обычным текстом (как agent_note в ленте
      // стадий): введённые `*`/`#`/URL не должны менять вид сообщения.
      return { actor: 'user', side: 'right', tone: 'neutral', kind: 'message', text: event.data.message, mono: false }
    case 'assistant_text':
      // Ответ агента — слева, как markdown (заголовки/код/таблицы), тем же
      // санитайзером, что и проза стадий.
      return { actor: 'agent', side: 'left', tone: 'neutral', kind: 'message', text: event.data.text, mono: false, markdown: true }
    case 'tool_action': {
      const { tool, detail } = event.data
      return { actor: 'agent', side: 'left', tone: 'neutral', kind: 'tool', text: `${tool}${detail !== '' ? `: ${detail}` : ''}`, mono: true }
    }
    case 'turn_failed': {
      // Код → короткий человекочитаемый текст (тот же errorCodeToMessage, что у
      // клиентских ошибок). message (уже редактированный на бэкенде, инвариант 8)
      // дописывается, только если непустой — чтобы не потерять диагностику, не
      // дублируя её при пустом поле.
      const label = errorCodeToMessage(event.data.code)
      const { message } = event.data
      return { actor: 'system', side: 'left', tone: 'danger', kind: 'status', text: message !== '' ? `${label}: ${message}` : label, mono: false }
    }
    case 'turn_interrupted': {
      const { reason } = event.data
      return { actor: 'system', side: 'left', tone: 'warning', kind: 'status', text: reason !== '' ? `Interrupted: ${reason}` : 'Interrupted', mono: false }
    }
    case 'turn_started':
    case 'turn_completed':
      return null
  }
}

// toSideAgentFeedItems — события беседы → FeedItem[]. Ключ строки — стабильный
// `seq:<seq>` (seq монотонен и уникален в журнале беседы): replay/переоткрытие
// окна НЕ меняют ключи, поэтому entrance-анимации старых сообщений не
// перезапускаются. gap считается ровно как в ленте стадий (formatEventGap).
// stageId пуст — у беседы нет стадии (FeedGroupView со stageId==='' не рисует
// бейдж, группировка идёт по стороне/актору).
export function toSideAgentFeedItems(events: SideAgentEvent[]): FeedItem[] {
  const items: FeedItem[] = []
  events.forEach((event, index) => {
    const m = mapSideAgentEvent(event)
    if (m === null) return
    const ts = Date.parse(event.timestamp)
    const prevTs = index > 0 ? Date.parse(events[index - 1]?.timestamp ?? '') : NaN
    items.push({
      key: `seq:${event.seq}`,
      stageId: '',
      actor: m.actor,
      side: m.side,
      tone: m.tone,
      kind: m.kind,
      text: m.text,
      gap: formatEventGap(ts, prevTs),
      mono: m.mono,
      markdown: m.markdown,
    })
  })
  return items
}

// sideAgentFeedGroups — готовые группы для FeedGroupView (тот же groupFeedItems,
// что у ленты стадий: сливает подряд идущие строки одной стороны/актора).
export function sideAgentFeedGroups(events: SideAgentEvent[]): FeedGroup[] {
  return groupFeedItems(toSideAgentFeedItems(events))
}
