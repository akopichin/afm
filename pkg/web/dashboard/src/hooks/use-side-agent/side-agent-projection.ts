import type { SideAgentEvent } from '../../types/side-agent'

export type ActiveTurnSource = { kind: 'modal' } | { kind: 'mesh'; sender: string } | null

export type QueuedMessage = {
  messageId: string
  text: string
  stageId: string
  seq: number
}

export type SideAgentProjection = {
  queue: QueuedMessage[]
  activeTurnId: string
  // null — ход не идёт либо его происхождение ещё неизвестно (кадр turn_accepted не дошёл): Stop тогда недоступен.
  activeSource: ActiveTurnSource
}

// projectSideAgent выводит очередь и активный ход из полного потока событий. Одна чистая функция для
// модалки и хука, чтобы reload/reconnect давал то же, что живой поток.
export function projectSideAgent(events: readonly SideAgentEvent[]): SideAgentProjection {
  const queued = new Map<string, QueuedMessage>()
  let activeTurnId = ''
  let activeSource: ActiveTurnSource = null

  for (const e of events) {
    switch (e.type) {
      case 'message_queued':
        queued.set(e.data.messageId, { messageId: e.data.messageId, text: e.data.message, stageId: e.data.stageId, seq: e.seq })
        break
      case 'message_cancelled':
      case 'message_failed':
        queued.delete(e.data.messageId)
        break
      case 'turn_accepted':
        for (const id of e.data.messageIds ?? []) queued.delete(id)
        activeTurnId = e.turnId
        activeSource = e.data.sourceMessageId === undefined
          ? { kind: 'modal' }
          : { kind: 'mesh', sender: e.data.sourceSenderName || e.data.sourceSenderId || 'Unknown sender' }
        break
      case 'turn_started':
        // Начало хода без виденного turn_accepted: ход идёт, но чей он — неизвестно.
        if (e.turnId !== activeTurnId) {
          activeTurnId = e.turnId
          activeSource = null
        }
        break
      case 'turn_completed':
      case 'turn_failed':
      case 'turn_interrupted':
        if (e.turnId === activeTurnId) {
          activeTurnId = ''
          activeSource = null
        }
        break
    }
  }
  return { queue: [...queued.values()], activeTurnId, activeSource }
}
