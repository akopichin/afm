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

function stageUrl(stageId: string, action: string): string {
  return `/api/stages/${encodeURIComponent(stageId)}/${action}`
}

export async function approveStage(stageId: string): Promise<void> {
  await postJson(stageUrl(stageId, 'approve'), null)
}

export async function reviseStage(stageId: string, feedback: string): Promise<void> {
  await postJson(stageUrl(stageId, 'revise'), { feedback })
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

export async function answerDialog(
  stageId: string,
  phase: string,
  id: string,
  answer: string,
  fromOptions: boolean,
): Promise<void> {
  await postJson(stageUrl(stageId, 'dialog/answer'), { id, phase, answer, from_options: fromOptions })
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
