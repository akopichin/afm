import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { CommentProvider, useComments, type CommentsApi, type FrozenSend } from './CommentProvider'
import type { CommentAnchor } from './comment-state'

const file = (line: number): CommentAnchor => ({
  kind: 'file', root: 'project', path: 'a.go', displayPath: 'project/a.go', reference: '[AFM file: "/w/a.go"]', etag: 'e1', line,
})

// Probe — тестовый «потребитель»: пробрасывает актуальный API наружу (живёт всё время, как App-уровень).
function Probe({ out }: { out: { api: CommentsApi | null } }): null {
  out.api = useComments()
  return null
}

// FilesStub — Files-подобный потребитель: его размонтирование не должно трогать провайдер.
function FilesStub(): null {
  useComments()
  return null
}

function Harness({ runId, out, showFiles }: { runId: string; out: { api: CommentsApi | null }; showFiles: boolean }): React.ReactElement {
  return (
    <CommentProvider runId={runId}>
      <Probe out={out} />
      {showFiles && <FilesStub />}
    </CommentProvider>
  )
}

function setup(runId = 'r1', showFiles = true) {
  const out: { api: CommentsApi | null } = { api: null }
  const view = render(<Harness runId={runId} out={out} showFiles={showFiles} />)
  const api = (): CommentsApi => {
    if (out.api === null) throw new Error('no api')
    return out.api
  }
  return { out, api, view, rerender: (r: string, files: boolean) => view.rerender(<Harness runId={r} out={out} showFiles={files} />) }
}

function type(api: () => CommentsApi, key: string, line: number, text: string): void {
  act(() => api().open(key, { anchor: file(line), quote: 'src' }))
  act(() => api().edit(key, text))
}

function freeze(api: () => CommentsApi, body = 'msg'): FrozenSend {
  return {
    recipientId: 'agent:a',
    route: 'mesh',
    originStage: 's1',
    body,
    revisions: api().comments.map((c) => ({ key: c.key, revision: c.revision })),
  }
}

