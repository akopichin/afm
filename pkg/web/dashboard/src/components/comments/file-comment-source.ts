// Сверка якоря файлового комментария с текущим файлом перед отправкой: одна загрузка на файл, статус — по записи.

import { FilesApiError, getContent } from '../../api/files-client'
import { fileGroupKey, rawSourceLines, sourceDrift } from '../file-browser/draft-comments'
import type { CommentEntry } from './comment-state'

export type FileEntry = CommentEntry & { anchor: Extract<CommentEntry['anchor'], { kind: 'file' }> }

export function isSendableFileComment(entry: CommentEntry): entry is FileEntry {
  return entry.anchor.kind === 'file' && entry.text.trim() !== ''
}

// FileFetch — что известно о файле после загрузки; 'unchanged' — 304: все якоря файла совпадают с ETag.
export type FileFetch =
  | { kind: 'unchanged' }
  | { kind: 'content'; etag: string; lines: string[] }
  | { kind: 'missing' }
  | { kind: 'error' }

export type SourceStatus =
  | { kind: 'checking' }
  | { kind: 'ok' }
  | { kind: 'changed'; currentText: string | null; etag: string }
  | { kind: 'missing' }
  | { kind: 'error' }

export function fileKey(entry: FileEntry): string {
  return fileGroupKey(entry.anchor.root, entry.anchor.path)
}

// fetchSources грузит каждый файл один раз. Условный запрос возможен, только если у всех комментариев файла один
// ETag: иначе 304 не дал бы содержимого для комментариев со старым ETag.
export async function fetchSources(entries: readonly FileEntry[]): Promise<Map<string, FileFetch>> {
  const etagsByFile = new Map<string, { root: string; path: string; etags: Set<string> }>()
  for (const entry of entries) {
    const key = fileKey(entry)
    const file = etagsByFile.get(key) ?? { root: entry.anchor.root, path: entry.anchor.path, etags: new Set<string>() }
    file.etags.add(entry.anchor.etag)
    etagsByFile.set(key, file)
  }
  const results = await Promise.all(
    Array.from(etagsByFile, async ([key, { root, path, etags }]): Promise<[string, FileFetch]> => {
      const [etag] = etags
      try {
        const content = await getContent(root, path, etags.size === 1 ? etag : undefined)
        if (content === undefined) return [key, { kind: 'unchanged' }]
        return [key, { kind: 'content', etag: content.etag, lines: rawSourceLines(content.content) }]
      } catch (err) {
        return [key, err instanceof FilesApiError && err.status === 404 ? { kind: 'missing' } : { kind: 'error' }]
      }
    }),
  )
  return new Map(results)
}

// sourceStatus — совпавшая строка подтверждает якорь даже при новом ETag; иначе нужно явное решение пользователя.
export function sourceStatus(entry: FileEntry, fetched: FileFetch | undefined): SourceStatus {
  switch (fetched?.kind) {
    case undefined:
      return { kind: 'checking' }
    case 'unchanged':
      return { kind: 'ok' }
    case 'missing':
    case 'error':
      return { kind: fetched.kind }
    case 'content': {
      const drift = sourceDrift(entry.quote, entry.anchor.line, fetched.lines)
      return drift === null ? { kind: 'ok' } : { kind: 'changed', currentText: drift.currentText, etag: fetched.etag }
    }
  }
}

// confirmationToken — что именно подтвердил пользователь: новое расхождение (другая текущая строка) требует нового решения.
export function confirmationToken(status: SourceStatus): string {
  return status.kind === 'changed' ? `changed:${status.currentText ?? ''}` : status.kind
}
