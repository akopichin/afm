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

// participantInitials — до двух букв для квадратного аватара. Предпочитает заглавные из имени
// ("QA reviewer" → "QA", "DevOps" → "DO"), иначе первые буквы первых слов.
export function participantInitials(name: string): string {
  const caps = name.replace(/[^A-Z]/g, '')
  if (caps.length >= 2) return caps.slice(0, 2)
  const letters = name.split(/\s+/).filter(Boolean).map((w) => w[0]).join('')
  return (letters !== '' ? letters : name).slice(0, 2).toUpperCase()
}

// Presence — точка присутствия в ростере: работает, ждёт (пауза/ответ/коллега), упал или неактивен.
export type Presence = 'working' | 'waiting' | 'failed' | 'idle'

// stagePresence — присутствие агента стадии по её живому статусу.
export function stagePresence(status: string): Presence {
  switch (status) {
    case 'running':
    case 'planning':
    case 'revising':
    case 'retrying':
      return 'working'
    case 'paused':
    case 'awaiting_user_input':
    case 'awaiting_approval':
    case 'awaiting_assistants':
    case 'awaiting_peer':
      return 'waiting'
    case 'failed':
    case 'hook_failed':
      return 'failed'
    default:
      return 'idle'
  }
}

// boardNotifyCandidates — кого можно уведомить о новой задаче на доске: доступные агенты рана и агенты
// стадий, которые сейчас работают. Ждущий, упавший или завершившийся агент сообщение не прочитает.
export function boardNotifyCandidates(
  participants: MeshParticipant[],
  stages: { id: string; status: string }[],
): MeshParticipant[] {
  return participants.filter((p) => {
    if (p.kind === 'run_agent') return p.reachable
    if (p.kind !== 'stage_agent') return false
    const stage = stages.find((s) => s.id === p.stage_id)
    return stage !== undefined && stagePresence(stage.status) === 'working'
  })
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

// senderLabel — имя автора сообщения: «You» для оператора, имя участника, если он известен. Неизвестный
// (ещё не пришедший в ростер) участник называется по своему id без префикса, а id задачи — просто «Agent»:
// внутренние `run:t-…` в интерфейс не попадают.
export function senderLabel(senderId: string, participants: MeshParticipant[]): string {
  if (senderId === USER_ID) return 'You'
  const p = participants.find((x) => x.id === senderId)
  if (p !== undefined) return participantLabel(p)
  const bare = senderId.includes(':') ? senderId.slice(senderId.indexOf(':') + 1) : senderId
  return bare === '' || /^t-[0-9a-f]+$/.test(bare) ? 'Agent' : bare
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
