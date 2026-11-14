import {
  budgetUpdateToWire,
  parseBudgetUpdateOutcome,
  parseBudgetView,
  parseBudgetYaml,
  type BudgetUpdateOutcome,
  type BudgetUpdateRequest,
  type BudgetView,
  type BudgetYaml,
} from '../types/budget'

// Клиент /api/budgets*. Ветвление по коду ошибки сервера, не по HTTP-статусу: 409 бывает и конфликтом
// ревизии, и «всё ещё ограничено».

export const BUDGET_ERROR_CONFLICT = 'budget_revision_conflict'
export const BUDGET_ERROR_STILL_LIMITED = 'budget_still_limited'

export class BudgetApiError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message !== '' ? message : `budget request failed (${status}${code !== '' ? ` ${code}` : ''})`)
    this.status = status
    this.code = code
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

async function failure(response: Response): Promise<BudgetApiError> {
  try {
    const body: unknown = await response.json()
    if (isRecord(body)) {
      return new BudgetApiError(response.status, typeof body.error === 'string' ? body.error : '', typeof body.message === 'string' ? body.message : '')
    }
  } catch {
    /* ответ без JSON-тела */
  }
  return new BudgetApiError(response.status, '', '')
}

async function request(url: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(url, init)
  if (!response.ok) throw await failure(response)
  return response.json()
}

function postInit(body: unknown): RequestInit {
  return { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

export async function fetchBudgets(): Promise<BudgetView[]> {
  const body = await request('/api/budgets')
  const list = isRecord(body) && Array.isArray(body.budgets) ? body.budgets : []
  return list.map(parseBudgetView).filter((v): v is BudgetView => v !== null)
}

export type BudgetViewResponse = { view: BudgetView; yaml: BudgetYaml | null }

export async function fetchBudgetView(scope: string): Promise<BudgetViewResponse> {
  const body = await request(`/api/budgets/view?scope=${encodeURIComponent(scope)}`)
  const view = isRecord(body) ? parseBudgetView(body.view) : null
  if (view === null) throw new BudgetApiError(200, '', 'budget view is missing in the response')
  return { view, yaml: isRecord(body) ? parseBudgetYaml(body.yaml) : null }
}

// updateBudget сохраняет override области; resumeError заполнен, когда сохранение прошло, а продолжение отклонено.
export async function updateBudget(req: BudgetUpdateRequest): Promise<BudgetUpdateOutcome> {
  const outcome = parseBudgetUpdateOutcome(await request('/api/budgets/update', postInit(budgetUpdateToWire(req))))
  if (outcome === null) throw new BudgetApiError(200, '', 'budget update result is missing in the response')
  return outcome
}

export async function resumeBudget(scope: string, expectedRevision: number): Promise<BudgetView> {
  const body = await request('/api/budgets/resume', postInit({ scope, expected_revision: expectedRevision }))
  const view = isRecord(body) ? parseBudgetView(body.view) : null
  if (view === null) throw new BudgetApiError(200, '', 'budget view is missing in the response')
  return view
}
