import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactElement } from 'react'
import { FlowApiError, setStageNote } from '../../api/run-client'
import type { Stage } from '../../types'
import { PasteableTextarea } from '../pasteable-textarea'

// The part of a stage the note block depends on.
export type StageNoteStage = Pick<Stage, 'id' | 'status' | 'isScript' | 'preNote' | 'timing'>

export type StageNotePresentation = 'expanded' | 'compact'
// The user's choice together with whether the stage had started when it was made, so a stage that started since
// then gets its one automatic collapse.
export type StageNotePreference = { presentation: StageNotePresentation; started: boolean }

type StageNoteProps = {
  stage: StageNoteStage
  // initialDraft — the unsaved text the caller kept for this stage; present means the editor opens with it. Read only
  // on mount: the component keeps the live text itself, so typing does not re-render the caller.
  initialDraft?: string
  // onDraftChange mirrors the editor text to the caller's store; null drops the draft.
  onDraftChange: (stageId: string, text: string | null) => void
  // initialPresentation — the choice the caller remembered for this stage; read only on mount, like initialDraft.
  initialPresentation?: StageNotePreference
  // onPresentationChange mirrors the presentation to the caller's store; it is never called while typing.
  onPresentationChange?: (stageId: string, preference: StageNotePreference) => void
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

function ChevronIcon({ expanded }: { expanded: boolean }): ReactElement {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={expanded ? 'm6 9 6 6 6-6' : 'm9 6 6 6-6 6'} />
    </svg>
  )
}

// Older servers send no timing: any status past pending/ready then means the stage has started.
function hasStarted(stage: StageNoteStage): boolean {
  return stage.timing?.started ?? (stage.status !== 'pending' && stage.status !== 'ready')
}

function defaultPresentation(started: boolean): StageNotePresentation {
  return started ? 'compact' : 'expanded'
}

// Only a 409 means the note reached the disk; the body is plain text, so the UI words each failure by status.
function failureText(status: number | undefined, stageStillPending: boolean): string {
  if (status === 409) return SAVED_BUT_STARTED
  if (status === 400 && !stageStillPending) return NOT_SAVED_STARTED
  return NOT_SAVED
}

