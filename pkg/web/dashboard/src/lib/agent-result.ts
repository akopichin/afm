// agent-result — распознаёт тело сообщения, в которое агент по ошибке вставил свой JSON-контракт результата
// ({"schema_version":1,"kind":"result","verdict":…,"summary":…}) вместо обычного текста. Такое сообщение
// показывается как summary плюс вердикт, а не как сырой JSON.

export type AgentResultBody = {
  // verdict — pass | needs_changes | inconclusive.
  verdict: string
  summary: string
}

const VERDICTS = ['pass', 'needs_changes', 'inconclusive']

// parseAgentResultBody — the result, if body is exactly a v1 result object (schema_version 1, kind "result",
// a known verdict, a string summary), else null.
export function parseAgentResultBody(body: string): AgentResultBody | null {
  const text = body.trim()
  if (!text.startsWith('{')) return null
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return null
  }
  if (typeof value !== 'object' || value === null) return null
  const v = value as Record<string, unknown>
  if (v.schema_version !== 1 || v.kind !== 'result' || typeof v.verdict !== 'string' || !VERDICTS.includes(v.verdict)) return null
  if (typeof v.summary !== 'string') return null
  return { verdict: v.verdict, summary: v.summary }
}

// messageText — what a message says, in words: for an agent's pasted result object its summary, else the
// body itself. A human's text is never reinterpreted.
export function messageText(body: string, fromAgent: boolean): string {
  return (fromAgent ? parseAgentResultBody(body)?.summary : undefined) ?? body
}

// verdictLabel — the verdict in words for its chip.
export function verdictLabel(verdict: string): string {
  switch (verdict) {
    case 'pass':
      return 'Pass'
    case 'needs_changes':
      return 'Needs changes'
    default:
      return 'Inconclusive'
  }
}
