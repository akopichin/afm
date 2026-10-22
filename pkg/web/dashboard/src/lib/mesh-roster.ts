// mesh-roster — общие чистые хелперы над ростером mesh для Team, ленты и меню упоминаний: человекочитаемые
// имена вместо внутренних id (`mt-…`, `side:<run-id>`) и поиск личного треда с участником.

import type { MeshParticipant, MeshThread, MeshThreadSummaries } from '../api/mesh-client'

const USER_ID = 'user'

// participantLabel — display_name, иначе alias, иначе id.
export function participantLabel(p: MeshParticipant): string {
  if (p.display_name !== undefined && p.display_name !== '') return p.display_name
  if (p.alias !== undefined && p.alias !== '') return p.alias
  return p.id
}

// mergeById — the roster's list plus local items it does not know yet (the roster wins once it has them).
export function mergeById<T extends { id: string }>(fromRoster: T[], local: T[]): T[] {
  const missing = local.filter((l) => !fromRoster.some((r) => r.id === l.id))
  return missing.length === 0 ? fromRoster : [...fromRoster, ...missing]
}

// mentionHandle — короткое имя для `@`: сервер даёт alias (`side`, id стадии), полный id — запасной вариант.
export function mentionHandle(p: MeshParticipant): string {
  return p.alias !== undefined && p.alias !== '' ? p.alias : p.id
}

// mentionHandles — `@`-имя каждого участника (по id), уникальное среди participants: alias, а если такой же
// alias (без учёта регистра) есть у кого-то ещё — полный id. Так одно упоминание никогда не адресует двоих.
export function mentionHandles(participants: MeshParticipant[]): Map<string, string> {
  const owners = new Map<string, number>()
  for (const p of participants) {
    const h = mentionHandle(p).toLowerCase()
    owners.set(h, (owners.get(h) ?? 0) + 1)
  }
  return new Map(participants.map((p) => [p.id, owners.get(mentionHandle(p).toLowerCase()) === 1 ? mentionHandle(p) : p.id]))
}

// participantInitials — до двух букв для квадратного аватара. Предпочитает заглавные из имени
// ("QA reviewer" → "QA", "DevOps" → "DO"), иначе первые буквы первых слов.
export function participantInitials(name: string): string {
  const caps = name.replace(/[^A-Z]/g, '')
  if (caps.length >= 2) return caps.slice(0, 2)
  const words = name.split(/\s+/).filter(Boolean)
  // One word ("Persistence") gives its first two letters, not a lone "P".
  const letters = words.length > 1 ? words.map((w) => w[0]).join('') : (words[0] ?? name)
  return letters.slice(0, 2).toUpperCase()
}

// Presence — точка присутствия в ростере: работает, ждёт (пауза/ответ/коллега), упал или неактивен.
export type Presence = 'working' | 'waiting' | 'failed' | 'idle'

// activityLabel — что делает run-агент или помощник сейчас, словами для строки ростера.
export function activityLabel(activity: string | undefined): string | null {
  switch (activity) {
    case 'working':
      return 'Working'
    case 'waiting':
      return 'Waiting'
    case 'idle':
      return 'Idle'
    case 'paused':
      return 'Paused'
    case 'done':
      return 'Done'
    default:
      return null
  }
}

// activityPresence — точка присутствия run-агента или помощника по его активности.
export function activityPresence(activity: string | undefined, reachable: boolean): Presence {
  switch (activity) {
    case 'working':
      return 'working'
    case 'waiting':
    case 'paused':
      return 'waiting'
    case 'idle':
    case 'done':
      return 'idle'
    default:
      return reachable ? 'working' : 'idle'
  }
}

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
// стадий, которые работают или ждут коллег (адресованное сообщение их будит). Стадия, ждущая человека,
// упавшая или завершившаяся, сообщение не прочитает, как и script-стадия: у неё нет агента (reachable: false).
export function boardNotifyCandidates(
  participants: MeshParticipant[],
  stages: { id: string; status: string }[],
): MeshParticipant[] {
  return participants.filter((p) => {
    if (p.reachable !== true) return false
    if (p.kind === 'run_agent') return true
    if (p.kind !== 'stage_agent') return false
    const stage = stages.find((s) => s.id === p.stage_id)
    return stage !== undefined && (stagePresence(stage.status) === 'working' || stage.status === 'awaiting_peer')
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
      return 'Helper'
    case 'side_agent':
      return 'Assistant'
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

// An untitled two-member conversation is the reusable DM. A titled conversation with the same members is
// a separate discussion. When several DMs exist, use the latest durable message seq; ties follow open order.
export function findExistingDirectThread(threads: MeshThread[], participantId: string, summaries: MeshThreadSummaries = {}): MeshThread | null {
  let latest: MeshThread | null = null
  let latestSeq = -1
  for (const thread of threads) {
    if (thread.status !== 'open' || thread.title || thread.members.length !== 2 ||
        !thread.members.includes(participantId) || !thread.members.includes(USER_ID)) continue
    const seq = summaries[thread.id]?.last_message?.seq ?? 0
    if (seq >= latestSeq) {
      latest = thread
      latestSeq = seq
    }
  }
  return latest
}

// directPeer — the other person of a one-to-one conversation (the operator and exactly one participant), so it
// is shown like a direct message — that person's avatar and role — and not as a group.
export function directPeer(thread: MeshThread, participants: MeshParticipant[]): MeshParticipant | undefined {
  if (thread.members.length !== 2 || !thread.members.includes(USER_ID)) return undefined
  const other = thread.members.find((m) => m !== USER_ID)
  return participants.find((p) => p.id === other)
}

// threadMembersLine — who is in a conversation, by initials ("QA · DO · FE"), for its row in the roster.
export function threadMembersLine(thread: MeshThread, participants: MeshParticipant[]): string {
  return thread.members
    .filter((m) => m !== USER_ID)
    .map((m) => participantInitials(senderLabel(m, participants)))
    .join(' · ')
}
