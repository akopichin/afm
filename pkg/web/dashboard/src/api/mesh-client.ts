// Типизированный клиент к /api/mesh/* (Agent Mesh Phase 7, T7.1/T7.3) — Team view, и далее Feed composer's
// @-mention/attach-preview (T7.4/T7.5) reuse the same functions. Same per-module convention as
// side-agent-client.ts/files-client.ts: a local Error class + local readErrorCode/isRecord, not a shared
// mega-helper (run-client.ts's own jsonRequest/postJson are private to that module, not exported for reuse).
//
// Every wire type below mirrors pkg/agentcomm/types.go and pkg/server/mesh.go's JSON tags field-for-field —
// ground truth is the Go struct tags, not this comment.

export class MeshApiError extends Error {
  status: number
  code: string | undefined

  constructor(url: string, status: number, code: string | undefined) {
    super(`${url} -> ${status}${code ? ` (${code})` : ''}`)
    this.name = 'MeshApiError'
    this.status = status
    this.code = code
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

async function readErrorCode(response: Response): Promise<string | undefined> {
  try {
    const body: unknown = await response.json()
    return isRecord(body) && typeof body.error === 'string' ? body.error : undefined
  } catch {
    return undefined
  }
}

async function meshGet<T>(url: string): Promise<T> {
  const response = await fetch(url)
  if (!response.ok) throw new MeshApiError(url, response.status, await readErrorCode(response))
  return response.json() as Promise<T>
}

async function meshPost<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw new MeshApiError(url, response.status, await readErrorCode(response))
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
}

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
  seq: number
  created_at: string
}

// MeshMessagePage mirrors server.MeshMessagePage.
export type MeshMessagePage = {
  messages: MeshMessage[]
  next_cursor: number
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

// MeshSendReceipt mirrors server.MeshSendReceipt.
export type MeshSendReceipt = {
  message_id: string
  seq: number
}

// MeshAttachAndSendResult mirrors server.MeshAttachAndSendResult.
export type MeshAttachAndSendResult = {
  task_id: string
  participant_id: string
  thread_id: string
  message_id?: string
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
export async function listMeshThreads(): Promise<{ threads: MeshThread[] }> {
  const body = await meshGet<{ threads?: MeshThread[] | null }>('/api/mesh/threads')
  return { threads: body.threads ?? [] }
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

// getMeshBoard reads the shared board — GET /api/mesh/board. An empty/omitted key returns every entry
// (orchestrator.ReadBoard's own "empty Key returns every entry" contract).
export async function getMeshBoard(key = ''): Promise<{ entries: MeshBoardEntry[] }> {
  const url = key === '' ? '/api/mesh/board' : `/api/mesh/board?key=${encodeURIComponent(key)}`
  const body = await meshGet<{ entries?: MeshBoardEntry[] | null }>(url)
  return { entries: body.entries ?? [] }
}

export type OpenMeshThreadRequest = {
  requestId: string
  title: string
  scope: 'stage' | 'run'
  memberIds: string[]
  maxMessages?: number
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
  })
}
