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

// reportParentKey — the key an entry is filed under as a report: "<parent>/<alias>" → "<parent>", with a nonempty
// alias. The one rule for a report, used by tasks and by Team's facts alike. A task is never a report, even when
// a fact's key (like "task") happens to prefix it.
function reportParentKey(entry: MeshBoardEntry): string | undefined {
  if (isTaskKey(entry.key)) return undefined
  const slash = entry.key.lastIndexOf('/')
  if (slash <= 0 || slash === entry.key.length - 1) return undefined
  return entry.key.slice(0, slash)
}

// boardTopLevel — the entries Team shows on their own (tasks and facts). Team shows one level: an entry is
// hidden only when it is rendered as a report under a parent that is itself shown, so no entry is ever lost.
export function boardTopLevel(entries: MeshBoardEntry[]): MeshBoardEntry[] {
  const shown = new Set<string>()
  // A parent's key is shorter than its report's, so going from short keys to long ones decides parents first.
  for (const e of [...entries].sort((a, b) => a.key.length - b.key.length)) {
    const parent = reportParentKey(e)
    if (parent === undefined || !shown.has(parent)) shown.add(e.key)
  }
  return entries.filter((e) => shown.has(e.key))
}

// boardReportsOf — the reports filed under a shown entry, in board order.
export function boardReportsOf(entry: MeshBoardEntry, entries: MeshBoardEntry[]): MeshBoardEntry[] {
  return entries.filter((e) => reportParentKey(e) === entry.key)
}

// boardTaskCount — the tasks on the board (the Board tab's badge): direct "task/<slug>" keys, not facts or reports.
export function boardTaskCount(entries: MeshBoardEntry[]): number {
  return entries.filter((e) => isTaskKey(e.key)).length
}

function taskReports(task: MeshBoardEntry, entries: MeshBoardEntry[], agentIds: ReadonlySet<string>): BoardReport[] {
  const children = boardReportsOf(task, entries)
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
