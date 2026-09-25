import { useEffect, useRef, useState } from 'react'
import { getContent, getDiff, type FileContent, type FileDiff, type TreeEntry } from '../../api/files-client'

export type ActiveFile = { root: string; entry: TreeEntry }
export type PreviewTab = 'FILE' | 'DIFF'

// Content and diff are loaded only for the visible tab. Reload keeps the
// displayed content on 304 or failure; a new selection invalidates old requests.
export function useFilePreview(file: ActiveFile | null, tab: PreviewTab) {
  const [content, setContent] = useState<FileContent | null>(null)
  const [contentLoading, setContentLoading] = useState(false)
  const [contentError, setContentError] = useState<string | null>(null)
  const [diff, setDiff] = useState<FileDiff | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffError, setDiffError] = useState<Error | null>(null)
  const [reloading, setReloading] = useState(false)
  const [reloadError, setReloadError] = useState<string | null>(null)
  const generationRef = useRef(0)

  useEffect(() => {
    const generation = ++generationRef.current
    const current = () => generation === generationRef.current
    setReloading(false)
    setReloadError(null)

    if (file !== null && tab === 'FILE') {
      setContent(null)
      setContentError(null)
      setContentLoading(true)
      void getContent(file.root, file.entry.path)
        .then((value) => {
          if (!current()) return
          setContentLoading(false)
          if (value !== undefined) setContent(value)
        })
        .catch((error: unknown) => {
          if (!current()) return
          setContentLoading(false)
          setContentError(error instanceof Error ? error.message : 'failed to load file')
        })
    } else if (file !== null) {
      setDiff(null)
      setDiffError(null)
      setDiffLoading(true)
      void getDiff(file.root, file.entry.path)
        .then((value) => {
          if (!current()) return
          setDiffLoading(false)
          setDiff(value)
        })
        .catch((error: unknown) => {
          if (!current()) return
          setDiffLoading(false)
          setDiffError(error instanceof Error ? error : new Error('failed to load diff'))
        })
    }

    return () => { ++generationRef.current }
  }, [file, tab])

  async function reload(): Promise<void> {
    if (file === null || content === null || tab !== 'FILE') return
    const generation = ++generationRef.current
    setReloading(true)
    setReloadError(null)
    try {
      const updated = await getContent(file.root, file.entry.path, content.etag)
      if (generation !== generationRef.current) return
      if (updated !== undefined) setContent(updated)
    } catch (error: unknown) {
      if (generation !== generationRef.current) return
      setReloadError(error instanceof Error ? error.message : 'failed to reload file')
    } finally {
      if (generation === generationRef.current) setReloading(false)
    }
  }

  return { content, contentLoading, contentError, diff, diffLoading, diffError, reloading, reloadError, reload }
}
