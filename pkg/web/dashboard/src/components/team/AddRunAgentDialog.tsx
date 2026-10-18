import { useEffect, useRef, useState, type ReactElement } from 'react'
import { attachAndSendMesh, type MeshAttachAndSendResult } from '../../api/mesh-client'
import { useRetryableSubmit } from '../../hooks/use-retryable-submit'
import type { AssistantProfile } from '../../types/assistant-profile'
import { visibleFocusables } from '../../lib/focus-trap'

export type AddRunAgentDialogProps = {
  profiles: AssistantProfile[]
  // onAttached — name is how the new agent should be shown: its title, else the role's title.
  onAttached: (result: MeshAttachAndSendResult, name: string) => void
  onCancel: () => void
}

// AddRunAgentDialog — the Team view's entry point for a STANDING run-scoped agent (as opposed to
// AttachHelperDialog, which attaches a task-scoped helper to one stage). Posts the SAME
// POST /api/mesh/attach-and-send the Feed's attach preview (T7.5) will call — this dialog composes nothing
// client-side, it only picks WHICH configured role to run; scope is always "run" with no stage id, the same
// convention orchestrator.AddRunAgent's own thin wrapper uses. Mirrors AttachHelperDialog's focus-trap
// (Escape/Tab via visibleFocusables) and modal-overlay/modal-content classes rather than reinventing them.
export function AddRunAgentDialog({ profiles, onAttached, onCancel }: AddRunAgentDialogProps): ReactElement {
  const available = profiles.filter((p) => p.available)
  const [profileId, setProfileId] = useState(available[0]?.id ?? '')
  const [title, setTitle] = useState('')
  const [instruction, setInstruction] = useState('')
  // firstMessage — optional; without one the agent is only registered and starts no work until it is sent one.
  const [firstMessage, setFirstMessage] = useState('')
  const modalRef = useRef<HTMLDivElement | null>(null)
  // submitter — a retry of the same agent reuses its request id (a slow response followed by a second click
  // must not mint a second durable attach); changing any field makes it a new request with a new id.
  const submitter = useRetryableSubmit<{ profileId: string; title: string; instruction: string; firstMessage: string }>('mesh-run-agent')

  const selected = available.find((p) => p.id === profileId) ?? null

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

  const canSubmit = profileId !== '' && instruction.trim() !== '' && !submitter.pending

  function handleSubmit(): void {
    if (!canSubmit) return
    const name = title.trim() !== '' ? title.trim() : (selected?.title ?? profileId)
    submitter.submit(
      { profileId, title: title.trim(), instruction: instruction.trim(), firstMessage: firstMessage.trim() },
      (p, requestId) =>
        attachAndSendMesh({
          requestId,
          profileId: p.profileId,
          scope: 'run',
          title: p.title,
          instruction: p.instruction,
          ...(p.firstMessage !== '' ? { firstMessage: p.firstMessage } : {}),
        }),
      { onSuccess: (result) => onAttached(result, name) },
    )
  }

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Add run agent">
      <div className="modal-content add-run-agent-dialog" ref={modalRef}>
        <h2 className="add-run-agent-heading">Add a run-scoped agent</h2>
        {available.length === 0 ? (
          <p className="add-run-agent-empty">No agent profiles are available for this run.</p>
        ) : (
          <>
            <label className="add-run-agent-field">
              <span>Role</span>
              <select value={profileId} onChange={(e) => setProfileId(e.target.value)}>
                {available.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.title}
                  </option>
                ))}
              </select>
            </label>
            {/* Read-only: the executor is decided by the profile's own configuration, never a per-request
                choice — there is no model selector here, same as MeshAttachAndSendRequest having no field
                for one (pkg/server/mesh.go's own comment on that struct). */}
            <dl className="add-run-agent-executor">
              <dt>Configured executor</dt>
              <dd>{selected?.command ?? '—'}</dd>
            </dl>
            <label className="add-run-agent-field">
              <span>Title (optional)</span>
              <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. Standing QA reviewer" />
            </label>
            <label className="add-run-agent-field">
              <span>Instruction</span>
              <textarea
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder="What should this run agent do?"
                rows={4}
              />
            </label>
            <label className="add-run-agent-field">
              <span>First message (optional)</span>
              <textarea
                value={firstMessage}
                onChange={(e) => setFirstMessage(e.target.value)}
                placeholder="Starts the agent right away — e.g. what to look at first"
                rows={2}
              />
            </label>
          </>
        )}
        {submitter.error !== null && (
          <div className="add-run-agent-error" role="alert">
            Add run agent failed: {submitter.error}
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn btn-send" disabled={!canSubmit} onClick={handleSubmit}>
            {submitter.pending ? 'Adding…' : 'Add run agent'}
          </button>
        </div>
      </div>
    </div>
  )
}
