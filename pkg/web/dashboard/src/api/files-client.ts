// Типизированный клиент к GET /api/files/* — дереву проекта в браузере файлов
// дашборда (Task 13 строит на нём UI, эта клетка — только клиент). Пара к
// run-client.ts (мутирующие POST-команды): тот же fetch-паттерн, но для
// чтения, поэтому разбор ответа ближе к normalizeStatus (use-status.ts) —
// snake_case JSON с бэкенда приводится к camelCase на границе клиента,
// с защитными дефолтами на месте отсутствующих/неверных полей.

export class FilesApiError extends Error {
  constructor(
    public code: string,
    public status: number,
  ) {
    super(code)
    this.name = 'FilesApiError'
  }
}

export type RootView = {
  id: string
  label: string
  kind: string
  mountReadOnly: boolean
}

export type TreeEntryKind = 'file' | 'directory' | 'symlink'

export type TreeEntry = {
  name: string
  path: string
  kind: TreeEntryKind
  size?: number
  language?: string
  selectable: boolean
}

export type TreePage = {
  entries: TreeEntry[]
  nextCursor?: string
}

export type FileReference = {
  path: string
  displayPath: string
  reference: string
}

export type FileContent = {
  path: string
  displayPath: string
  reference: string
  language: string
  size: number
  modifiedAt: string
  content: string
  // etag приходит не в теле ответа, а в заголовке ETag — нужен вызывающему,
  // чтобы передать его следующим запросом через If-None-Match.
  etag: string
}

export type FileDiff = {
  path: string
  baseline: string
  status: string
  binary: boolean
  truncated: boolean
  diff: string
}

// fetchOk делает GET и бросает типизированную FilesApiError на не-ok ответ
// (кроме 304 — это валидный "не изменилось", обрабатывается вызывающим).
// Код ошибки разбирается из JSON-тела {"error":"<code>"}; если тело не JSON —
// падаем на 'read_failed' по контракту бэкенда.
async function fetchOk(url: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(url, init)
  if (response.ok || response.status === 304) return response

  let code = 'read_failed'
  try {
    const body: unknown = await response.json()
    if (isRecord(body) && typeof body.error === 'string') code = body.error
  } catch {
    // тело не JSON — оставляем код по умолчанию
  }
  throw new FilesApiError(code, response.status)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') search.set(key, value)
  }
  const qs = search.toString()
  return qs ? `?${qs}` : ''
}

export async function getRoots(): Promise<RootView[]> {
  const response = await fetchOk('/api/files/roots')
  const data = (await response.json()) as { roots?: unknown }
  return Array.isArray(data.roots) ? data.roots.map(toRootView).filter((r): r is RootView => r !== null) : []
}

function toRootView(raw: unknown): RootView | null {
  if (!isRecord(raw) || typeof raw.id !== 'string') return null
  return {
    id: raw.id,
    label: typeof raw.label === 'string' ? raw.label : '',
    kind: typeof raw.kind === 'string' ? raw.kind : '',
    mountReadOnly: raw.mount_read_only === true,
  }
}

export async function getTree(root: string, path: string, cursor?: string): Promise<TreePage> {
  const response = await fetchOk(`/api/files/tree${query({ root, path, cursor })}`)
  const data = (await response.json()) as { entries?: unknown; next_cursor?: unknown }
  const entries = Array.isArray(data.entries)
    ? data.entries.map(toTreeEntry).filter((e): e is TreeEntry => e !== null)
    : []
  const nextCursor = typeof data.next_cursor === 'string' && data.next_cursor !== '' ? data.next_cursor : undefined
  return { entries, nextCursor }
}

function toTreeEntry(raw: unknown): TreeEntry | null {
  if (!isRecord(raw) || typeof raw.name !== 'string' || typeof raw.path !== 'string') return null
  return {
    name: raw.name,
    path: raw.path,
    kind: isTreeEntryKind(raw.kind) ? raw.kind : 'file',
    size: typeof raw.size === 'number' ? raw.size : undefined,
    language: typeof raw.language === 'string' ? raw.language : undefined,
    selectable: raw.selectable === true,
  }
}

function isTreeEntryKind(value: unknown): value is TreeEntryKind {
  return value === 'file' || value === 'directory' || value === 'symlink'
}

