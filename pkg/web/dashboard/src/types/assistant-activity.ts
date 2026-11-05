// Activity of one helper task: the parsed messages and tool calls it produced while running. Source: live
// `assistant_activity` WebSocket notices and GET /api/assistants/{id}/activity (stagefiles.ActivityData,
// ActivityEntry and ActivityPage in pkg/orchestrator/stagefiles); both carry the same fields, so one parser serves both.

// Tool name the server uses for a plain assistant message (rendered as Markdown, not as a tool row).
export const ACTIVITY_TEXT_TOOL = 'text'

export type ActivityKind = 'message' | 'tool_started' | 'tool_finished' | 'invocation_finished'
export type ActivityOutcome = 'completed' | 'failed' | 'interrupted' | 'timed_out' | 'usage_limit' | 'unknown'

const ACTIVITY_KINDS: readonly string[] = ['message', 'tool_started', 'tool_finished', 'invocation_finished']
const ACTIVITY_OUTCOMES: readonly string[] = ['completed', 'failed', 'interrupted', 'timed_out', 'usage_limit', 'unknown']

export type AssistantActivityEntry = {
  eventId: string
  taskId: string
  participantId: string
  turn: number
  occurredAt: string
  tool: string
  detail: string
  truncated: boolean
  // Fields of the updatable rows; a record without `kind` is a legacy static line.
  kind?: ActivityKind
  invocationId?: string
  itemId?: string
  revision?: number
  partial?: boolean
  final?: boolean
  outcome?: ActivityOutcome
  exitCode?: number
  startedAt?: string
  finishedAt?: string
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

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
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
  const kind = typeof raw.kind === 'string' && ACTIVITY_KINDS.includes(raw.kind) ? (raw.kind as ActivityKind) : undefined
  const outcome = typeof raw.outcome === 'string' && ACTIVITY_OUTCOMES.includes(raw.outcome) ? (raw.outcome as ActivityOutcome) : undefined
  return {
    eventId,
    taskId,
    participantId: typeof raw.participant_id === 'string' ? raw.participant_id : '',
    turn: typeof raw.turn === 'number' ? raw.turn : 0,
    occurredAt,
    tool,
    detail: typeof raw.detail === 'string' ? raw.detail : '',
    truncated: raw.truncated === true,
    kind,
    invocationId: optionalString(raw.invocation_id),
    itemId: optionalString(raw.item_id),
    revision: typeof raw.revision === 'number' ? raw.revision : undefined,
    partial: raw.partial === true ? true : undefined,
    final: raw.final === true ? true : undefined,
    outcome,
    exitCode: typeof raw.exit_code === 'number' ? raw.exit_code : undefined,
    startedAt: optionalString(raw.started_at),
    finishedAt: optionalString(raw.finished_at),
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
