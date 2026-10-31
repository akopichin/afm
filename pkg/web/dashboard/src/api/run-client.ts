import type { ReviewNote } from '../types'
import { parseAssistantActivityPage, type AssistantActivityPage } from '../types/assistant-activity'
import { toAssistantProfile, type AssistantProfile, type AssistantProfileWire } from '../types/assistant-profile'

// Мутирующие команды дашборда к afm-серверу. Вынесено из PlanPanel/DialogChannel/App, которые раньше
// дублировали один и тот же postJson-паттерн по месту использования.
async function postJson(url: string, body: unknown): Promise<void> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })

  if (!response.ok) {
    throw new Error(`POST ${url} -> ${response.status}`)
  }
}

// FlowApiError сохраняет машинный код /api/flow/* в .code для различения конфликтов в UI.
export class FlowApiError extends Error {
  code: string | undefined
  status: number

  // reason — the server's explanation of a rejected request (the error body's message, e.g. why a mention cannot
  // read a post); it becomes the error's message, so a form shows it as it is. Without one the message names the
  // request, its status and code.
  constructor(url: string, status: number, code: string | undefined, reason?: string) {
    super(reason !== undefined && reason !== '' ? reason : `${url} -> ${status}${code ? ` (${code})` : ''}`)
    this.status = status
    this.code = code
  }
}

// flowApiError — the FlowApiError of a failed response: its error code and, when the server gave one, its reason.
async function flowApiError(url: string, response: Response): Promise<FlowApiError> {
  try {
    const body: unknown = await response.json()
    if (isRecord(body)) {
      return new FlowApiError(url, response.status, typeof body.error === 'string' ? body.error : undefined, typeof body.message === 'string' ? body.message : undefined)
    }
  } catch {
    /* no JSON body */
  }
  return new FlowApiError(url, response.status, undefined)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

// jsonRequest — общий помощник для /api/flow/* вызовов, возвращающих тело ответа. method параметризован,
// т.к.
async function jsonRequest<T>(url: string, method: 'POST' | 'PUT', body: unknown): Promise<T> {
  const response = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === null ? null : JSON.stringify(body),
  })

  if (!response.ok) {
    throw await flowApiError(url, response)
  }

  return response.json() as Promise<T>
}

function stageUrl(stageId: string, action: string): string {
  return `/api/stages/${encodeURIComponent(stageId)}/${action}`
}

export async function approveStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'approve'), null)
}

export async function reviseStage(stageId: string, feedback: string): Promise<void> {
  await postJson(stageUrl(stageId, 'revise'), { feedback })
}

// triggerStageButton нажимает предопределённую в flow.yaml кнопку стадии: клиент шлёт только имя, сервер
// сам достаёт промпт из флоу и доставляет его живому агенту через Revise.
export async function triggerStageButton(stageId: string, name: string): Promise<void> {
  await postJson(stageUrl(stageId, 'button'), { name })
}

// setStageNote прикрепляет заметку к ещё не стартовавшей стадии — доедет до агента в контексте при старте.
// В отличие от reviseStage не трогает FSM: стадия остаётся pending. Ошибка — FlowApiError: UI различает 400 и 409 по
// статусу (тело ответа — plain text).
export async function setStageNote(stageId: string, note: string): Promise<void> {
  await jsonRequest(stageUrl(stageId, 'note'), 'POST', { note })
}

export async function retryStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'retry'), null)
}

export async function pauseStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'pause'), null)
}

export async function continueStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'continue'), null)
}

export async function retryHookStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'retry-hook'), null)
}

export async function skipHookStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'skip-hook'), null)
}

// answerDialog бросает типизированный FlowApiError — DialogChannel различает конкретный код
// answer_out_of_order от прочих ошибок, чтобы решить, стоит ли резинхронизировать панель.
export async function answerDialog(
  stageId: string,
  phase: string,
  id: string,
  answer: string,
  fromOptions: boolean,
): Promise<void> {
  const url = stageUrl(stageId, 'dialog/answer')
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, phase, answer, from_options: fromOptions }),
  })
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
}

