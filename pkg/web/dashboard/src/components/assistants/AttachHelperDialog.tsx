import { useRef, useState, type ReactElement } from 'react'
import { attachAssistant, type AttachHelperRequest } from '../../api/run-client'
import { describeExecutor, describeUnavailable, type AssistantProfile } from '../../types/assistant-profile'
import { useModalDialog } from '../../lib/focus-trap'

export type AttachHelperDialogProps = {
  stageId: string
  stageTitle: string
  // profiles — resolved in this stage's context, so the executor shown is the one the helper would run on.
  profiles: AssistantProfile[]
  onAttached: (taskId: string) => void
  onCancel: () => void
}

// AttachHelperDialog — the human entry point for attaching a helper task to a stage (POST
// /api/stages/{id}/assistants). Distinct from AttachAssistantDialog, which only CONFIRMS an action draft a
// side-agent turn already proposed (confirmActionDraft); this one creates a fresh top-level task from the
// operator's own choices. Focus, Escape and Tab-wrap come from the shared useModalDialog.
export function AttachHelperDialog({ stageId, stageTitle, profiles, onAttached, onCancel }: AttachHelperDialogProps): ReactElement {
  const available = profiles.filter((p) => p.available)
  const [chosenId, setChosenId] = useState('')
  const [title, setTitle] = useState('')
  const [instruction, setInstruction] = useState('')
  const [mode, setMode] = useState<AttachHelperRequest['mode']>('advisory')
  const [trigger, setTrigger] = useState<AttachHelperRequest['trigger']>('one_shot')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const modalRef = useRef<HTMLDivElement | null>(null)

  useModalDialog(modalRef, onCancel)

  // Profiles load after the dialog opens, so the choice falls back to the first available one until picked.
  const selected = available.find((p) => p.id === chosenId) ?? available[0] ?? null
  const canSubmit = selected !== null && instruction.trim() !== '' && !submitting

  function handleSubmit(): void {
    if (!canSubmit) return
    const profileId = selected.id
    setSubmitting(true)
    setError(null)
    attachAssistant(stageId, { profileId, title: title.trim(), instruction: instruction.trim(), mode, trigger })
      .then(({ taskId }) => onAttached(taskId))
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'request failed')
        setSubmitting(false)
      })
  }

  const unavailable = describeUnavailable(profiles)

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Attach helper">
      <div className="modal-content attach-helper-dialog" ref={modalRef}>
        <h2 className="attach-helper-heading">Attach helper to {stageTitle}</h2>
        {available.length === 0 ? (
          <p className="attach-helper-empty">
            No helper profiles are available for this stage.
            {unavailable !== '' && ` Unavailable — ${unavailable}.`}
          </p>
        ) : (
          <>
            <label className="attach-helper-field">
              <span>Role</span>
              <select value={selected?.id ?? ''} onChange={(e) => setChosenId(e.target.value)}>
                {available.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.title}
                  </option>
                ))}
              </select>
            </label>
            <dl className="add-run-agent-executor">
              <dt>Configured executor</dt>
              <dd>{selected === null ? '—' : describeExecutor(selected)}</dd>
            </dl>
            <label className="attach-helper-field">
              <span>Title (optional)</span>
              <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. Correctness review" />
            </label>
            <label className="attach-helper-field">
              <span>Instruction</span>
              <textarea
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder="What should this helper do?"
                rows={4}
              />
            </label>
            <div className="attach-helper-row">
              <label className="attach-helper-field">
                <span>Mode</span>
                <select value={mode} onChange={(e) => setMode(e.target.value as AttachHelperRequest['mode'])}>
                  <option value="advisory">Advisory (does not block the stage)</option>
                  <option value="required">Required (must pass to finish the stage)</option>
                </select>
              </label>
              <label className="attach-helper-field">
                <span>Trigger</span>
                <select value={trigger} onChange={(e) => setTrigger(e.target.value as AttachHelperRequest['trigger'])}>
                  <option value="one_shot">One shot</option>
                  <option value="on_completion">On stage completion</option>
                  <option value="checkpoints">At checkpoints</option>
                </select>
              </label>
            </div>
          </>
        )}
        {error !== null && (
          <div className="attach-helper-error" role="alert">
            Attach failed: {error}
          </div>
        )}
        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn btn-send" disabled={!canSubmit} onClick={handleSubmit}>
            {submitting ? 'Attaching…' : 'Attach helper'}
          </button>
        </div>
      </div>
    </div>
  )
}
