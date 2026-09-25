import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { getContent, getDiff, type FileContent } from '../../api/files-client'
import { useFilePreview, type ActiveFile, type PreviewTab } from './use-file-preview'

vi.mock('../../api/files-client', () => ({ getContent: vi.fn(), getDiff: vi.fn() }))

afterEach(() => { vi.resetAllMocks() })

function file(path: string): ActiveFile {
  return { root: 'project', entry: { name: path, path, kind: 'file', selectable: true } }
}

function content(text: string): FileContent {
  return { path: 'a.ts', displayPath: 'a.ts', reference: '', language: '', size: text.length, modifiedAt: '', etag: text, content: text }
}

describe('useFilePreview', () => {
  test.each(['file', 'tab'] as const)('rejects an old reload after leaving and returning to the same %s', async (switchBy) => {
    const a = file('a.ts')
    let finishReload!: (value: FileContent) => void
    const pending = new Promise<FileContent>((resolve) => { finishReload = resolve })
    vi.mocked(getContent).mockResolvedValue(content('fresh'))
    vi.mocked(getDiff).mockResolvedValue({ path: 'a.ts', baseline: 'index', status: 'clean', binary: false, truncated: false, diff: '' })
    const { result, rerender } = renderHook(
      ({ target, tab }: { target: ActiveFile; tab: PreviewTab }) => useFilePreview(target, tab),
      { initialProps: { target: a, tab: 'FILE' as PreviewTab } },
    )
    await waitFor(() => expect(result.current.content?.content).toBe('fresh'))
    vi.mocked(getContent).mockReturnValueOnce(pending)
    let reload!: Promise<void>
    act(() => { reload = result.current.reload() })
    expect(result.current.reloading).toBe(true)

    rerender({ target: switchBy === 'file' ? file('b.ts') : a, tab: switchBy === 'tab' ? 'DIFF' : 'FILE' })
    rerender({ target: a, tab: 'FILE' })
    await waitFor(() => expect(result.current.content?.content).toBe('fresh'))
    await act(async () => { finishReload(content('stale')); await reload })

    expect(result.current.content?.content).toBe('fresh')
    expect(result.current.reloading).toBe(false)
    expect(result.current.reloadError).toBeNull()
  })
})