export async function cancelDialog(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'dialog/cancel'), null)
}

export class AttachmentUploadError extends Error {
  status: number

  constructor(status: number) {
    super(`upload attachment failed with status ${status}`)
    this.status = status
  }
}

// pauseFlow ставит на паузу все активные стадии флоу для сбора ревью-заметок — возвращает id стадий,
// которые реально поставлены на паузу этим вызовом.
export async function pauseFlow(): Promise<{ paused_stages: string[] }> {
  return jsonRequest('/api/flow/pause', 'POST', null)
}

// injectNotes закрывает раунд ревью: собранные заметки доставляются в
// указанную стадию, и флоу возобновляется (see InjectNotesAndResume).
export async function injectNotes(stageId: string): Promise<void> {
  await postJson('/api/flow/notes/inject', { stage_id: stageId })
}

// cancelNotes закрывает раунд ревью без доставки заметок — они остаются в
// сторе, флоу просто возобновляется (see CancelNotesAndResume).
export async function cancelNotes(): Promise<void> {
  await postJson('/api/flow/notes/cancel', null)
}

// listNotes читает текущий список ревью-заметок вместе с ревизией (rev) —
// нужна для optimistic-concurrency при последующих update/delete.
export async function listNotes(): Promise<{ rev: number; notes: ReviewNote[] }> {
  const url = '/api/flow/notes'
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { rev?: number; notes?: ReviewNote[] | null }
  // Defensive normalization: a freshly created store serializes an empty note list as `"notes": null`.
  return { rev: body.rev ?? 0, notes: body.notes ?? [] }
}

export type AddNoteRequest = {
  root: string
  path: string
  line: number | null
  text: string
  content_sha: string
  expected_rev: number
}

// addNote создаёт ревью-заметку. content_sha — хэш содержимого файла на момент постановки заметки, сервер
// сверяет его перед принятием (см. ErrStaleContent/stale_content).
export async function addNote(body: AddNoteRequest): Promise<{ note: ReviewNote; rev: number }> {
  return jsonRequest('/api/flow/notes', 'POST', body)
}

// updateNote правит текст существующей заметки. expectedRev — оптимистическая
// блокировка: расхождение с текущей ревизией стора вернёт rev_conflict.
export async function updateNote(id: string, text: string, expectedRev: number): Promise<{ rev: number }> {
  return jsonRequest(`/api/flow/notes/${encodeURIComponent(id)}`, 'PUT', { text, expected_rev: expectedRev })
}

// deleteNote удаляет заметку по id. expectedRev идёт в query-строку — у
// DELETE по конвенции нет тела запроса (см. handleNotesDelete).
export async function deleteNote(id: string, expectedRev: number): Promise<void> {
  const url = `/api/flow/notes/${encodeURIComponent(id)}?expected_rev=${expectedRev}`
  const response = await fetch(url, { method: 'DELETE' })
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
}

// --- Collaboration (helper assistants). Reads are each hook's own poll; the mutations below (attach a
// helper to a stage, waive/cancel/retry a task) back the dashboard's helper controls.

// AttachHelperRequest mirrors the POST /api/stages/{id}/assistants body (pkg/server/assistants.go's
// handleAttach). request_id is generated client-side for idempotency, same convention as addStageRequestId.
export type AttachHelperRequest = {
  profileId: string
  title: string
  instruction: string
  mode: 'advisory' | 'required'
  trigger: 'one_shot' | 'checkpoints' | 'on_completion'
}

// Task-level lifecycle mutations — POST /api/assistants/{id}/{action}. Distinct from the stage-level
// pauseStage/continueStage above (those act on a whole stage's own agent). Errors surface as FlowApiError so
// the task view can show the server's reason (e.g. a stale status).
function assistantTaskUrl(taskId: string, action: string): string {
  return `/api/assistants/${encodeURIComponent(taskId)}/${action}`
}

