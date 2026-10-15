import { useEffect, useRef, useState, type ReactElement } from 'react'
import { ActionCard } from './ActionCard'
import { confirmActionDraft } from '../../api/run-client'
import type { AssistantProfile } from '../../types/assistant-profile'
import { visibleFocusables } from '../../lib/focus-trap'

export type AttachAssistantDialogProps = {
  turnId: string
  index: number
  profileId: string
  stageId: string
  title: string
  instruction: string
  profiles: AssistantProfile[]
  stages: { id: string; title: string }[]
  // maxDepth — effective_max_depth from GET /api/assistant-profiles. Этап 4 has no recursion (Этап 4a):
  // this is always a top-level attach (depth=1), so remaining depth after it is maxDepth - 1.
  maxDepth: number
  onConfirmed: (taskId: string) => void
  onCancel: () => void
}

// AttachAssistantDialog — the modal wrapper around ActionCard: confirms (or cancels) one action draft a
// side-agent turn proposed (dev-plan.md T4.6). Mirrors FileBrowserModal's Escape/Tab-wrap focus-trap
// (visibleFocusables) rather than reinventing one.
export function AttachAssistantDialog({
  turnId, index, profileId, stageId, title, instruction, profiles, stages, maxDepth, onConfirmed, onCancel,
}: AttachAssistantDialogProps): ReactElement {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const modalRef = useRef<HTMLDivElement | null>(null)

  const roleTitle = profiles.find((p) => p.id === profileId)?.title ?? profileId
  const stageTitle = stages.find((s) => s.id === stageId)?.title ?? stageId
  const remainingDepth = maxDepth - 1
  const atDepthLimit = remainingDepth < 0

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
    confirmActionDraft(turnId, index)
      .then(({ taskId }) => onConfirmed(taskId))
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
          title={title}
          instruction={instruction}
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
            {submitting ? 'Attaching…' : 'Attach assistant'}
          </button>
        </div>
      </div>
    </div>
  )
}
