// Типизированный клиент к GET /api/stages/{id}/verify/{verificationId}/report — единственный способ
// получить полный текст отчёта одной AI-verify-проверки.

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
