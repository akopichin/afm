// Типы фронтенда для задач помощников стадии (collaboration, Этап 3): один advisory-помощник per stage,
// без рекурсии/делегирования (то — Этап 4a). Источник — GET /api/stages/{id}/assistants
// (pkg/server/assistants.go's AssistantTaskView) и GET /api/assistants/{id}/events
// (pkg/server/assistants.go's AssistantEventView).

// Зеркало pkg/assistants.TaskStatus (pkg/assistants/types.go) — собственный, отдельный от stage-FSM
// жизненный цикл задачи.
export const ASSISTANT_TASK_STATUSES = [
  'waiting_parent',
  'queued',
  'running',
  'waiting_children',
  'watching',
  'paused',
  'completed',
  'failed',
  'cancelled',
] as const

export type AssistantTaskStatus = (typeof ASSISTANT_TASK_STATUSES)[number]

export function isAssistantTaskStatus(value: unknown): value is AssistantTaskStatus {
  return typeof value === 'string' && (ASSISTANT_TASK_STATUSES as readonly string[]).includes(value)
}

// Терминальные статусы задачи — больше не изменятся (зеркало assistants.TaskStatus.Terminal()).
export const ASSISTANT_TASK_TERMINAL: ReadonlySet<AssistantTaskStatus> = new Set(['completed', 'failed', 'cancelled'])

// Человекочитаемые подписи — используются и рядом со статус-точкой задачи, и в ленте помощника.
export const ASSISTANT_TASK_STATUS_LABELS: Record<AssistantTaskStatus, string> = {
  waiting_parent: 'Waiting',
  queued: 'Queued',
  running: 'Running',
  waiting_children: 'Waiting on children',
  watching: 'Watching',
  paused: 'Paused',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
}

// Mode определяет, блокирует ли задача завершение корневой стадии (зеркало assistants.Mode). В Этапе 3
// `required` ещё ничего не блокирует (барьер — Этап 7) — но различать режимы текстом/значком, а не только
// цветом, нужно уже сейчас (dev-plan.md T3.10).
export type AssistantTaskMode = 'advisory' | 'required'

export function isAssistantTaskMode(value: unknown): value is AssistantTaskMode {
  return value === 'advisory' || value === 'required'
}

// AssistantTask — одна задача помощника, нормализованная из AssistantTaskView (camelCase, те же поля).
export type AssistantTask = {
  id: string
  parentTaskId: string | null
  profileId: string
  title: string
  mode: AssistantTaskMode
  trigger: string
  status: AssistantTaskStatus
  statusReason: string
  // depth — 1 для задачи верхнего уровня, depth родителя + 1 для каждого делегированного уровня ниже
  // (Этап 4a, зеркало assistants.Task.Depth). Используется breadcrumb'ом (AssistantBreadcrumb) вместе с
  // max_depth, который приходит отдельно (GET /api/assistant-profiles, уже не меняется по ходу рана).
  depth: number
  // watchPasses/watchLastCheckpointSeq (Этап 5, T5.6) — только для checkpoints/on_completion watcher-задачи
  // (зеркало server.AssistantTaskView.WatchPasses/WatchLastCheckpointSeq). null для обычной one_shot задачи —
  // она никогда не была привязана как наблюдатель, показывать нечего.
  watchPasses: number | null
  watchLastCheckpointSeq: number | null
}

// AssistantTaskNode — AssistantTask плюс дети по дереву (ByParent-проекция). Глубина не ограничена данными —
// только визуальный отступ капается на отрисовке (StagesList/AssistantTaskRow).
export type AssistantTaskNode = AssistantTask & { children: AssistantTaskNode[] }

// buildBreadcrumbChain walks taskId's parentTaskId chain UP to its root and returns it root-first
// (dev-plan.md T4a.8: "Frontend → QA → Mobile" — the whole chain including the task itself, not just its
// ancestors). A dangling/missing parent id (should never happen server-side, ValidateTree rejects it, but
// the dashboard reads a possibly-stale poll snapshot) stops the walk there rather than throwing — a partial
// chain is better than a crashed breadcrumb. visited guards against an accidental cycle in bad data for the
// same reason: render SOMETHING, never hang.
export function buildBreadcrumbChain(tasks: AssistantTask[], taskId: string): AssistantTask[] {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const chain: AssistantTask[] = []
  const visited = new Set<string>()
  let current: AssistantTask | undefined = byId.get(taskId)
  while (current !== undefined && !visited.has(current.id)) {
    chain.push(current)
    visited.add(current.id)
    current = current.parentTaskId !== null ? byId.get(current.parentTaskId) : undefined
  }
  return chain.reverse()
}

