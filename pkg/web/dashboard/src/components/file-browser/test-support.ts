// Тестовый (не production) хелпер: маршрутизирует моки globalThis.fetch по
// /api/files/* так же, как это делает files-client.ts (Task 12) — снаружи
// проверяем через реальный files-client, а не через vi.mock его модуля,
// следуя конвенции репозитория (см. DialogChannel.test.tsx: моки на уровне
// fetch, не на уровне api-клиента). Не *.test.ts — vitest не подхватит его
// как тестовый файл; используется из FileTree.test.tsx и FileBrowserModal.test.tsx.
import { vi } from 'vitest'

type RootInput = { id: string; label: string; kind?: string; mountReadOnly?: boolean }
type TreeEntryInput = {
  name: string
  path: string
  kind: 'file' | 'directory' | 'symlink'
  language?: string
  size?: number
  selectable?: boolean
}
type ContentInput = { language: string; content: string; size?: number; modifiedAt?: string; etag?: string }
type DiffInput = { status?: string; binary?: boolean; truncated?: boolean; diff?: string }

export function jsonResponse(data: unknown, opts: { status?: number; etag?: string } = {}): Response {
  const status = opts.status ?? 200
  return {
    ok: status < 400,
    status,
    json: async () => data,
    headers: { get: (name: string) => (name === 'ETag' ? (opts.etag ?? null) : null) },
  } as unknown as Response
}

export function errorResponse(code: string, status: number): Response {
  return jsonResponse({ error: code }, { status })
}

// FilesApiMock — построитель фикстур для GET /api/files/*. Запросы,
// для которых ничего не настроено, отвечают 404 not_found (тот же код,
// что и реальный бэкенд для несуществующего root/path).
export class FilesApiMock {
  private roots: RootInput[] = []
  private trees = new Map<string, { entries: TreeEntryInput[]; nextCursor?: string }>()
  private references = new Map<string, string>()
  private contents = new Map<string, ContentInput>()
  private diffs = new Map<string, DiffInput | { errorCode: string; status: number }>()

  setRoots(roots: RootInput[]): void {
    this.roots = roots
  }

  private rootLabel(root: string): string {
    return this.roots.find((r) => r.id === root)?.label ?? root
  }

  setTree(root: string, path: string, entries: TreeEntryInput[], opts: { cursor?: string; nextCursor?: string } = {}): void {
    this.trees.set(`${root}|${path}|${opts.cursor ?? ''}`, { entries, nextCursor: opts.nextCursor })
  }

  setReference(root: string, path: string, reference: string): void {
    this.references.set(`${root}|${path}`, reference)
  }

  setContent(root: string, path: string, content: ContentInput): void {
    this.contents.set(`${root}|${path}`, content)
  }

  setDiff(root: string, path: string, diff: DiffInput): void {
    this.diffs.set(`${root}|${path}`, diff)
  }

  setDiffError(root: string, path: string, code: string, status: number): void {
    this.diffs.set(`${root}|${path}`, { errorCode: code, status })
  }

  install(): void {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const rawUrl = typeof input === 'string' ? input : (input as Request).url
      const url = new URL(rawUrl, 'http://localhost')
      const root = url.searchParams.get('root') ?? ''
      const path = url.searchParams.get('path') ?? ''
      const cursor = url.searchParams.get('cursor') ?? ''

      if (url.pathname === '/api/files/roots') {
        return jsonResponse({
          roots: this.roots.map((r) => ({ id: r.id, label: r.label, kind: r.kind ?? 'project', mount_read_only: r.mountReadOnly ?? false })),
        })
      }

      if (url.pathname === '/api/files/tree') {
        const page = this.trees.get(`${root}|${path}|${cursor}`)
        if (page === undefined) return errorResponse('not_found', 404)
        return jsonResponse({
          entries: page.entries.map((e) => ({
            name: e.name,
            path: e.path,
            kind: e.kind,
            language: e.language,
            size: e.size,
            selectable: e.selectable ?? e.kind === 'file',
          })),
          next_cursor: page.nextCursor ?? '',
        })
      }

      if (url.pathname === '/api/files/reference') {
        const reference = this.references.get(`${root}|${path}`)
        if (reference === undefined) return errorResponse('not_found', 404)
        return jsonResponse({ path, display_path: `${this.rootLabel(root)}/${path}`, reference })
      }

      if (url.pathname === '/api/files/content') {
        const c = this.contents.get(`${root}|${path}`)
        if (c === undefined) return errorResponse('not_found', 404)
        return jsonResponse(
          {
            path,
            display_path: `${this.rootLabel(root)}/${path}`,
            reference: `[AFM file: "${root}/${path}"]`,
            language: c.language,
            size: c.size ?? c.content.length,
            modified_at: c.modifiedAt ?? '2026-01-01T00:00:00Z',
            content: c.content,
          },
          { etag: c.etag ?? 'W/"1"' },
        )
      }

      if (url.pathname === '/api/files/diff') {
        const d = this.diffs.get(`${root}|${path}`)
        if (d === undefined) return errorResponse('not_found', 404)
        if ('errorCode' in d) return errorResponse(d.errorCode, d.status)
        return jsonResponse({
          path,
          baseline: 'HEAD',
          status: d.status ?? 'modified',
          binary: d.binary ?? false,
          truncated: d.truncated ?? false,
          diff: d.diff ?? '',
        })
      }

      return errorResponse('not_found', 404)
    })
  }
}
