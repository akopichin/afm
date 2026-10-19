// agent-result — распознаёт тело сообщения, в которое агент по ошибке вставил свой JSON-контракт результата
// ({"schema_version":1,"kind":"result","verdict":…,"summary":…}) вместо обычного текста. Такое сообщение
// показывается как summary плюс вердикт, а не как сырой JSON.

export type AgentResultBody = {
  // verdict — pass | needs_changes | inconclusive (или иное значение, которое прислал агент).
  verdict: string
  summary: string
}

// parseAgentResultBody — результат, если body целиком является объектом результата, иначе null.
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
  if (v.schema_version === undefined || v.kind !== 'result' || typeof v.summary !== 'string') return null
  return { verdict: typeof v.verdict === 'string' ? v.verdict : '', summary: v.summary }
}

// messageText — what a message says, in words: the summary of a pasted result object, else the body itself.
export function messageText(body: string): string {
  return parseAgentResultBody(body)?.summary ?? body
}

// verdictLabel — the verdict in words for its chip.
export function verdictLabel(verdict: string): string {
  switch (verdict) {
    case 'pass':
      return 'Pass'
    case 'needs_changes':
      return 'Needs changes'
    case 'inconclusive':
      return 'Inconclusive'
    default:
      return verdict
  }
}
