import type { ReactElement } from 'react'

// FileCommentCount — маркер «● N» у файла с комментариями в дереве, поиске и списке изменений.
export function FileCommentCount({ count }: { count: number }): ReactElement | null {
  if (count === 0) return null
  return (
    <span className="file-comment-count" role="img" aria-label={`${count} ${count === 1 ? 'comment' : 'comments'}`}>
      ● {count}
    </span>
  )
}

// noComments — счётчик по умолчанию для списков без комментариев (выбор файлов, изолированные тесты).
export const noComments = (): number => 0
