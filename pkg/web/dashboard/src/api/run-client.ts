import type { ReviewNote } from '../types'

// Мутирующие команды дашборда к afm-серверу (approve/revise/retry/dialog).
// Вынесено из PlanPanel/DialogChannel/App, которые раньше дублировали один и
// тот же postJson-паттерн по месту использования.
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

// FlowApiError — как обычный Error из postJson выше, но для ручек /api/flow/*:
// сервер на ошибке отдаёт машиночитаемый {"error":"<code>"} (см.
// writeFlowError в pkg/server/flow_handlers.go) — код кладём в .code, чтобы UI
// мог различить конкретные конфликты (stale_content/rev_conflict/
// flow_not_paused/...), а не только парсить текст сообщения.
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

// jsonRequest — общий помощник для /api/flow/* вызовов, возвращающих тело
// ответа (в отличие от postJson выше, который используется там, где ответ не
// нужен вызывающему коду). method параметризован, т.к. updateNote — PUT, а
// остальные — POST.
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

// triggerStageButton нажимает предопределённую в flow.yaml кнопку стадии: клиент
// шлёт только имя (= label = ключ в YAML), сервер сам достаёт промпт из флоу и
// доставляет его живому агенту через Revise (тот же путь, что и reviseStage).
export async function triggerStageButton(stageId: string, name: string): Promise<void> {
  await postJson(stageUrl(stageId, 'button'), { name })
}

// setStageNote прикрепляет (или, при пустом note, удаляет) заметку к ещё не
// стартовавшей стадии — доедет до агента в контексте при старте. В отличие от
// reviseStage не трогает FSM: стадия остаётся pending.
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

// answerDialog бросает типизированный FlowApiError (а не Error из postJson) —
// DialogChannel различает конкретный код answer_out_of_order (стадия успела
// переключиться на другой вопрос, ответ отправлен на устаревший id) от прочих
// ошибок, чтобы решить, стоит ли резинхронизировать панель.
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

// pauseFlow ставит на паузу все активные стадии флоу для сбора ревью-заметок
// (см. pkg/orchestrator's PauseFlow) — возвращает id стадий, которые реально
// поставлены на паузу этим вызовом (уже приостановленные стадии в список не
// попадают).
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
  // Defensive normalization: a freshly created store serializes an empty note
  // list as `"notes": null` (Go encodes a nil slice as null). Callers iterate
  // `notes` directly (groupByFile), so coerce null/undefined to [] here — the
  // single point where untyped JSON becomes a typed ReviewNote[].
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

// addNote создаёт (или, для того же root/path/line, заменяет) ревью-заметку.
// content_sha — хэш содержимого файла на момент постановки заметки, сервер
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