function deferred() {
  let resolve!: () => void
  let reject!: (e: Error) => void
  const promise = new Promise<void>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('CommentProvider', () => {
  it('keeps drafts while the Files-like consumer unmounts and mounts again', () => {
    const { api, rerender } = setup()
    type(api, 'f1', 3, 'draft')
    rerender('r1', false)
    rerender('r1', true)
    expect(api().comments.map((c) => c.text)).toEqual(['draft'])
  })

  it('accepted send after the consumer unmounted clears only the captured revisions', async () => {
    const { api, rerender } = setup()
    type(api, 'f1', 1, 'one')
    type(api, 'f2', 2, 'two')
    const op = deferred()
    const deliver = vi.fn(() => op.promise)

    act(() => api().send.start(freeze(api), deliver))
    rerender('r1', false) // Files закрыт, запрос продолжает жить в провайдере
    act(() => api().edit('f2', 'two, continued'))
    await act(async () => op.resolve())

    expect(api().comments.map((c) => [c.key, c.text])).toEqual([['f2', 'two, continued']])
    expect(api().send.accepted).toEqual({ count: 2, recipientId: 'agent:a' })
    expect(api().send.pending).toBe(false)
  })

  it('failed send keeps the text and a retry after remount reuses the same payload and request id', async () => {
    const { api, rerender } = setup()
    type(api, 'f1', 1, 'one')
    const deliver = vi.fn<(f: FrozenSend, id: string) => Promise<void>>()
    deliver.mockRejectedValueOnce(new Error('network down')).mockResolvedValueOnce(undefined)

    await act(async () => api().send.start(freeze(api), deliver))
    expect(api().send.error).toBe('network down')
    expect(api().comments).toHaveLength(1)

    rerender('r1', false)
    rerender('r1', true)
    await act(async () => api().send.retry())

    expect(deliver).toHaveBeenCalledTimes(2)
    expect(deliver.mock.calls[1]![0]).toEqual(deliver.mock.calls[0]![0])
    expect(deliver.mock.calls[1]![1]).toBe(deliver.mock.calls[0]![1])
    expect(api().comments).toEqual([])
    expect(api().send.error).toBeNull()
  })

  it('a second Send while one is pending creates no request', () => {
    const { api } = setup()
    type(api, 'f1', 1, 'one')
    const deliver = vi.fn(() => new Promise<void>(() => {}))

    act(() => {
      api().send.start(freeze(api), deliver)
      api().send.start(freeze(api, 'other'), deliver)
    })

    expect(deliver).toHaveBeenCalledTimes(1)
    expect(api().send.pending).toBe(true)
    expect(api().send.operation?.body).toBe('msg')
  })

  it('a new run starts without the previous run drafts and its late acceptance cannot clear new text', async () => {
    const { api, rerender } = setup('r1')
    type(api, 'f1', 1, 'old run')
    const op = deferred()
    act(() => api().send.start(freeze(api), () => op.promise))

    rerender('r2', true)
    expect(api().comments).toEqual([])
    expect(api().send.pending).toBe(false)

    type(api, 'f1', 1, 'new run') // тот же ключ и та же ревизия, что у отправленного текста
    await act(async () => op.resolve())

    expect(api().comments.map((c) => c.text)).toEqual(['new run'])
    expect(api().send.accepted).toBeNull()
  })

  it('acknowledge clears only the captured revision, outside the send operation', () => {
    const { api } = setup()
    type(api, 'f1', 1, 'one')
    type(api, 'f2', 2, 'two')
    const sent = api().comments.map((c) => ({ key: c.key, revision: c.revision }))

    act(() => api().edit('f2', 'two, continued'))
    act(() => api().acknowledge(sent))

    expect(api().comments.map((c) => [c.key, c.text])).toEqual([['f2', 'two, continued']])
  })

  it('acknowledge captured before a run change cannot clear the new run text', () => {
    const { api, rerender } = setup('r1')
    type(api, 'f1', 1, 'old run')
    const acknowledgeOld = api().acknowledge
    const sent = api().comments.map((c) => ({ key: c.key, revision: c.revision }))

    rerender('r2', true)
    type(api, 'f1', 1, 'new run')
    act(() => acknowledgeOld(sent))

    expect(api().comments.map((c) => c.text)).toEqual(['new run'])
  })

  it('first run id after page load does not reset drafts', () => {
    const { api, rerender } = setup('')
    type(api, 'f1', 1, 'early')
    rerender('r1', true)
    expect(api().comments.map((c) => c.text)).toEqual(['early'])
  })

  it('throws a clear error outside the provider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    function Bare(): null {
      useComments()
      return null
    }
    expect(() => render(<Bare />)).toThrow(/CommentProvider/)
    spy.mockRestore()
  })

  it('cancel drops the failed operation so a stale error is not shown', async () => {
    const { api } = setup()
    type(api, 'f1', 1, 'one')
    await act(async () => api().send.start(freeze(api), () => Promise.reject(new Error('boom'))))
    expect(api().send.error).toBe('boom')

    act(() => api().send.cancel())
    expect(api().send.operation).toBeNull()
    expect(api().send.error).toBeNull()
  })
  it('Send while a failed operation is unresolved keeps the original payload and deliver', async () => {
    const { api } = setup()
    type(api, 'f1', 1, 'one')
    const failing = vi.fn<(f: FrozenSend, id: string) => Promise<void>>().mockRejectedValue(new Error('down'))
    await act(async () => api().send.start(freeze(api), failing))

    type(api, 'f1', 1, 'one, edited')
    const other = vi.fn(() => Promise.resolve())
    await act(async () => api().send.start(freeze(api, 'changed packet'), other))

    expect(other).not.toHaveBeenCalled()
    expect(api().send.operation?.body).toBe('msg')
    await act(async () => api().send.retry())
    expect(failing).toHaveBeenCalledTimes(2)
    expect(failing.mock.calls[1]![0].body).toBe('msg')
  })

  it('remembers a document scroll position per key and forgets it on a run change', () => {
    const { api, rerender } = setup('r1')
    act(() => api().scroll.set('plan:s1', 120))
    rerender('r1', false)
    expect(api().scroll.get('plan:s1')).toBe(120)
    expect(api().scroll.get('plan:s2')).toBeUndefined()

    rerender('r2', true)
    expect(api().scroll.get('plan:s1')).toBeUndefined()
  })
})
