import { useEffect, useRef, useState, type ReactElement } from 'react'
import { ActionCard } from './ActionCard'
import { attachAndSendMesh, genMeshRequestId, type MeshAttachAndSendResult } from '../../api/mesh-client'
import type { AssistantProfile } from '../../types/assistant-profile'
import { visibleFocusables } from '../../lib/focus-trap'

export type AttachAssistantDialogProps = {
  profileId: string
  stageId: string
  // title — optional task title; an empty/omitted value falls back to the profile's own title for display,
  // and is left out of the request so the server picks its own default (mirrors AddRunAgentDialog's own
  // "optional, trimmed" title field).
  title?: string
  // instruction — the assignment text the human typed. Sent as BOTH the task's Instruction and its
  // FirstMessage: this is a single free-form message, not two separate inputs, so one string drives both.
  instruction: string
  mode?: 'advisory' | 'required'
  profiles: AssistantProfile[]
  stages: { id: string; title: string }[]
  // maxDepth — effective_max_depth from GET /api/assistant-profiles, when the caller has it (dev-plan.md
  // T4.6, §12.4). Optional: the Feed's attach preview (T7.5) does not thread this through yet, and ActionCard
  // simply omits the depth row rather than showing a misleading value.
  maxDepth?: number
  onConfirmed: (result: MeshAttachAndSendResult) => void
  onCancel: () => void
}

// AttachAssistantDialog — the modal wrapper around ActionCard: previews, then confirms (or cancels), one
// attach-and-send to a configured profile (dev-plan.md T4.6/T7.5). "Attach and send" calls the T6.2-backed
// composite (attachAndSendMesh → POST /api/mesh/attach-and-send) directly — NOT confirmActionDraft, which was
// this dialog's original Side-agent-draft-confirm path (Этап 4a) and has no other live caller today (T7.5's
// job folds that unmounted flow into the mesh composite rather than keeping two). Mirrors FileBrowserModal's
// Escape/Tab-wrap focus-trap (visibleFocusables) rather than reinventing one.
export function AttachAssistantDialog({
  profileId, stageId, title, instruction, mode, profiles, stages, maxDepth, onConfirmed, onCancel,
}: AttachAssistantDialogProps): ReactElement {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const modalRef = useRef<HTMLDivElement | null>(null)
  // requestIdRef — minted ONCE when the dialog opens (mount) and reused on every retry within this same
  // preview session, same pattern as AddRunAgentDialog's own requestIdRef: a slow response followed by a
  // second click (or a retry after a server error) must not mint a second durable attach.
  const requestIdRef = useRef(genMeshRequestId('mesh-attach-preview'))

  const profile = profiles.find((p) => p.id === profileId)
  const roleTitle = profile?.title ?? profileId
  const executor = profile?.command ?? '—'
  const stageTitle = stages.find((s) => s.id === stageId)?.title ?? stageId
  const displayTitle = title !== undefined && title.trim() !== '' ? title : roleTitle
  const remainingDepth = maxDepth === undefined ? undefined : maxDepth - 1
  const atDepthLimit = remainingDepth !== undefined && remainingDepth < 0

  useEffect(() => {
    const modal = modalRef.current
    if (modal !== null) visibleFocusables(modal)[0]?.focus()
  }, [])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel()
        return
      }
      const modal = modalRef.current
      if (e.key !== 'Tab' || modal === null) return
      const items = visibleFocusables(modal)
      const first = items[0]
      const last = items[items.length - 1]
      if (first === undefined || last === undefined) return
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [onCancel])

  function handleConfirm(): void {
    if (submitting || atDepthLimit) return
    setSubmitting(true)
    setError(null)
    attachAndSendMesh({
      requestId: requestIdRef.current,
      profileId,
      scope: 'stage',
      stageId,
      ...(title !== undefined && title.trim() !== '' ? { title: title.trim() } : {}),
      ...(mode !== undefined ? { mode } : {}),
      instruction,
      firstMessage: instruction,
    })
      .then((result) => onConfirmed(result))
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'request failed')
        setSubmitting(false)
      })
  }

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Attach assistant">
      <div className="modal-content attach-assistant-dialog" ref={modalRef}>
        <ActionCard
          roleTitle={roleTitle}
          stageTitle={stageTitle}
          title={displayTitle}
          instruction={instruction}
          executor={executor}
          mode={mode}
          maxDepth={maxDepth}
          remainingDepth={remainingDepth}
          atDepthLimit={atDepthLimit}
        />
        {error !== null && (
          <div className="attach-assistant-error" role="alert">
            Attach failed: {error}
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={submitting || atDepthLimit}
            onClick={handleConfirm}
          >
            {submitting ? 'Attaching…' : 'Attach and send'}
          </button>
        </div>
      </div>
    </div>
  )
}