// The note a user attaches to a stage before it starts. Before the start it is editable; afterwards it is shown
// read only, and an editor left open turns read only with an explicit way to discard its draft. It opens as a card
// before the start and as a one-line row afterwards; the header button switches between the two.
export function StageNote({ stage, initialDraft, initialPresentation, onDraftChange, onPresentationChange, onChanged }: StageNoteProps): ReactElement | null {
  const [text, setText] = useState<string | null>(initialDraft ?? null)
  // Sticky: Retry puts a started stage back to pending, and a missing timing must not undo the start — not even
  // across a remount, hence the remembered fact.
  const [startSeen, setStartSeen] = useState(() => initialPresentation?.started === true || hasStarted(stage))
  const [presentation, setPresentation] = useState<StageNotePresentation>(() => {
    if (initialDraft !== undefined && !startSeen) return 'expanded'
    return initialPresentation?.started === startSeen ? initialPresentation.presentation : defaultPresentation(startSeen)
  })
  // The text just saved, with the preNote it replaced: the status refresh lags the save, and any other preNote wins.
  const [justSaved, setJustSaved] = useState<{ note: string; replaced: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ids = useId()
  const titleId = `${ids}-title`
  const helpId = `${ids}-help`
  const bodyId = `${ids}-body`
  const sectionRef = useRef<HTMLElement | null>(null)
  const toggleRef = useRef<HTMLButtonElement | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const editorRef = useRef<HTMLTextAreaElement | null>(null)
  const pendingFocus = useRef<'trigger' | 'editor' | 'toggle' | null>(null)
  const mounted = useRef(true)
  const latestStage = useRef(stage)
  latestStage.current = stage
  const startSeenRef = useRef(startSeen)
  startSeenRef.current = startSeen

  // Set on every setup: StrictMode runs the cleanup once and re-runs the effect on the same instance.
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  // Focus is requested together with the state change that renders its target, and applied once after that commit —
  // a request left over must not pull focus later.
  useEffect(() => {
    const targets = { trigger: triggerRef.current, editor: editorRef.current, toggle: toggleRef.current }
    const target = pendingFocus.current === null ? null : targets[pendingFocus.current]
    pendingFocus.current = null
    target?.focus({ preventScroll: true })
  })

  // The first observed start collapses the card once. Done while rendering, before the commit removes the old body,
  // so the focus check still sees where focus was; the draft, the error and a running save are not touched.
  if (!startSeen && hasStarted(stage)) {
    setStartSeen(true)
    setPresentation('compact')
    if (sectionRef.current?.contains(document.activeElement)) pendingFocus.current = 'toggle'
  }

  // Mirrors every presentation or start change to the caller; typing changes neither.
  useEffect(() => {
    onPresentationChange?.(stage.id, { presentation, started: startSeen })
  }, [stage.id, presentation, startSeen, onPresentationChange])

  // Retired for good once the server note moves on, so a later change by someone else is never masked.
  useEffect(() => {
    if (justSaved !== null && stage.preNote !== justSaved.replaced) setJustSaved(null)
  }, [justSaved, stage.preNote])

  const editable = stage.status === 'pending' && !stage.isScript
  const note = justSaved !== null && justSaved.replaced === stage.preNote ? justSaved.note : stage.preNote
  const hasNote = note !== ''
  const editing = text !== null
  if (!editable && !hasNote && !editing) return null

  function openEditor(): void {
    setPresentation('expanded')
    setText(note)
    onDraftChange(stage.id, note)
    pendingFocus.current = 'editor'
  }

  function closeEditor(focus: 'trigger' | 'toggle'): void {
    setPresentation(defaultPresentation(startSeen))
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
  async function save(value: string): Promise<void> {
    setSaving(true)
    setError(null)
    try {
      await setStageNote(stage.id, value)
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
    setJustSaved({ note: value, replaced: latestStage.current.preNote })
    setPresentation(defaultPresentation(startSeenRef.current))
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

  const expanded = presentation === 'expanded'
  // Collapsing is local, so it must not hide a live editor or an in-flight save; expanding is always allowed.
  const collapseLocked = expanded && ((editing && editable) || saving)
  const preview = note.replace(/\s+/g, ' ').trim()

  return (
    <div className="stage-note-wrap">
      <section
        ref={sectionRef}
        className={`stage-note${!hasNote && !editing ? ' stage-note-empty' : ''}`}
        data-presentation={presentation}
        aria-labelledby={titleId}
      >
        <div className="stage-note-heading">
          <h2 className="stage-note-title">
            <button
              ref={toggleRef}
              type="button"
              className="stage-note-toggle"
              aria-label={expanded ? 'Collapse stage note' : 'Expand stage note'}
              aria-expanded={expanded}
              aria-controls={bodyId}
              disabled={collapseLocked}
              onClick={() => setPresentation(expanded ? 'compact' : 'expanded')}
            >
              <ChevronIcon expanded={expanded} />
              <PencilIcon />
              <span id={titleId} className="stage-note-name">Stage note</span>
              {expanded && <span className="stage-note-subtitle">Before start</span>}
              {!expanded && preview !== '' && <span className="stage-note-preview">{preview}</span>}
            </button>
          </h2>
          {!editable && <span className="stage-note-badge">Read only</span>}
          {!editable && editing && <span className="stage-note-badge">Unsaved draft</span>}
          {editable && !editing && (
            <button ref={triggerRef} type="button" className="btn stage-note-action" onClick={openEditor}>
              {hasNote ? <PencilIcon /> : <PlusIcon />}
              {hasNote ? 'Edit note' : 'Add note'}
            </button>
          )}
        </div>

        <div id={bodyId} className="stage-note-body" hidden={!expanded}>
          {expanded && (
            <>
              {!editing && hasNote && <p className="stage-note-text">{note}</p>}
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
                      <button type="button" className="btn stage-note-button" disabled={saving} onClick={() => closeEditor('toggle')}>
                        Discard draft
                      </button>
                    </div>
                  </div>
                </>
              )}

              {editing && error !== null && <p className="stage-note-error" role="alert">{error}</p>}
            </>
          )}
        </div>
      </section>
    </div>
  )
}
