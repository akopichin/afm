import { useEffect, useState } from 'react'
import { listNotes } from '../../api/run-client'

// Poll only during a review pause; keep the last count on transient errors.
export function useReviewNoteCount(enabled: boolean): number {
  const [reviewNoteCount, setReviewNoteCount] = useState(0)
  useEffect(() => {
    if (!enabled) {
      setReviewNoteCount(0)
      return
    }

    let cancelled = false
    const load = () => {
      listNotes()
        .then(({ notes }) => {
          if (!cancelled) setReviewNoteCount(notes.length)
        })
        .catch(() => {
          /* сеть отвалилась — оставляем предыдущее значение, следующий тик повторит */
        })
    }

    load()
    const timer = setInterval(load, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [enabled])
  return reviewNoteCount
}
