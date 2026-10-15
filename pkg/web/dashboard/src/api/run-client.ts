import type { ReviewNote } from '../types'
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

  constructor(url: string, status: number, code: string | undefined) {
    super(`${url} -> ${status}${code ? ` (${code})` : ''}`)
    this.status = status
    this.code = code
  }
}

async function readErrorCode(response: Response): Promise<string | undefined> {
  try {
    const body: unknown = await response.json()
    return isRecord(body) && typeof body.error === 'string' ? body.error : undefined
  } catch {
    return undefined
  }
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
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
// В отличие от reviseStage не трогает FSM: стадия остаётся pending.
export async function setStageNote(stageId: string, note: string): Promise<void> {
  await postJson(stageUrl(stageId, 'note'), { note })
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
  }
}

// --- Collaboration (helper assistants) — GET only, T3.9/T3.10's own poll. Mutations (attach/pause/
// continue/cancel) are out of scope here (not part of T3.9/T3.10's exact acceptance criteria) — adding them
// now would be speculative surface with no test driving it.

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
}

// listStageAssistants reads one stage's task tree (flat, parent-linked) — GET /api/stages/{id}/assistants.
// A 404 (capability off) surfaces as a thrown FlowApiError like every other stage sub-route; callers decide
// how to degrade (useStageAssistants treats any error as "no data yet", not a hard failure).
export async function listStageAssistants(stageId: string): Promise<{ tasks: AssistantTaskWire[] }> {
  const url = stageUrl(stageId, 'assistants')
  const response = await fetch(url)
  if (!response.ok) {
    throw new FlowApiError(url, response.status, await readErrorCode(response))
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
  }
  const body = (await response.json()) as { events?: AssistantEventWire[] | null; next_cursor?: number; has_more?: boolean }
  return { events: body.events ?? [], nextCursor: body.next_cursor ?? cursor, hasMore: body.has_more === true }
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
  created_at: string
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
    throw new FlowApiError(url, response.status, await readErrorCode(response))
  }
  const body = (await response.json()) as { messages?: DiscussionMessageWire[] | null; next_cursor?: number; has_more?: boolean }
  return { messages: body.messages ?? [], nextCursor: body.next_cursor ?? cursor, hasMore: body.has_more === true }
}

// postStageDiscussionMessage appends one human message to a stage's discussion — POST
// /api/stages/{id}/discussion. This NEVER triggers Revise/affects the stage's status on its own.
export async function postStageDiscussionMessage(stageId: string, requestId: string, body: string): Promise<{ messageId: string }> {
  const resp = await jsonRequest<{ message_id: string }>(stageUrl(stageId, 'discussion'), 'POST', { request_id: requestId, body })
  return { messageId: resp.message_id }
}

// ProposalWire mirrors pkg/server/proposals.go's ProposalView.
export type ProposalWire = {
  id: string
  stage_id: string
  task_id?: string
  type: string
  title: string
  rationale?: string
  authors?: string[]
  source_message_ids?: string[]
  source_finding_ids?: string[]
  expected_stage_generation: number
  status: string
  created_at: string
}

// listStageProposals reads a stage's proposals — GET /api/stages/{id}/proposals.
export async function listStageProposals(stageId: string): Promise<{ proposals: ProposalWire[] }> {
  const url = stageUrl(stageId, 'proposals')
  const response = await fetch(url)
  if (!response.ok) {
    throw new FlowApiError(url, response.status, await readErrorCode(response))
  }
  const body = (await response.json()) as { proposals?: ProposalWire[] | null }
  return { proposals: body.proposals ?? [] }
}

// acceptProposal durably accepts a proposal and starts its delivery — POST /api/proposals/{id}/accept.
// requestId is the idempotency key: resending the SAME id after a timeout/double-click is always safe.
export async function acceptProposal(proposalId: string, requestId: string): Promise<void> {
  await jsonRequest<{ status: string }>(`/api/proposals/${encodeURIComponent(proposalId)}/accept`, 'POST', { request_id: requestId })
}

// dismissProposal durably dismisses a pending proposal — POST /api/proposals/{id}/dismiss. Never touches the
// main agent in any way.
export async function dismissProposal(proposalId: string, requestId: string, reason = ''): Promise<void> {
  await jsonRequest<{ status: string }>(`/api/proposals/${encodeURIComponent(proposalId)}/dismiss`, 'POST', {
    request_id: requestId,
    reason,
  })
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
