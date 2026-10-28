// Activity of one helper task: the parsed messages and tool calls it produced while running. Source: live
// `assistant_activity` WebSocket notices and GET /api/assistants/{id}/activity (stagefiles.ActivityData,
// ActivityEntry and ActivityPage in pkg/orchestrator/stagefiles); both carry the same fields, so one parser serves both.

// Tool name the server uses for a plain assistant message (rendered as Markdown, not as a tool row).
export const ACTIVITY_TEXT_TOOL = 'text'

export type AssistantActivityEntry = {
  eventId: string
  taskId: string
  participantId: string
  turn: number
  occurredAt: string
  tool: string
  detail: string
  truncated: boolean
  // Offset just after the entry's line in notices.jsonl; only history entries have one. Not a task event Seq.
  cursor: string | undefined
}

// AssistantActivityPage — one bounded page of saved activity. `after` is the cursor for the next forward
// request, `before` the one for the next older page. Cursors are opaque decimal byte offsets, kept as strings.
export type AssistantActivityPage = {
  entries: AssistantActivityEntry[]
  after: string
  before: string
  hasMore: boolean
  hasOlder: boolean
}

// parseCursor accepts the string the API documents and a bare number alike.
function parseCursor(value: unknown): string | undefined {
  if (typeof value === 'string' && /^\d+$/.test(value)) return value
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) return String(value)
  return undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

// parseAssistantActivity drops a payload without the identity fields instead of guessing them: a row without
// event_id could not be merged between history and live.
export function parseAssistantActivity(raw: unknown): AssistantActivityEntry | null {
  if (!isRecord(raw)) return null
  const { event_id: eventId, task_id: taskId, tool, occurred_at: occurredAt } = raw
  if (typeof eventId !== 'string' || eventId === '' || typeof taskId !== 'string' || taskId === '') return null
  if (typeof tool !== 'string' || typeof occurredAt !== 'string') return null
  return {
    eventId,
    taskId,
    participantId: typeof raw.participant_id === 'string' ? raw.participant_id : '',
    turn: typeof raw.turn === 'number' ? raw.turn : 0,
    occurredAt,
    tool,
    detail: typeof raw.detail === 'string' ? raw.detail : '',
    truncated: raw.truncated === true,
    cursor: parseCursor(raw.cursor),
  }
}

export function parseAssistantActivityPage(raw: unknown): AssistantActivityPage {
  const body = isRecord(raw) ? raw : {}
  const rows: unknown[] = Array.isArray(body.entries) ? body.entries : []
  const entries = rows.flatMap((row) => parseAssistantActivity(row) ?? [])
  return {
    entries,
    after: parseCursor(body.after) ?? '0',
    before: parseCursor(body.before) ?? '0',
    hasMore: body.has_more === true,
    hasOlder: body.has_older === true,
  }
}
