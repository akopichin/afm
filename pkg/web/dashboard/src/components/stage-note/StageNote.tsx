import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactElement } from 'react'
import { FlowApiError, setStageNote } from '../../api/run-client'
import type { Stage } from '../../types'
import { PasteableTextarea } from '../pasteable-textarea'

// The part of a stage the note block depends on.
export type StageNoteStage = Pick<Stage, 'id' | 'status' | 'isScript' | 'preNote'>

type StageNoteProps = {
  stage: StageNoteStage
  // initialDraft — the unsaved text the caller kept for this stage; present means the editor opens with it. Read only
  // on mount: the component keeps the live text itself, so typing does not re-render the caller.
  initialDraft?: string
  // onDraftChange mirrors the editor text to the caller's store; null drops the draft.
  onDraftChange: (stageId: string, text: string | null) => void
  // onChanged — the server was reached (saved, or the stage turned out to have started): reload the status.
  onChanged: () => void
}

const SAVED_BUT_STARTED = 'Saved, but the stage started at the same moment — the agent may not have received this note.'
const NOT_SAVED_STARTED = 'Not saved — the stage has already started.'
const NOT_SAVED = 'Could not save the note. Try again.'

function PencilIcon(): ReactElement {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
    </svg>
  )
}

function PlusIcon(): ReactElement {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
      <path d="M12 5v14M5 12h14" />
    </svg>
  )
}

// Only a 409 means the note reached the disk; the body is plain text, so the UI words each failure by status.
function failureText(status: number | undefined, stageStillPending: boolean): string {
  if (status === 409) return SAVED_BUT_STARTED
  if (status === 400 && !stageStillPending) return NOT_SAVED_STARTED
  return NOT_SAVED
}

// The note a user attaches to a stage before it starts. Before the start it is editable; afterwards it is shown
// read only, and an editor left open turns read only with an explicit way to discard its draft.
export function StageNote({ stage, initialDraft, onDraftChange, onChanged }: StageNoteProps): ReactElement | null {
  const [text, setText] = useState<string | null>(initialDraft ?? null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ids = useId()
  const titleId = `${ids}-title`
  const helpId = `${ids}-help`
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const editorRef = useRef<HTMLTextAreaElement | null>(null)
  const pendingFocus = useRef<'trigger' | 'editor' | null>(null)
  const mounted = useRef(true)
  const latestStage = useRef(stage)
  latestStage.current = stage

  // Set on every setup: StrictMode runs the cleanup once and re-runs the effect on the same instance.
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  // Focus is requested together with the state change that renders its target, and applied once after that commit —
  // a request left over must not pull focus later.
  useEffect(() => {
    const target = pendingFocus.current === 'trigger' ? triggerRef.current : pendingFocus.current === 'editor' ? editorRef.current : null
    pendingFocus.current = null
    target?.focus({ preventScroll: true })
  })

  const editable = stage.status === 'pending' && !stage.isScript
  const hasNote = stage.preNote !== ''
  const editing = text !== null
  if (!editable && !hasNote && !editing) return null

  function openEditor(): void {
    setText(stage.preNote)
    onDraftChange(stage.id, stage.preNote)
    pendingFocus.current = 'editor'
  }

  function closeEditor(focus: 'trigger' | null): void {
    setText(null)
    setError(null)
    onDraftChange(stage.id, null)
    pendingFocus.current = focus
  }

  function edit(value: string): void {
    setText(value)
    onDraftChange(stage.id, value)
  }

  // The request outlives the component if the user switches stage; only its UI effects are skipped then.
  async function save(note: string): Promise<void> {
    setSaving(true)
    setError(null)
    try {
      await setStageNote(stage.id, note)
    } catch (err) {
      const status = err instanceof FlowApiError ? err.status : undefined
      if (status === 400 || status === 409) onChanged()
      if (!mounted.current) return
      setError(failureText(status, latestStage.current.status === 'pending'))
      setSaving(false)
      pendingFocus.current = 'editor'
      return
    }
    onChanged()
    // A gone component must not clear the draft a newer instance of this stage may already hold.
    if (!mounted.current) return
    onDraftChange(stage.id, null)
    setText(null)
    setSaving(false)
    pendingFocus.current = 'trigger'
  }

  const trimmed = (text ?? '').trim()

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (event.nativeEvent.isComposing) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      closeEditor('trigger')
    } else if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      if (trimmed !== '') void save(trimmed)
    }
  }

  return (
    <div className="stage-note-wrap">
      <section className={`stage-note${!hasNote && !editing ? ' stage-note-empty' : ''}`} aria-labelledby={titleId}>
        <div className="stage-note-heading">
          <div className="stage-note-label">
            <PencilIcon />
            <h2 id={titleId}>Stage note</h2>
            <span>Before start</span>
          </div>
          {editable && !editing && (
            <button ref={triggerRef} type="button" className="btn stage-note-action" onClick={openEditor}>
              {hasNote ? <PencilIcon /> : <PlusIcon />}
              {hasNote ? 'Edit note' : 'Add note'}
            </button>
          )}
          {!editable && <span className="stage-note-readonly">Read only</span>}
        </div>

        {!editing && hasNote && <p className="stage-note-text">{stage.preNote}</p>}
        {!editing && editable && (
          <p className="stage-note-help">
            {hasNote
              ? 'The agent will receive this note when the stage starts. You can edit it until then.'
              : 'Give the agent extra context or constraints before this stage starts.'}
          </p>
        )}

        {editing && editable && (
          <>
            <PasteableTextarea
              stageId={stage.id}
              className="stage-note-input"
              value={text}
              onChange={edit}
              placeholder="Add context, constraints or checks for this stage…"
              disabled={saving}
              allowFileReferences
              textareaRef={editorRef}
              onKeyDown={handleKeyDown}
              textareaProps={{ 'aria-label': 'Stage note', 'aria-describedby': helpId }}
              toolbar={
                <>
                  <p id={helpId}>Included in the agent’s context when this stage starts. ⌘/Ctrl+Enter saves, Esc cancels.</p>
                  <div>
                    {hasNote && (
                      <button type="button" className="btn btn-cancel stage-note-button" disabled={saving} onClick={() => void save('')}>
                        Remove note
                      </button>
                    )}
                    <button type="button" className="btn stage-note-button" disabled={saving} onClick={() => closeEditor('trigger')}>
                      Cancel
                    </button>
                    <button type="button" className="btn btn-send stage-note-button" disabled={saving || trimmed === ''} onClick={() => void save(trimmed)}>
                      Save note
                    </button>
                  </div>
                </>
              }
            />
          </>
        )}

        {editing && !editable && (
          <>
            <p className="stage-note-help" id={helpId}>The stage has started, so this draft can no longer be saved.</p>
            <textarea
              className="stage-note-input"
              aria-label="Stage note"
              aria-describedby={helpId}
              value={text}
              readOnly
            />
            <div className="stage-note-editor-footer">
              <div>
                <button type="button" className="btn stage-note-button" disabled={saving} onClick={() => closeEditor(null)}>
                  Discard draft
                </button>
              </div>
            </div>
          </>
        )}

        {editing && error !== null && <p className="stage-note-error" role="alert">{error}</p>}
      </section>
    </div>
  )
}
