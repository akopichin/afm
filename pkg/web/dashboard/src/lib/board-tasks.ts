// board-tasks — the one projection of the shared board into tasks, used by Team's mixed board and the Board tab.
// A task is a direct "task/<slug>" key; agents report under "<task key>/<alias>". Only reports whose author is an
// agent of the roster drive the task's status, so the operator cannot move a card by publishing a child key.

import type { MeshBoardEntry, MeshBoardSnapshot, MeshBoardTask, MeshBoardTaskAssignment, MeshParticipant } from '../api/mesh-client'

export type BoardTaskStatus = 'todo' | 'in_progress' | 'done'

type ReportMarker = 'in_progress' | 'done'

// BoardReport — an author's current report on a task. marker is set when the text starts with a status marker;
// claimant is set when an agent made such a report (so it counts toward the task's status).
export type BoardReport = {
  entry: MeshBoardEntry
  marker: ReportMarker | null
  claimant: boolean
}

// BoardAssignee — a participant the task is on. Typed tasks carry the assignment's own state; a legacy agent's
// state comes from its report marker ("in progress:" / "done:"), and a legacy claimant is never merely 'assigned'.
export type BoardAssignee = {
  id: string
  state: 'assigned' | 'in_progress' | 'done'
}

export type BoardTask = {
  key: string
  // entry / text — the task entry itself (author, version, artifacts) and its original text; a legacy task only.
  entry?: MeshBoardEntry
  text?: string
  status: BoardTaskStatus
  assignees: BoardAssignee[]
  // reports — each agent's latest report under the task plus every non-agent entry, oldest first; empty on a typed task.
  reports: BoardReport[]
  // author — who posted the task.
  author: string
  // title / description — the task's own fields (a legacy task splits them out of its text).
  title: string
  description: string
  // note — the latest progress or result an assignee reported (typed tasks only).
  note?: string
  // version — the task's version, the expectedVersion of a typed update.
  version: number
  // assignments / history — the typed task's active and released assignments; absent on a legacy task.
  assignments?: MeshBoardTaskAssignment[]
  history?: MeshBoardTaskAssignment[]
}

// The task form publishes "<title> — <details>"; a task an agent published may have no details.
const TITLE_SEPARATOR = ' — '

// splitTaskText — the task's title and its description (empty when the text has no details).
export function splitTaskText(text: string): { title: string; description: string } {
  const at = text.indexOf(TITLE_SEPARATOR)
  if (at < 0) return { title: text, description: '' }
  return { title: text.slice(0, at), description: text.slice(at + TITLE_SEPARATOR.length) }
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
        assignees: claimants.map((r) => ({ id: r.entry.author_id, state: r.marker === 'in_progress' ? 'in_progress' : 'done' })),
        reports,
        author: entry.author_id,
        ...splitTaskText(entry.status),
        version: entry.version,
      }
    })
}

// assignmentActivitySeq — the latest event of an assignment: assigned, started or completed.
function assignmentActivitySeq(a: MeshBoardTaskAssignment): number {
  return Math.max(a.assigned_seq, a.started_seq ?? 0, a.completed_seq ?? 0)
}

// latestNote — the result or progress text of the most recently active assignment that has one.
function latestNote(assignments: MeshBoardTaskAssignment[]): string | undefined {
  const byRecency = [...assignments].sort((a, b) => assignmentActivitySeq(b) - assignmentActivitySeq(a))
  for (const a of byRecency) {
    const text = a.result || a.progress
    if (text) return text
  }
  return undefined
}

// taskFromView — a typed task of the server as a BoardTask. Assignees are the active assignments (a participant
// outside the roster stays; the views resolve names). A typed task has no entry, text or reports of its own.
export function taskFromView(view: MeshBoardTask): BoardTask {
  return {
    key: view.key,
    status: view.status,
    assignees: view.assignments.map((a) => ({ id: a.participant_id, state: a.state })),
    reports: [],
    author: view.author_id,
    title: view.title,
    description: view.description ?? '',
    note: latestNote(view.assignments),
    version: view.version,
    assignments: view.assignments,
    history: view.history,
  }
}

// boardTasksOf — the board's tasks: the typed ones when the run keeps them, else the legacy "task/<slug>" entries.
export function boardTasksOf(board: MeshBoardSnapshot, participants: MeshParticipant[]): BoardTask[] {
  return board.typedTasks ? board.tasks.map(taskFromView) : boardTasks(board.entries, participants)
}

// boardTaskCountOf — the Board tab's badge for either mode.
export function boardTaskCountOf(board: MeshBoardSnapshot): number {
  return board.typedTasks ? board.tasks.length : boardTaskCount(board.entries)
}

// boardTopLevelOf — the entries Team shows on their own. With typed tasks, the "task/" keys are the tasks' storage
// and are not entries to show, so only the facts remain.
export function boardTopLevelOf(board: Pick<MeshBoardSnapshot, 'entries' | 'typedTasks'>): MeshBoardEntry[] {
  const entries = board.typedTasks ? board.entries.filter((e) => !e.key.startsWith(TASK_PREFIX)) : board.entries
  return boardTopLevel(entries)
}