export async function pauseAssistantTask(taskId: string): Promise<void> {
  await jsonRequest(assistantTaskUrl(taskId, 'pause'), 'POST', null)
}

export async function continueAssistantTask(taskId: string): Promise<void> {
  await jsonRequest(assistantTaskUrl(taskId, 'continue'), 'POST', null)
}

export async function cancelAssistantTask(taskId: string): Promise<void> {
  await jsonRequest(assistantTaskUrl(taskId, 'cancel'), 'POST', null)
}

// waiveAssistantTask records a human decision to accept the risk of a required task never closing its gate.
// The server requires a non-empty reason (400 reason_required otherwise).
export async function waiveAssistantTask(taskId: string, reason: string): Promise<void> {
  await jsonRequest(assistantTaskUrl(taskId, 'waive'), 'POST', { reason })
}

// attachAssistant attaches a helper task to a stage — POST /api/stages/{id}/assistants, returns the new
// task id. Errors surface as FlowApiError (e.g. 400 for a terminal/script stage or unknown profile) so the
// dialog can show the server's reason.
export async function attachAssistant(stageId: string, req: AttachHelperRequest): Promise<{ taskId: string }> {
  const body = await jsonRequest<{ task_id: string }>(stageUrl(stageId, 'assistants'), 'POST', {
    request_id: `attach-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    profile_id: req.profileId,
    title: req.title,
    instruction: req.instruction,
    mode: req.mode,
    trigger: req.trigger,
  })
  return { taskId: body.task_id }
}

export type AssistantTaskWire = {
  id: string
  parent_task_id?: string
  profile_id: string
  title?: string
  mode: string
  trigger: string
  status: string
  status_reason?: string
  // depth — 1 for a top-level task, parent.depth+1 for each delegated level (T4a.8's breadcrumb).
  depth?: number
  // watch_passes/watch_last_checkpoint_seq (T5.6) — present only for a checkpoints/on_completion watcher task.
  watch_passes?: number
  watch_last_checkpoint_seq?: number
  // blocks_root_completion (Этап 7, T7.5/T7.9) — whether an unresolved instance of this task would block the
  // ROOT STAGE's own completion (vs. only its immediate parent helper). See
  // pkg/server/assistants.go's AssistantTaskView.BlocksRootCompletion.
  blocks_root_completion?: boolean
  // waived/waiver_reason (Этап 7, T7.6/T7.9) — a human has explicitly accepted the risk of this required task
  // never closing its own gate. Once waived, the dashboard must stop rendering it as "blocks stage"/"blocks
  // helper X" even though blocks_root_completion stays structurally true.
  waived?: boolean
  waiver_reason?: string
  // quota — durable usage-limit park of a queued task (same shape as StageView.quota); parsed by
  // parseStageQuota, which validates every field.
  quota?: unknown
}

// listStageAssistants reads one stage's task tree (flat, parent-linked) — GET /api/stages/{id}/assistants.
// A 404 (capability off) surfaces as a thrown FlowApiError like every other stage sub-route; callers decide
// how to degrade (useStageAssistants treats any error as "no data yet", not a hard failure).
export async function listStageAssistants(stageId: string): Promise<{ tasks: AssistantTaskWire[] }> {
  const url = stageUrl(stageId, 'assistants')
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { tasks?: AssistantTaskWire[] | null }
  return { tasks: body.tasks ?? [] }
}

// listAssistantProfiles reads the configured helper roles and the effective delegation depth — GET
// /api/assistant-profiles (404 when the collaboration capability is off; callers degrade to an empty list,
// same posture as listStageAssistants).
export async function listAssistantProfiles(): Promise<{ profiles: AssistantProfile[]; maxDepth: number; maxDepthSource: string }> {
  const url = '/api/assistant-profiles'
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { profiles?: AssistantProfileWire[] | null; max_depth?: number; max_depth_source?: string }
  return {
    profiles: (body.profiles ?? []).map(toAssistantProfile),
    maxDepth: body.max_depth ?? 0,
    maxDepthSource: body.max_depth_source ?? 'default',
  }
}

// confirmActionDraft executes one action draft a side-agent turn proposed (T4.5/T4.6) — POST
// /api/side-agent/drafts/confirm. The server reads the draft's actual content from durable history by
// (turnId, index); this call sends nothing else. Repeat calls for the SAME (turnId, index) are idempotent
// (same operation id server-side) — resolve to the same taskId, never a second task.
export async function confirmActionDraft(turnId: string, index: number): Promise<{ taskId: string }> {
  const body = await jsonRequest<{ task_id: string }>('/api/side-agent/drafts/confirm', 'POST', { turn_id: turnId, index })
  return { taskId: body.task_id }
}

export type AssistantEventWire = { seq: number; type: string; data?: unknown }

// getAssistantTaskEvents reads one page of a task's durable event history, strictly after cursor — GET
// /api/assistants/{id}/events.
export async function getAssistantTaskEvents(
  taskId: string,
  cursor: number,
): Promise<{ events: AssistantEventWire[]; nextCursor: number; hasMore: boolean }> {
  const url = `/api/assistants/${encodeURIComponent(taskId)}/events?cursor=${cursor}`
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { events?: AssistantEventWire[] | null; next_cursor?: number; has_more?: boolean }
  return { events: body.events ?? [], nextCursor: body.next_cursor ?? cursor, hasMore: body.has_more === true }
}

// ActivityQuery picks one page of a helper's saved activity: no cursor is the tail, `before` pages older and
// `after` pages forward. The server rejects both at once.
export type ActivityQuery = { limit?: number; before?: string; after?: string }

// getAssistantActivity reads saved helper activity — GET /api/assistants/{id}/activity. Failures throw a
// FlowApiError: 404 is "no activity available" (unknown task, reader off), 400 an invalid cursor.
export async function getAssistantActivity(taskId: string, query: ActivityQuery): Promise<AssistantActivityPage> {
  const params = new URLSearchParams()
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.before !== undefined) params.set('before', query.before)
  if (query.after !== undefined) params.set('after', query.after)
  const queryString = params.toString()
  const suffix = queryString !== '' ? `?${queryString}` : ''
  const url = `/api/assistants/${encodeURIComponent(taskId)}/activity${suffix}`
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  return parseAssistantActivityPage(await response.json())
}

// getAssistantTaskRaw reads the raw text a helper task's final turn produced — GET /api/assistants/{id}/raw.
// Only meaningful for a failed task (protocol_error): the dashboard fetches this lazily, on expand, never on
// mount — see AssistantTaskView's raw-answer <details>.
export async function getAssistantTaskRaw(taskId: string): Promise<string> {
  const url = `/api/assistants/${encodeURIComponent(taskId)}/raw`
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { content?: string }
  return body.content ?? ''
}

// DiscussionMessageWire mirrors pkg/server/discussion.go's DiscussionMessageView.
export type DiscussionMessageWire = {
  id: string
  author_kind: string
  author_id?: string
  role?: string
  executor?: string
  round: number
  parent_message_id?: string
  context_version?: string
  evidence?: string[]
  body?: string
  mentions?: string[]
  interrupt?: boolean
  unanswered?: boolean
  auto_result?: boolean
  created_at: string
  // seq — the journal seq of the post (the order mesh messages carry too); absent from an older server cache.
  seq?: number
}

// listStageDiscussion reads one page of a stage's discussion log — GET /api/stages/{id}/discussion. A 404
// (capability off) surfaces as a thrown FlowApiError, same convention as listStageAssistants.
export async function listStageDiscussion(
  stageId: string,
  cursor: number,
  limit = 50,
): Promise<{ messages: DiscussionMessageWire[]; nextCursor: number; hasMore: boolean }> {
  const url = `${stageUrl(stageId, 'discussion')}?cursor=${cursor}&limit=${limit}`
  const response = await fetch(url)
  if (!response.ok) {
    throw await flowApiError(url, response)
  }
  const body = (await response.json()) as { messages?: DiscussionMessageWire[] | null; next_cursor?: number; has_more?: boolean }
  return { messages: body.messages ?? [], nextCursor: body.next_cursor ?? cursor, hasMore: body.has_more === true }
}

// DiscussionPost — one human post: its text, the mesh participants pulled in with `@` (they receive it besides
// the stage agent), and whether to interrupt the running stage agent so it answers now.
export type DiscussionPost = {
  body: string
  mentions: string[]
  interrupt: boolean
}

// postStageDiscussionMessage appends one human message to a stage's discussion — POST
// /api/stages/{id}/discussion. It never revises the stage's work; with `interrupt` a RUNNING stage agent stops
// its turn to answer the post and then continues (no effect for any other status).
export async function postStageDiscussionMessage(stageId: string, requestId: string, post: DiscussionPost): Promise<{ messageId: string }> {
  const resp = await jsonRequest<{ message_id: string }>(stageUrl(stageId, 'discussion'), 'POST', {
    request_id: requestId,
    body: post.body,
    mentions: post.mentions,
    interrupt: post.interrupt,
  })
  return { messageId: resp.message_id }
}

// --- Этап 9 (T9.6): dynamic "add a regular stage" dialog — POST /api/run/graph/{preview|commit}. Same wire
// shape for both calls (addStageRequest, pkg/server/graph.go); preview never mutates anything, commit is the
// idempotent write (request_id required).

export type AddStageRequest = {
  kind: 'insert_before' | 'append'
  id: string
  name?: string
  command: string
  prompt: string
  // target_id — insert_before only.
  target_id?: string
  // depends_on — append only, an explicit override of the default (current sinks).
  depends_on?: string[]
  // expected_revision — 0 (or omitted) means "don't check" (graph_mutate.go's AddStageDraft.ExpectedRevision).
  expected_revision?: number
  // request_id — required for /commit (idempotency key), ignored by /preview.
  request_id?: string
}

// AddStagePreviewView mirrors pkg/server/graph.go's stagePreviewView. depends_on is the NEW stage's own
// dependencies; extra_deps maps an EXISTING stage id to the dependency ids newly added to it (insert_before
// only). order/warnings are present on /preview's response and omitted by /commit's.
export type AddStagePreviewView = {
  id: string
  name?: string
  command: string
  prompt: string
  depends_on: string[]
  extra_deps?: Record<string, string[]>
  order?: string[]
  warnings?: string[]
}

export type AddStagePreviewResponse = {
  stage: AddStagePreviewView
  graph_revision: number
}

export type AddStageCommitResponse = {
  stage: AddStagePreviewView
  graph_revision: number
}

// previewAddStage validates req against the CURRENT registry and returns the resolved dependency diff WITHOUT
// mutating anything — POST /api/run/graph/preview. A rejection surfaces as FlowApiError with .code set to one
// of graph_mutate.go's documented error codes (stale_revision, invalid_status, target_not_found, ...).
export async function previewAddStage(req: AddStageRequest): Promise<AddStagePreviewResponse> {
  return jsonRequest('/api/run/graph/preview', 'POST', req)
}

// commitAddStage durably adds the stage and wakes the scheduler — POST /api/run/graph/commit. req.request_id
// is the idempotency key: resending the SAME id after a timeout/double-click is always safe.
export async function commitAddStage(req: AddStageRequest): Promise<AddStageCommitResponse> {
  return jsonRequest('/api/run/graph/commit', 'POST', req)
}

export async function uploadAttachment(stageId: string, file: Blob): Promise<{ path: string }> {
  const url = stageUrl(stageId, 'attachments')
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': file.type },
    body: file,
  })

  if (!response.ok) {
    throw new AttachmentUploadError(response.status)
  }

  return (await response.json()) as { path: string }
}
