// mesh-roster — общие чистые хелперы над ростером mesh для Team, ленты и меню упоминаний: человекочитаемые
// имена вместо внутренних id (`mt-…`, `side:<run-id>`) и поиск личного треда с участником.

import type { MeshParticipant, MeshThread } from '../api/mesh-client'

const USER_ID = 'user'

// participantLabel — display_name, иначе alias, иначе id.
export function participantLabel(p: MeshParticipant): string {
  if (p.display_name !== undefined && p.display_name !== '') return p.display_name
  if (p.alias !== undefined && p.alias !== '') return p.alias
  return p.id
}

// mentionHandle — короткое имя для `@`: сервер даёт alias (`side`, id стадии), полный id — запасной вариант.
export function mentionHandle(p: MeshParticipant): string {
  return p.alias !== undefined && p.alias !== '' ? p.alias : p.id
}

// participantRole — подпись роли по kind участника.
export function participantRole(kind: string): string {
  switch (kind) {
    case 'run_agent':
      return 'Run agent'
    case 'stage_agent':
      return 'Stage executor'
    case 'helper':
      return 'Assistant'
    case 'side_agent':
      return 'Side agent'
    default:
      return kind
  }
}

// senderLabel — имя автора сообщения: «You» для оператора, имя участника, если он известен, иначе id.
export function senderLabel(senderId: string, participants: MeshParticipant[]): string {
  if (senderId === USER_ID) return 'You'
  const p = participants.find((x) => x.id === senderId)
  return p !== undefined ? participantLabel(p) : senderId
}

// threadLabel — название треда, а у личного треда без названия — имена собеседников (кроме оператора).
export function threadLabel(thread: MeshThread, participants: MeshParticipant[]): string {
  if (thread.title !== undefined && thread.title !== '') return thread.title
  const others = thread.members.filter((m) => m !== USER_ID).map((m) => senderLabel(m, participants))
  return others.length > 0 ? others.join(', ') : thread.id
}

// findExistingDirectThread — уже открытый тред ровно из двух участников (оператор + participant). Повторное
// обращение к тому же участнику должно попасть в ЭТОТ разговор: OpenThread на каждый вызов создаёт новый
// тред, дедупликации по составу на сервере нет. Групповой тред, где участник тоже есть, сюда намеренно не
// подходит — отправка никогда не превращает личный разговор в групповой.
export function findExistingDirectThread(threads: MeshThread[], participantId: string): MeshThread | null {
  return (
    threads.find(
      (t) => t.status === 'open' && t.members.length === 2 && t.members.includes(participantId) && t.members.includes(USER_ID),
    ) ?? null
  )
}
