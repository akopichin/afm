// composer-draft-store — the run's drafts, one per stage, outside React: a send that settles after its composer
// unmounted still applies, and a remounted composer reads the current draft, not an old copy.

import { useCallback, useSyncExternalStore, type MutableRefObject } from 'react'
import type { AttachmentActions, ImagePasteDraft } from '../../hooks/use-image-paste'
import { EMPTY_COMPOSER_DRAFT, completeSend, failSend, patchDraft, type ComposerDraft, type ReplyContext, type SendSnapshot } from './composer-draft'

export type ComposerDraftStore = {
  get: (stageId: string) => ComposerDraft
  // update — applies fn to the stage's draft synchronously and returns the result; listeners hear only a change.
  update: (stageId: string, fn: (draft: ComposerDraft) => ComposerDraft) => ComposerDraft
  subscribe: (stageId: string, listener: () => void) => () => void
  // registerAttachments — the mounted composer's attachment actions: while they exist they own the attachment
  // list, so a discard goes through them. The returned unregister removes only this registration.
  registerAttachments: (stageId: string, actions: MutableRefObject<AttachmentActions | null>) => () => void
  // discardAttachments — drops sent attachments and revokes their previews, wherever they currently live.
  discardAttachments: (stageId: string, sent: ImagePasteDraft) => void
  // revokeAll — releases every preview of a store the run left behind.
  revokeAll: () => void
}

export function createComposerDraftStore(): ComposerDraftStore {
  const drafts = new Map<string, ComposerDraft>()
  const listeners = new Map<string, Set<() => void>>()
  const live = new Map<string, MutableRefObject<AttachmentActions | null>>()

  const get = (stageId: string): ComposerDraft => drafts.get(stageId) ?? EMPTY_COMPOSER_DRAFT

  function update(stageId: string, fn: (draft: ComposerDraft) => ComposerDraft): ComposerDraft {
    const current = get(stageId)
    const next = fn(current)
    if (next === current) return current
    drafts.set(stageId, next)
    listeners.get(stageId)?.forEach((listener) => listener())
    return next
  }

  function subscribe(stageId: string, listener: () => void): () => void {
    const set = listeners.get(stageId) ?? new Set()
    set.add(listener)
    listeners.set(stageId, set)
    return () => {
      set.delete(listener)
    }
  }

  function registerAttachments(stageId: string, actions: MutableRefObject<AttachmentActions | null>): () => void {
    live.set(stageId, actions)
    return () => {
      if (live.get(stageId) === actions) live.delete(stageId)
    }
  }

  function discardAttachments(stageId: string, sent: ImagePasteDraft): void {
    if (sent.length === 0) return
    const actions = live.get(stageId)?.current
    if (actions !== null && actions !== undefined) {
      actions.discard(sent)
      return
    }
    const urls = new Set(sent.map((a) => a.previewUrl))
    update(stageId, (d) => patchDraft(d, { attachments: d.attachments.filter((a) => !urls.has(a.previewUrl)) }))
    urls.forEach((url) => URL.revokeObjectURL(url))
  }

  function revokeAll(): void {
    drafts.forEach((d) => d.attachments.forEach((a) => URL.revokeObjectURL(a.previewUrl)))
  }

  return { get, update, subscribe, registerAttachments, discardAttachments, revokeAll }
}

// settleSuccess — applies a send's success to the store it was sent from, even with no composer mounted (a
// request of a previous run lands in that run's store), and drops the sent attachments the text no longer shows.
export function settleSuccess(store: ComposerDraftStore, stageId: string, snap: SendSnapshot): void {
  let discard: ImagePasteDraft = []
  store.update(stageId, (d) => {
    const result = completeSend(d, snap)
    discard = result.discard
    return result.draft
  })
  store.discardAttachments(stageId, discard)
}

export function settleFailure(store: ComposerDraftStore, stageId: string, snap: SendSnapshot, error: string): void {
  store.update(stageId, (d) => failSend(d, snap, error))
}

// useComposerDraft — the stage's draft; the composer re-renders with every change of it.
export function useComposerDraft(store: ComposerDraftStore, stageId: string): ComposerDraft {
  const subscribe = useCallback((listener: () => void) => store.subscribe(stageId, listener), [store, stageId])
  const getSnapshot = useCallback(() => store.get(stageId), [store, stageId])
  return useSyncExternalStore(subscribe, getSnapshot)
}

// useComposerContext — only the stage's reply context: the feed does not re-render while the text is typed.
export function useComposerContext(store: ComposerDraftStore, stageId: string | null): ReplyContext | null {
  const subscribe = useCallback((listener: () => void) => (stageId === null ? () => {} : store.subscribe(stageId, listener)), [store, stageId])
  const getSnapshot = useCallback(() => (stageId === null ? null : store.get(stageId).context), [store, stageId])
  return useSyncExternalStore(subscribe, getSnapshot)
}
