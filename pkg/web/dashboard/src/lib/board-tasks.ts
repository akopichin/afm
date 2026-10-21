// board-tasks — the one projection of the shared board into tasks, used by Team's mixed board and the Board tab.
// A task is a direct "task/<slug>" key; agents report under "<task key>/<alias>". Only reports whose author is an
// agent of the roster drive the task's status, so the operator cannot move a card by publishing a child key.

import type { MeshBoardEntry, MeshParticipant } from '../api/mesh-client'

export type BoardTaskStatus = 'todo' | 'in_progress' | 'done'

type ReportMarker = 'in_progress' | 'done'

// BoardReport — an author's current report on a task. marker is set when the text starts with a status marker;
// claimant is set when an agent made such a report (so it counts toward the task's status).
export type BoardReport = {
  entry: MeshBoardEntry
  marker: ReportMarker | null
  claimant: boolean
}

// BoardAssignee — an agent that claimed the task; inProgress is false once its own report says "done:".
export type BoardAssignee = {
  id: string
  inProgress: boolean
}

export type BoardTask = {
  key: string
  // entry — the task entry itself (author, version, artifacts); text — its original text.
  entry: MeshBoardEntry
  text: string
  status: BoardTaskStatus
  assignees: BoardAssignee[]
  // reports — each agent's latest report under the task plus every non-agent entry, oldest first.
  reports: BoardReport[]
}

const TASK_PREFIX = 'task/'
const AGENT_KINDS: ReadonlySet<string> = new Set(['stage_agent', 'helper', 'run_agent', 'side_agent'])

function isTaskKey(key: string): boolean {
  return key.startsWith(TASK_PREFIX) && key.length > TASK_PREFIX.length && !key.includes('/', TASK_PREFIX.length)
}

function markerOf(text: string): ReportMarker | null {
  if (/^in progress:/i.test(text)) return 'in_progress'
  if (/^done:/i.test(text)) return 'done'
  return null
}

// reportParent — the entry a report is filed under ("<key>/<alias>" under "<key>"), if any. A real task is always
// top-level, even when a fact's key (like "task") happens to prefix it; the nearest (longest) parent key wins.
function reportParent(entry: MeshBoardEntry, entries: MeshBoardEntry[]): MeshBoardEntry | undefined {
  if (isTaskKey(entry.key)) return undefined
  return entries
    .filter((t) => t.key !== entry.key && entry.key.startsWith(t.key + '/'))
    .reduce<MeshBoardEntry | undefined>((best, t) => (best === undefined || t.key.length > best.key.length ? t : best), undefined)
}

// boardTopLevel — the board's own entries (tasks and facts), without the reports filed under them.
export function boardTopLevel(entries: MeshBoardEntry[]): MeshBoardEntry[] {
  return entries.filter((e) => reportParent(e, entries) === undefined)
}

// boardReportsOf — the reports filed under entry, in board order.
export function boardReportsOf(entry: MeshBoardEntry, entries: MeshBoardEntry[]): MeshBoardEntry[] {
  return entries.filter((e) => reportParent(e, entries)?.key === entry.key)
}

// boardTaskCount — the tasks on the board (the Board tab's badge): direct "task/<slug>" keys, not facts or reports.
export function boardTaskCount(entries: MeshBoardEntry[]): number {
  return entries.filter((e) => isTaskKey(e.key)).length
}

function taskReports(task: MeshBoardEntry, entries: MeshBoardEntry[], agentIds: ReadonlySet<string>): BoardReport[] {
  // Only direct "<task key>/<alias>" entries are reports: the alias is nonempty and has no further "/".
  const prefix = task.key + '/'
  const children = entries.filter(
    (e) => e.key.startsWith(prefix) && e.key.length > prefix.length && !e.key.includes('/', prefix.length),
  )
  // An agent's current report is its latest one; other authors cannot claim, so all their entries stay visible.
  const latestByAgent = new Map<string, MeshBoardEntry>()
  for (const e of children) {
    const prev = latestByAgent.get(e.author_id)
    if (agentIds.has(e.author_id) && (prev === undefined || e.seq > prev.seq)) latestByAgent.set(e.author_id, e)
  }
  return children
    .filter((e) => !agentIds.has(e.author_id) || latestByAgent.get(e.author_id) === e)
    .sort((a, b) => a.seq - b.seq)
    .map((entry) => {
      const marker = markerOf(entry.status)
      return { entry, marker, claimant: marker !== null && agentIds.has(entry.author_id) }
    })
}

function taskStatus(claimants: BoardReport[]): BoardTaskStatus {
  if (claimants.length === 0) return 'todo'
  return claimants.every((r) => r.marker === 'done') ? 'done' : 'in_progress'
}

// boardTasks — the board's tasks in board order, with status, assignees and each author's latest report.
export function boardTasks(entries: MeshBoardEntry[], participants: MeshParticipant[]): BoardTask[] {
  const agentIds = new Set(participants.filter((p) => AGENT_KINDS.has(p.kind)).map((p) => p.id))
  return entries
    .filter((e) => isTaskKey(e.key))
    .map((entry) => {
      const reports = taskReports(entry, entries, agentIds)
      const claimants = reports.filter((r) => r.claimant)
      return {
        key: entry.key,
        entry,
        text: entry.status,
        status: taskStatus(claimants),
        assignees: claimants.map((r) => ({ id: r.entry.author_id, inProgress: r.marker === 'in_progress' })),
        reports,
      }
    })
}
