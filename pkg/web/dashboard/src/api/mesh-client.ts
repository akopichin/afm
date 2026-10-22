// Типизированный клиент к /api/mesh/* (Agent Mesh Phase 7, T7.1/T7.3) — Team view, и далее Feed composer's
// @-mention/attach-preview (T7.4/T7.5) reuse the same functions. Same per-module convention as
// side-agent-client.ts/files-client.ts: a local Error class + local apiError/isRecord, not a shared
// mega-helper (run-client.ts's own jsonRequest/postJson are private to that module, not exported for reuse).
//
// Every wire type below mirrors pkg/agentcomm/types.go and pkg/server/mesh.go's JSON tags field-for-field —
// ground truth is the Go struct tags, not this comment.

export class MeshApiError extends Error {
  status: number
  code: string | undefined

  // reason — the server's explanation of a rejected request (e.g. why a recipient cannot be written to); it
  // becomes the error's message, so a form shows it as it is.
  constructor(url: string, status: number, code: string | undefined, reason?: string) {
    super(reason !== undefined && reason !== '' ? reason : `${url} -> ${status}${code ? ` (${code})` : ''}`)
    this.name = 'MeshApiError'
    this.status = status
    this.code = code
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

// apiError — the MeshApiError of a failed response: its error code and, when the server gave one, its reason.
async function apiError(url: string, response: Response): Promise<MeshApiError> {
  try {
    const body: unknown = await response.json()
    if (isRecord(body)) {
      const code = typeof body.error === 'string' ? body.error : undefined
      const reason = typeof body.message === 'string' ? body.message : undefined
      return new MeshApiError(url, response.status, code, reason)
    }
  } catch {
    // not JSON — the status alone
  }
  return new MeshApiError(url, response.status, undefined)
}

async function meshGet<T>(url: string): Promise<T> {
  const response = await fetch(url)
  if (!response.ok) throw await apiError(url, response)
  return response.json() as Promise<T>
}

async function meshPost<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw await apiError(url, response)
  return response.json() as Promise<T>
}

// --- Wire types ---

// MeshParticipant mirrors agentcomm.Participant.
export type MeshParticipant = {
  id: string
  kind: string
  scope: string
  alias?: string
  display_name?: string
  stage_id?: string
  reachable: boolean
  unreachable_reason?: string
  // activity — what a run agent or helper is doing now (working, waiting, idle, paused, done).
  activity?: string
  // command — the agent command this participant runs (a stage's effective command, a task's frozen profile
  // command). readonly_enforcement — a run agent's or helper's read-only guarantee ("os" | "none"): for a live
  // task what the current environment gives it, for a finished one what it ran under;
  // admitted_readonly_enforcement — the guarantee recorded when the task was admitted. All server-derived on
  // every roster read; absent for the user, the Side agent and script stages.
  command?: string
  readonly_enforcement?: string
  admitted_readonly_enforcement?: string
}

export type MeshThreadBudgets = {
  max_messages?: number
}

// MeshThread mirrors agentcomm.Thread.
export type MeshThread = {
  id: string
  title?: string
  scope: string
  members: string[]
  status: string
  budgets?: MeshThreadBudgets
  // origin_stage — the stage the thread was opened from (absent for older threads and run-level ones).
  origin_stage?: string
}

// MeshThreadSummary mirrors the scoped summary in agentcomm.ThreadPage. The map is keyed by thread id.
export type MeshThreadSummary = {
  last_message?: {
    id: string
    seq: number
    created_at: string
    sender_id: string
    preview: string
  }
  last_other_message_seq: number
  title_seq: number
}

export type MeshThreadSummaries = Record<string, MeshThreadSummary>

// MeshMessage mirrors agentcomm.Message.
export type MeshMessage = {
  id: string
  thread_id: string
  sender_id: string
  recipient_ids: string[]
  kind: string
  in_reply_to?: string
  body: string
  artifact_refs?: string[]
  // auto_result — afm posted this as an agent's turn result (a passive reply that wakes nobody).
  auto_result?: boolean
  // side_agent_failed — durable undeliverable receipt for this original message, included in thread/stage reads.
  side_agent_failed?: boolean
  seq: number
  created_at: string
}

// MeshMessagePage mirrors server.MeshMessagePage.
export type MeshMessagePage = {
  messages: MeshMessage[]
  next_cursor: number
}

// MeshStageMessagePage mirrors server.MeshStageMessagePage: a stage feed's conversation messages in journal
// order, the threads they belong to, and the cursor of the next page.
export type MeshStageMessagePage = {
  messages: MeshMessage[]
  threads: MeshThread[]
  next_cursor: number
  has_more: boolean
}

// MeshBoardEntry mirrors agentcomm.BoardEntry.
export type MeshBoardEntry = {
  author_id: string
  key: string
  version: number
  status: string
  artifact_refs?: string[]
  seq: number
  ts: string
}

export type MeshBoardTaskStatus = 'todo' | 'in_progress' | 'done'
export type MeshAssignmentState = 'assigned' | 'in_progress' | 'done'

// MeshBoardTaskAssignment mirrors agentcomm.BoardTaskAssignment. ended is set only on history rows.
export type MeshBoardTaskAssignment = {
  id: string
  participant_id: string
  state: MeshAssignmentState
  assigned_by: string
  progress?: string
  result?: string
  artifact_refs?: string[]
  assigned_seq: number
  started_seq?: number
  completed_seq?: number
  ended?: 'released' | 'reopened'
  ended_by?: string
  end_reason?: string
  ended_seq?: number
}

// MeshBoardTask mirrors the server's typed board task view (agentcomm.BoardTaskView). assignments are the
// active ones; history holds released/reopened ones, oldest first.
export type MeshBoardTask = {
  key: string
  author_id: string
  coordinator_id?: string
  title: string
  description?: string
  version: number
  generation: number
  assignments: MeshBoardTaskAssignment[]
  history?: MeshBoardTaskAssignment[]
  created_seq: number
  updated_seq: number
  created_at: string
  updated_at: string
  status: MeshBoardTaskStatus
}

// MeshBoardSnapshot — GET /api/mesh/board. typedTasks says whether the run keeps typed tasks; tasks is empty
// otherwise (and the board's tasks are then the "task/<slug>" entries).
export type MeshBoardSnapshot = { entries: MeshBoardEntry[]; tasks: MeshBoardTask[]; typedTasks: boolean }

// BoardTaskReceipt mirrors orchestrator.BoardTaskReceipt — the result of POST /api/mesh/tasks.
export type BoardTaskReceipt = {
  key: string
  version: number
  status: MeshBoardTaskStatus
  assignment_id?: string
  seq: number
  message_id?: string
  thread_id?: string
  notice_skipped?: boolean
}

// MeshSendReceipt mirrors server.MeshSendReceipt.
export type MeshSendReceipt = {
  message_id: string
  seq: number
  // thread_id — the conversation the message went into (the server picks or opens it for a send without one).
  thread_id?: string
}

// MeshAttachAndSendResult mirrors server.MeshAttachAndSendResult.
export type MeshAttachAndSendResult = {
  task_id: string
  participant_id: string
  thread_id: string
  message_id?: string
  // participant — the new participant's roster entry (alias included), before the roster poll has it.
  participant?: MeshParticipant
}

// genMeshRequestId is this module's one idempotency-key generator, same shape as run-client.ts's inline
// `attach-${Date.now()}-${Math.random()...}` convention — named so a caller that must hold ONE id across
// retries (AddRunAgentDialog's shared request_id) can generate it once and reuse it, instead of a fresh id
// per click.
export function genMeshRequestId(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// listMeshParticipants reads the run's whole mesh roster — GET /api/mesh/participants. A 404 (MeshApiError
// with status 404) means the mesh capability is off (nil Service server-side); callers (useMeshRoster) treat
// that as "hide the Team tab", not a hard error.
export async function listMeshParticipants(): Promise<{ participants: MeshParticipant[] }> {
  const body = await meshGet<{ participants?: MeshParticipant[] | null }>('/api/mesh/participants')
  return { participants: body.participants ?? [] }
}

// listMeshThreads reads every thread the human dashboard operator belongs to — GET /api/mesh/threads (bare
// list form, the T7.3 dependency landed in pkg/server/mesh.go's handleMeshThreads).
export async function listMeshThreads(): Promise<{ threads: MeshThread[]; summaries: MeshThreadSummaries }> {
  const body = await meshGet<{ threads?: MeshThread[] | null; summaries?: Record<string, Partial<MeshThreadSummary>> | null }>('/api/mesh/threads')
  const summaries: MeshThreadSummaries = Object.fromEntries(
    Object.entries(body.summaries ?? {}).map(([id, summary]) => [id, {
      ...(summary.last_message === undefined ? {} : { last_message: summary.last_message }),
      last_other_message_seq: summary.last_other_message_seq ?? 0,
      title_seq: summary.title_seq ?? 0,
    }]),
  )
  return { threads: body.threads ?? [], summaries }
}

// getMeshThread reads one page of a thread's messages — GET /api/mesh/threads/{id}.
export async function getMeshThread(threadId: string, cursor = 0, limit = 0): Promise<MeshMessagePage> {
  const params = new URLSearchParams()
  if (cursor > 0) params.set('cursor', String(cursor))
  if (limit > 0) params.set('limit', String(limit))
  const qs = params.toString()
  const url = `/api/mesh/threads/${encodeURIComponent(threadId)}${qs ? `?${qs}` : ''}`
  const body = await meshGet<{ messages?: MeshMessage[] | null; next_cursor?: number }>(url)
  return { messages: body.messages ?? [], next_cursor: body.next_cursor ?? cursor }
}

// getStageMessages reads one page of the conversations a stage's feed shows — GET
// /api/mesh/stages/{id}/messages, after cursor (the seq of the last message read).
export async function getStageMessages(stageId: string, cursor: number, limit: number): Promise<MeshStageMessagePage> {
  const url = `/api/mesh/stages/${encodeURIComponent(stageId)}/messages?cursor=${cursor}&limit=${limit}`
  const body = await meshGet<Partial<Omit<MeshStageMessagePage, 'messages' | 'threads'>> & { messages?: MeshMessage[] | null; threads?: MeshThread[] | null }>(url)
  return { messages: body.messages ?? [], threads: body.threads ?? [], next_cursor: body.next_cursor ?? cursor, has_more: body.has_more === true }
}

// publishMeshBoard posts a board entry as the human operator — POST /api/mesh/board (e.g. a task for the agents).
// notify — participant ids the server messages about the new entry; sent only when non-empty.
export async function publishMeshBoard(req: { requestId: string; key: string; status: string; notify?: string[] }): Promise<MeshBoardEntry> {
  const body: Record<string, unknown> = { request_id: req.requestId, key: req.key, status: req.status }
  if (req.notify !== undefined && req.notify.length > 0) body.notify = req.notify
  return meshPost<MeshBoardEntry>('/api/mesh/board', body)
}

// getMeshBoard reads the shared board — GET /api/mesh/board. An empty/omitted key returns every entry
// (orchestrator.ReadBoard's own "empty Key returns every entry" contract).
export async function getMeshBoard(key = ''): Promise<MeshBoardSnapshot> {
  const url = key === '' ? '/api/mesh/board' : `/api/mesh/board?key=${encodeURIComponent(key)}`
  const body = await meshGet<{ entries?: MeshBoardEntry[] | null; typed_tasks?: boolean; tasks?: MeshBoardTask[] | null }>(url)
  return { entries: body.entries ?? [], tasks: body.tasks ?? [], typedTasks: body.typed_tasks === true }
}

// postMeshTask changes a typed board task as the human operator — POST /api/mesh/tasks. Only the fields the
// action uses are sent. action is the server's verb (create, update, assign, ...); requestId makes a retry idempotent.
export type PostMeshTaskRequest = {
  requestId: string
  action: string
  key: string
  expectedVersion?: number
  assignmentId?: string
  to?: string
  title?: string
  description?: string
  coordinator?: string
  text?: string
  artifactRefs?: string[]
}

export async function postMeshTask(req: PostMeshTaskRequest): Promise<BoardTaskReceipt> {
  const body: Record<string, unknown> = { request_id: req.requestId, action: req.action, key: req.key }
  if (req.expectedVersion !== undefined) body.expected_version = req.expectedVersion
  if (req.assignmentId !== undefined) body.assignment_id = req.assignmentId
  if (req.to !== undefined) body.to = req.to
  if (req.title !== undefined) body.title = req.title
  if (req.description !== undefined) body.description = req.description
  if (req.coordinator !== undefined) body.coordinator = req.coordinator
  if (req.text !== undefined) body.text = req.text
  if (req.artifactRefs !== undefined) body.artifact_refs = req.artifactRefs
  return meshPost<BoardTaskReceipt>('/api/mesh/tasks', body)
}

export type OpenMeshThreadRequest = {
  requestId: string
  title: string
  scope: 'stage' | 'run'
  memberIds: string[]
  maxMessages?: number
  // originStage — the stage whose feed opens the thread, so the conversation shows in that feed.
  originStage?: string
}

// openMeshThread opens a NEW conversation — POST /api/mesh/threads. The human operator ends up a member of
// the result even if memberIds omits it (the server adds it — cmd/afm/run_mesh.go's OpenThread), so callers
// never need to include their own id.
export async function openMeshThread(req: OpenMeshThreadRequest): Promise<MeshThread> {
  return meshPost<MeshThread>('/api/mesh/threads', {
    request_id: req.requestId,
    title: req.title,
    scope: req.scope,
    member_ids: req.memberIds,
    ...(req.maxMessages !== undefined ? { max_messages: req.maxMessages } : {}),
    ...(req.originStage !== undefined ? { origin_stage: req.originStage } : {}),
  })
}

export type MeshThreadTitleReceipt = { title: string; title_seq: number }

export async function updateMeshThreadTitle(
  threadId: string,
  req: { requestId: string; title: string; expectedTitleSeq: number },
): Promise<MeshThreadTitleReceipt> {
  return meshPost<MeshThreadTitleReceipt>(`/api/mesh/threads/${encodeURIComponent(threadId)}/topic`, {
    request_id: req.requestId,
    title: req.title,
    expected_title_seq: req.expectedTitleSeq,
  })
}

export type SendMeshMessageRequest = {
  requestId: string
  recipientIds: string[]
  kind?: string
  inReplyTo?: string
  body: string
  artifactRefs?: string[]
}

// sendMeshMessage sends into an EXISTING thread — POST /api/mesh/threads/{id}/messages. The thread id always
// comes from the URL; the server overwrites any thread_id the body might carry (handleMeshSend), so this
// never sends one in the body.
export async function sendMeshMessage(threadId: string, req: SendMeshMessageRequest): Promise<MeshSendReceipt> {
  return meshPost<MeshSendReceipt>(`/api/mesh/threads/${encodeURIComponent(threadId)}/messages`, {
    request_id: req.requestId,
    recipient_ids: req.recipientIds,
    kind: req.kind ?? 'message',
    ...(req.inReplyTo !== undefined ? { in_reply_to: req.inReplyTo } : {}),
    body: req.body,
    ...(req.artifactRefs !== undefined ? { artifact_refs: req.artifactRefs } : {}),
  })
}

export type SendMeshMessageToRequest = {
  requestId: string
  // recipientIds — participant ids or aliases (with or without "@"); the server resolves them and rejects an
  // unknown, ambiguous or unreachable one with a reason.
  recipientIds: string[]
  inReplyTo?: string
  body: string
  // originStage — the stage feed the message is written from: the conversation it goes into shows there.
  originStage?: string
}

// sendMeshMessageTo sends the human's message to a set of people without naming a thread — POST
// /api/mesh/messages. The server continues their open conversation that the origin stage's feed shows (a
// reply goes into the thread of the message it answers), or opens one in the same operation; the receipt
// names it. A retry under the same requestId returns the original receipt.
export async function sendMeshMessageTo(req: SendMeshMessageToRequest): Promise<MeshSendReceipt> {
  return meshPost<MeshSendReceipt>('/api/mesh/messages', {
    request_id: req.requestId,
    recipient_ids: req.recipientIds,
    kind: 'message',
    ...(req.inReplyTo !== undefined ? { in_reply_to: req.inReplyTo } : {}),
    body: req.body,
    ...(req.originStage !== undefined ? { origin_stage: req.originStage } : {}),
  })
}

export type AttachAndSendMeshRequest = {
  requestId: string
  profileId: string
  scope?: 'stage' | 'run'
  stageId?: string
  mode?: string
  title?: string
  instruction?: string
  firstMessage?: string
  recipientIds?: string[]
  // originStage — for a run agent, the stage whose feed it is added from (its first conversation shows there).
  originStage?: string
}

// attachAndSendMesh is the T6.2 composite both the Team view's "Add run agent" dialog (T7.3) AND the Feed's
// attach preview (T7.5) call — POST /api/mesh/attach-and-send. Neither caller composes an attach + a message
// client-side: the server does both atomically in one request.
export async function attachAndSendMesh(req: AttachAndSendMeshRequest): Promise<MeshAttachAndSendResult> {
  return meshPost<MeshAttachAndSendResult>('/api/mesh/attach-and-send', {
    request_id: req.requestId,
    profile_id: req.profileId,
    ...(req.scope !== undefined ? { scope: req.scope } : {}),
    ...(req.stageId !== undefined ? { stage_id: req.stageId } : {}),
    ...(req.mode !== undefined ? { mode: req.mode } : {}),
    ...(req.title !== undefined ? { title: req.title } : {}),
    ...(req.instruction !== undefined ? { instruction: req.instruction } : {}),
    ...(req.firstMessage !== undefined ? { first_message: req.firstMessage } : {}),
    ...(req.recipientIds !== undefined ? { recipient_ids: req.recipientIds } : {}),
    ...(req.originStage !== undefined ? { origin_stage: req.originStage } : {}),
  })
}
