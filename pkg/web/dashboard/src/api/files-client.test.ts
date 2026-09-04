import { afterEach, describe, expect, test, vi } from 'vitest'
import { FilesApiError, getContent, getDiff, getReference, getRoots, getSearch, getTree } from './files-client'

describe('files-client', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('getTree parses entries and next_cursor', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [{ name: 'a.go', path: 'a.go', kind: 'file', language: 'go', selectable: true }],
        next_cursor: 'abc',
      }),
    } as Response)

    const page = await getTree('project', '.')

    expect(page.entries[0]).toEqual({
      name: 'a.go',
      path: 'a.go',
      kind: 'file',
      size: undefined,
      language: 'go',
      selectable: true,
    })
    expect(page.nextCursor).toBe('abc')
  })

  test('getTree omits nextCursor when the backend sends an empty string (no more pages)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [], next_cursor: '' }),
    } as Response)

    const page = await getTree('project', '.')
    expect(page.nextCursor).toBeUndefined()
  })

  test('getContent throws a typed FilesApiError parsed from the error body', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 415,
      json: async () => ({ error: 'binary_file' }),
    } as Response)

    await expect(getContent('project', 'bin')).rejects.toMatchObject({ code: 'binary_file', status: 415 })
    await expect(getContent('project', 'bin')).rejects.toBeInstanceOf(FilesApiError)
  })

  test('getContent falls back to read_failed when the error body is not JSON', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => {
        throw new Error('not json')
      },
    } as unknown as Response)

    await expect(getContent('project', 'x')).rejects.toMatchObject({ code: 'read_failed', status: 500 })
  })

  test('getContent returns undefined on 304 (unchanged) without reading the body', async () => {
    const json = vi.fn()
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 304,
      json,
      headers: { get: () => null },
    } as unknown as Response)

    const result = await getContent('project', 'a.go', '"etag-1"')

    expect(result).toBeUndefined()
    expect(json).not.toHaveBeenCalled()
  })

  test('getContent sends If-None-Match when an etag is passed', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 304,
      json: vi.fn(),
      headers: { get: () => null },
    } as unknown as Response)

    await getContent('project', 'a.go', '"etag-1"')

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/api/files/content?'),
      expect.objectContaining({ headers: { 'If-None-Match': '"etag-1"' } }),
    )
  })

  test('getContent maps snake_case fields and captures the ETag response header', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      headers: { get: (name: string) => (name === 'ETag' ? '"abc123"' : null) },
      json: async () => ({
        path: 'a.go',
        display_path: 'a.go',
        reference: 'HEAD',
        language: 'go',
        size: 10,
        modified_at: '2026-09-03T00:00:00Z',
        content: 'package a',
      }),
    } as unknown as Response)

    const content = await getContent('project', 'a.go')

    expect(content).toEqual({
      path: 'a.go',
      displayPath: 'a.go',
      reference: 'HEAD',
      language: 'go',
      size: 10,
      modifiedAt: '2026-09-03T00:00:00Z',
      content: 'package a',
      etag: '"abc123"',
    })
  })

  test('getRoots maps mount_read_only to camelCase', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ roots: [{ id: 'project', label: 'Project', kind: 'workspace', mount_read_only: true }] }),
    } as Response)

    const roots = await getRoots()

    expect(roots).toEqual([{ id: 'project', label: 'Project', kind: 'workspace', mountReadOnly: true }])
  })

  test('getReference maps display_path to camelCase', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ path: 'a.go', display_path: 'a.go', reference: 'HEAD' }),
    } as Response)

    const ref = await getReference('project', 'a.go')

    expect(ref).toEqual({ path: 'a.go', displayPath: 'a.go', reference: 'HEAD' })
  })

  test('getDiff throws FilesApiError(diff_unavailable, 409) on a 409 response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ error: 'diff_unavailable' }),
    } as Response)

    await expect(getDiff('project', 'a.go')).rejects.toMatchObject({ code: 'diff_unavailable', status: 409 })
  })

  test('getSearch parses entries and truncated, and forwards the abort signal', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [{ name: 'a.go', path: 'pkg/a.go', kind: 'file', language: 'go', selectable: true }],
        truncated: true,
      }),
    } as Response)

    const controller = new AbortController()
    const result = await getSearch('project', 'a', controller.signal)

    expect(result.truncated).toBe(true)
    expect(result.entries[0]).toMatchObject({ name: 'a.go', path: 'pkg/a.go', selectable: true })
    const [url, init] = fetchMock.mock.calls[0]!
    expect(String(url)).toContain('/api/files/search?root=project&q=a')
    expect((init as RequestInit).signal).toBe(controller.signal)
  })

  test('getSearch defaults truncated to false when the backend omits it', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [] }),
    } as Response)
    const result = await getSearch('project', 'x')
    expect(result.truncated).toBe(false)
    expect(result.entries).toEqual([])
  })
})