// Verdict — зеркало assistants.Verdict (pkg/assistants/types.go).
export const ASSISTANT_VERDICTS = ['pass', 'needs_changes', 'inconclusive'] as const
export type AssistantVerdict = (typeof ASSISTANT_VERDICTS)[number]

function isAssistantVerdict(value: unknown): value is AssistantVerdict {
  return typeof value === 'string' && (ASSISTANT_VERDICTS as readonly string[]).includes(value)
}

// verdictLabel/verdictGlyph — вердикт различается текстом/значком, не только цветом (T3.10 accessibility
// requirement): required/advisory и pass/needs_changes/inconclusive никогда не несут смысл одним лишь тоном.
export function verdictLabel(verdict: AssistantVerdict): string {
  switch (verdict) {
    case 'pass':
      return 'Pass'
    case 'needs_changes':
      return 'Needs changes'
    case 'inconclusive':
      return 'Inconclusive'
  }
}

export function verdictGlyph(verdict: AssistantVerdict): string {
  switch (verdict) {
    case 'pass':
      return '✓'
    case 'needs_changes':
      return '✕'
    case 'inconclusive':
      return '?'
  }
}

// Finding — зеркало assistants.Finding.
export type AssistantFinding = {
  id: string
  severity: string
  path: string | null
  lineStart: number | null
  lineEnd: number | null
  detail: string
}

// AssistantResult — зеркало assistants.Result (pkg/assistants/types.go), декодированный ход помощника с
// kind:"result". С коммита 419753b бэкенд публикует это как отдельное "task_result" событие в
// GET /api/assistants/{id}/events (runHelperTask бандлит task_result-эффект в тот же ApplyRunOperation, что
// и переход в completed, см. pkg/orchestrator/assistants.go's taskResultEffect/assistants_lifecycle.go) — в
// дополнение к result.json на диске (T3.6), не вместо него. parseAssistantResult читает событие defensively
// из ЛЮБОГО события журнала, чьи данные уже соответствуют этой форме: это живой путь чтения (см.
// AssistantTaskView.tsx's latestResult), подтверждённый сквозным
// cmd/afm/run_collaboration_integration_test.go, а не форвард-совместимый мёртвый код.
export type AssistantResult = {
  verdict: AssistantVerdict
  summary: string
  findings: AssistantFinding[]
  evidence: string[]
  contextVersion: string
  suggestedChange: string
  limitations: string
}

export function parseAssistantResult(raw: unknown): AssistantResult | null {
  if (raw === null || typeof raw !== 'object') return null
  const obj = raw as Record<string, unknown>
  if (!isAssistantVerdict(obj.verdict) || typeof obj.summary !== 'string') return null

  const findings: AssistantFinding[] = Array.isArray(obj.findings)
    ? obj.findings.map(toFinding).filter((f): f is AssistantFinding => f !== null)
    : []
  const evidence = Array.isArray(obj.evidence) ? obj.evidence.filter((v): v is string => typeof v === 'string') : []

  return {
    verdict: obj.verdict,
    summary: obj.summary,
    findings,
    evidence,
    contextVersion: typeof obj.context_version === 'string' ? obj.context_version : '',
    suggestedChange: typeof obj.suggested_change === 'string' ? obj.suggested_change : '',
    limitations: typeof obj.limitations === 'string' ? obj.limitations : '',
  }
}

function toFinding(raw: unknown): AssistantFinding | null {
  if (raw === null || typeof raw !== 'object') return null
  const obj = raw as Record<string, unknown>
  if (typeof obj.id !== 'string' || typeof obj.severity !== 'string' || typeof obj.detail !== 'string') return null
  return {
    id: obj.id,
    severity: obj.severity,
    path: typeof obj.path === 'string' ? obj.path : null,
    lineStart: typeof obj.line_start === 'number' ? obj.line_start : null,
    lineEnd: typeof obj.line_end === 'number' ? obj.line_end : null,
    detail: obj.detail,
  }
}
