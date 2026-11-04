import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AttachmentActions, AttachmentRecord } from '../../hooks/use-image-paste'
import { EMPTY_COMPOSER_DRAFT, patchDraft, selectThought } from './composer-draft'
import { createComposerDraftStore, useComposerContext, useComposerDraft } from './composer-draft-store'

function attachment(id: string): AttachmentRecord {
  return { id, previewUrl: `blob:${id}`, uploading: false, failed: false, errorMsg: null, insertedText: `[Screenshot: /x/${id}.png]\n`, file: new File([], `${id}.png`) }
}

function liveActions(): AttachmentActions {
  return { snapshot: vi.fn(() => []), sentIn: vi.fn(() => []), discard: vi.fn() }
}

describe('composer draft store', () => {
  afterEach(() => vi.restoreAllMocks())

  it('a stage without a draft reads the one empty draft object', () => {
    const store = createComposerDraftStore()
    expect(store.get('s1')).toBe(EMPTY_COMPOSER_DRAFT)
    expect(store.get('s1')).toBe(store.get('s2'))
  })

  it('an update notifies only that stage, synchronously; an unchanged update notifies no one', () => {
    const store = createComposerDraftStore()
    const s1 = vi.fn()
    const s2 = vi.fn()
    store.subscribe('s1', s1)
    store.subscribe('s2', s2)
    const next = store.update('s1', (d) => patchDraft(d, { text: 'hi' }))
    expect(next.text).toBe('hi')
    expect(store.get('s1')).toBe(next)
    expect(s1).toHaveBeenCalledTimes(1)
    expect(s2).not.toHaveBeenCalled()
    store.update('s1', (d) => patchDraft(d, { text: 'hi' }))
    expect(s1).toHaveBeenCalledTimes(1)
  })

  it('an unsubscribed listener hears nothing more', () => {
    const store = createComposerDraftStore()
    const listener = vi.fn()
    const unsubscribe = store.subscribe('s1', listener)
    unsubscribe()
    store.update('s1', (d) => patchDraft(d, { text: 'x' }))
    expect(listener).not.toHaveBeenCalled()
  })

  it('discarding sent attachments goes through the mounted composer when there is one', () => {
    const store = createComposerDraftStore()
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const a = attachment('a')
    store.update('s1', (d) => patchDraft(d, { attachments: [a] }))
    const live = liveActions()
    const unregister = store.registerAttachments('s1', { current: live })
    store.discardAttachments('s1', [a])
    expect(live.discard).toHaveBeenCalledWith([a])
    expect(revoke).not.toHaveBeenCalled()
    unregister()
  })

  it('without a mounted composer the store drops the attachments and revokes their previews', () => {
    const store = createComposerDraftStore()
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const a = attachment('a')
    const b = attachment('b')
    store.update('s1', (d) => patchDraft(d, { attachments: [a, b] }))
    store.discardAttachments('s1', [a])
    expect(store.get('s1').attachments).toEqual([b])
    expect(revoke).toHaveBeenCalledWith('blob:a')
    expect(revoke).not.toHaveBeenCalledWith('blob:b')
  })

  it('a stale unregister does not remove the registration of a newer composer of the same stage', () => {
    const store = createComposerDraftStore()
    const older = liveActions()
    const newer = liveActions()
    const unregisterOlder = store.registerAttachments('s1', { current: older })
    store.registerAttachments('s1', { current: newer })
    unregisterOlder()
    store.discardAttachments('s1', [attachment('a')])
    expect(newer.discard).toHaveBeenCalled()
    expect(older.discard).not.toHaveBeenCalled()
  })

  it('revokeAll revokes every preview of every stage and is safe to repeat', () => {
    const store = createComposerDraftStore()
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    store.update('s1', (d) => patchDraft(d, { attachments: [attachment('a')] }))
    store.update('s2', (d) => patchDraft(d, { attachments: [attachment('b')] }))
    store.revokeAll()
    store.revokeAll()
    expect(revoke.mock.calls.map(([url]) => url).sort()).toEqual(['blob:a', 'blob:a', 'blob:b', 'blob:b'])
  })
})

describe('composer draft hooks', () => {
  it('useComposerDraft re-renders with every change of its own stage only', () => {
    const store = createComposerDraftStore()
    let renders = 0
    const { result } = renderHook(() => {
      renders += 1
      return useComposerDraft(store, 's1')
    })
    expect(result.current).toBe(EMPTY_COMPOSER_DRAFT)
    const before = renders
    act(() => {
      store.update('s1', (d) => patchDraft(d, { text: 'a' }))
    })
    expect(result.current.text).toBe('a')
    expect(renders).toBe(before + 1)
    act(() => {
      store.update('s2', (d) => patchDraft(d, { text: 'b' }))
    })
    expect(renders).toBe(before + 1)
  })

  it('useComposerContext does not re-render while the text changes, only when the context does', () => {
    const store = createComposerDraftStore()
    let renders = 0
    const { result } = renderHook(() => {
      renders += 1
      return useComposerContext(store, 's1')
    })
    expect(result.current).toBeNull()
    const before = renders
    act(() => {
      store.update('s1', (d) => patchDraft(d, { text: 'typing…' }))
      store.update('s1', (d) => patchDraft(d, { text: 'typing more' }))
    })
    expect(renders).toBe(before)
    act(() => {
      store.update('s1', (d) => selectThought(d, { key: 'k', text: 'quote' }, true))
    })
    expect(result.current).toEqual({ kind: 'thought', key: 'k', text: 'quote' })
    expect(renders).toBe(before + 1)
  })

  it('useComposerContext without a stage is null', () => {
    const store = createComposerDraftStore()
    const { result } = renderHook(() => useComposerContext(store, null))
    expect(result.current).toBeNull()
  })
})