export async function getReference(root: string, path: string): Promise<FileReference> {
  const response = await fetchOk(`/api/files/reference${query({ root, path })}`)
  const data = (await response.json()) as Record<string, unknown>
  return {
    path: typeof data.path === 'string' ? data.path : '',
    displayPath: typeof data.display_path === 'string' ? data.display_path : '',
    reference: typeof data.reference === 'string' ? data.reference : '',
  }
}

// getContent возвращает undefined на 304 ("не изменилось" — вызывающий
// сохраняет предыдущий контент как есть) вместо того, чтобы бросать ошибку:
// это ожидаемый, а не исключительный исход условного запроса.
export async function getContent(root: string, path: string, etag?: string): Promise<FileContent | undefined> {
  const response = await fetchOk(`/api/files/content${query({ root, path })}`, etag ? { headers: { 'If-None-Match': etag } } : undefined)
  if (response.status === 304) return undefined

  const data = (await response.json()) as Record<string, unknown>
  return {
    path: typeof data.path === 'string' ? data.path : '',
    displayPath: typeof data.display_path === 'string' ? data.display_path : '',
    reference: typeof data.reference === 'string' ? data.reference : '',
    language: typeof data.language === 'string' ? data.language : '',
    size: typeof data.size === 'number' ? data.size : 0,
    modifiedAt: typeof data.modified_at === 'string' ? data.modified_at : '',
    content: typeof data.content === 'string' ? data.content : '',
    etag: response.headers.get('ETag') ?? '',
  }
}

export async function getDiff(root: string, path: string): Promise<FileDiff> {
  const response = await fetchOk(`/api/files/diff${query({ root, path })}`)
  const data = (await response.json()) as Record<string, unknown>
  return {
    path: typeof data.path === 'string' ? data.path : '',
    baseline: typeof data.baseline === 'string' ? data.baseline : '',
    status: typeof data.status === 'string' ? data.status : '',
    binary: data.binary === true,
    truncated: data.truncated === true,
    diff: typeof data.diff === 'string' ? data.diff : '',
  }
}

export type SearchResult = {
  entries: TreeEntry[]
  truncated: boolean
}

// getSearch запрашивает GET /api/files/search. signal позволяет вызывающему
// отменить устаревший запрос (см. FileBrowserModal — debounce + AbortController).
export async function getSearch(root: string, query2: string, signal?: AbortSignal): Promise<SearchResult> {
  const response = await fetchOk(`/api/files/search${query({ root, q: query2 })}`, signal ? { signal } : undefined)
  const data = (await response.json()) as { entries?: unknown; truncated?: unknown }
  const entries = Array.isArray(data.entries)
    ? data.entries.map(toTreeEntry).filter((e): e is TreeEntry => e !== null)
    : []
  return { entries, truncated: data.truncated === true }
}

export type ChangeStatus = 'modified' | 'added' | 'deleted'
export type ChangeEntry = TreeEntry & { status: ChangeStatus }
export type ChangeList = { entries: ChangeEntry[]; truncated: boolean }

function isChangeStatus(value: unknown): value is ChangeStatus {
  return value === 'modified' || value === 'added' || value === 'deleted'
}

// toChangeEntry требует строковые name/path и известный status; иначе строка
// отбрасывается (не выдумываем 'modified'). kind синтезируется как 'file' —
// changed-entry структурно совместим с TreeEntry, чтобы openFile/onToggleSelect
// принимали его без адаптеров.
function toChangeEntry(raw: unknown): ChangeEntry | null {
  if (!isRecord(raw) || typeof raw.name !== 'string' || typeof raw.path !== 'string' || !isChangeStatus(raw.status)) {
    return null
  }
  return {
    name: raw.name,
    path: raw.path,
    kind: 'file',
    status: raw.status,
    selectable: raw.selectable === true,
  }
}

// getChanged запрашивает GET /api/files/changed (git-статус относительно
// индекса или HEAD — переключатель mode). signal — тот же паттерн отмены
// устаревшего запроса, что и у getSearch.
export async function getChanged(root: string, mode: 'index' | 'head', signal?: AbortSignal): Promise<ChangeList> {
  const response = await fetchOk(`/api/files/changed${query({ root, mode })}`, signal ? { signal } : undefined)
  const data = (await response.json()) as { entries?: unknown; truncated?: unknown }
  const entries = Array.isArray(data.entries)
    ? data.entries.map(toChangeEntry).filter((e): e is ChangeEntry => e !== null)
    : []
  return { entries, truncated: data.truncated === true }
}
