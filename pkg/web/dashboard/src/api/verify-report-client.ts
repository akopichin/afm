// Типизированный клиент к GET /api/stages/{id}/verify/{verificationId}/report
// (V5b.3) — единственный способ получить полный текст отчёта одной
// AI-verify-проверки. Адресация ОПАЧНАЯ: stageId + verificationId, оба уже
// известны фронту из события verify_result (см. feed-view-model.ts) — сюда
// НИКОГДА не передаётся клиентский filesystem-путь (report_path из payload
// события используется только как булев сигнал "отчёт есть", не как URL).

export class VerifyReportError extends Error {
  constructor(public status: number) {
    super(`verify report request failed: ${status}`)
    this.name = 'VerifyReportError'
  }
}

export async function getVerifyReport(stageId: string, verificationId: string): Promise<string> {
  const url = `/api/stages/${encodeURIComponent(stageId)}/verify/${encodeURIComponent(verificationId)}/report`
  const response = await fetch(url)
  if (!response.ok) throw new VerifyReportError(response.status)

  const data = (await response.json()) as { content?: unknown }
  return typeof data.content === 'string' ? data.content : ''
}
