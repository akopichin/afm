import { useEffect, useMemo, useState, type ReactElement } from 'react'
import { useComments } from '../comments/CommentProvider'
import { RECIPIENT_GONE, resolveCommentRecipient, useCommentRecipients } from '../comments/comment-recipients'
import { isDraft } from '../comments/comment-state'
import { fetchSources, fileKey, isSendableFileComment, sourceStatus, type FileEntry, type FileFetch, type SourceStatus } from '../comments/file-comment-source'
import { fileGroupKey } from './draft-comments'

export type CommentTarget = { root: string; path: string; line: number }

type FileCommentsListProps = {
  rootLabel: (rootId: string) => string
  onOpen: (target: CommentTarget) => void
}

// Список всех файловых комментариев рана по корням и файлам: читает то же run-scoped хранилище, что и
// карточки у строк, а не свою копию.
export function FileCommentsList({ rootLabel, onOpen }: FileCommentsListProps): ReactElement {
  const { comments, lastRecipientId } = useComments()
  const recipients = useCommentRecipients()
  const fileComments = useMemo(() => comments.filter(isSendableFileComment), [comments])

  // Источник сверяется, пока список показан и пока меняется набор (root, path, etag): один запрос на файл, без опроса.
  // Правка текста комментария набор не меняет, поэтому лишних запросов нет.
  const [fetched, setFetched] = useState<Map<string, FileFetch>>(new Map())
  const sourcesKey = JSON.stringify(
    Array.from(new Set(fileComments.map((e) => JSON.stringify([e.anchor.root, e.anchor.path, e.anchor.etag])))).sort(),
  )
  useEffect(() => {
    let cancelled = false
    void fetchSources(fileComments).then((result) => {
      if (!cancelled) setFetched(result)
    })
    return () => {
      cancelled = true
    }
  }, [sourcesKey]) // eslint-disable-line react-hooks/exhaustive-deps

  const groups = useMemo(() => {
    const byFile = new Map<string, { key: string; label: string; entries: FileEntry[] }>()
    for (const entry of fileComments) {
      const { root, path } = entry.anchor
      const key = fileGroupKey(root, path)
      const group = byFile.get(key) ?? { key, label: `${rootLabel(root)} / ${path}`, entries: [] }
      group.entries.push(entry)
      byFile.set(key, group)
    }
    const sorted = Array.from(byFile.values()).sort((a, b) => a.label.localeCompare(b.label))
    for (const group of sorted) group.entries.sort((a, b) => a.anchor.line - b.anchor.line)
    return sorted
  }, [fileComments, rootLabel])

  if (groups.length === 0) {
    return <div className="file-tree-hint">No file comments yet. Click a line number in a file to add one.</div>
  }

  function recipientText(entry: FileEntry): string {
    const { recipient, problem } = resolveCommentRecipient(recipients, entry.recipientId, lastRecipientId)
    if (recipient !== null) return `→ ${recipient.label}`
    return problem === RECIPIENT_GONE ? 'Agent unavailable' : 'No agent chosen'
  }

  const statusOf = (entry: FileEntry): SourceStatus['kind'] => sourceStatus(entry, fetched.get(fileKey(entry))).kind

  return (
    <div className="file-comments-list">
      {groups.map((group) => (
        <div key={group.key} className="file-comments-group" role="group" aria-label={group.label}>
          <div className="file-comments-group-title">{group.label}</div>
          {group.entries.map((entry) => (
            <button
              key={entry.key}
              type="button"
              className="file-comments-row"
              onClick={() => onOpen({ root: entry.anchor.root, path: entry.anchor.path, line: entry.anchor.line })}
            >
              <span className="file-comments-row-head">
                <span className="file-comments-row-line">Line {entry.anchor.line}</span>
                {isDraft(entry) && <span className="file-comments-badge">Draft</span>}
                <SourceBadge kind={statusOf(entry)} />
              </span>
              <span className="file-comments-row-text">{entry.text}</span>
              <span className="file-comments-row-recipient">{recipientText(entry)}</span>
            </button>
          ))}
        </div>
      ))}
    </div>
  )
}

function SourceBadge({ kind }: { kind: SourceStatus['kind'] }): ReactElement | null {
  if (kind === 'changed') return <span className="file-comments-badge file-comments-badge-warn">Source changed</span>
  if (kind === 'missing') return <span className="file-comments-badge file-comments-badge-warn">Source missing</span>
  return null
}
