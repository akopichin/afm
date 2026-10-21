import { useEffect, useRef, useState, type ReactElement } from 'react'
import { GraphOperationPreview, type GraphStagePreview } from './GraphOperationPreview'
import {
  commitAddStage,
  previewAddStage,
  FlowApiError,
  type AddStagePreviewView,
  type AddStageRequest,
} from '../../api/run-client'
import { useModalDialog } from '../../lib/focus-trap'

export type AddStageDialogProps = {
  // capabilityEnabled — GET /api/status's capabilities.add_stage. The caller fetches this itself (this
  // component never calls /api/status on its own, same posture AttachAssistantDialog takes toward
  // maxDepth/profiles/stages) and tells us whether the backend can actually accept a commit right now.
  capabilityEnabled: boolean
  // graphRevision — GET /api/status's graph_revision at the moment the dialog was opened. Used as the FIRST
  // preview's optimistic-concurrency check; a stale_revision rejection triggers an automatic unconditional
  // re-fetch (see runPreview below), after which the freshly observed revision takes over.
  graphRevision: number
  // stages — candidate stages this draft can reference: insert_before's target picker, and append's optional
  // explicit-dependency override. The caller decides which stages are offerable (e.g. "not yet started" for a
  // target) — this component does not re-derive that filter.
  stages: { id: string; title: string }[]
  onCommitted: (result: { stageId: string; graphRevision: number }) => void
  onCancel: () => void
}

type PreviewStatus = 'idle' | 'loading' | 'ready' | 'error'

function toGraphStagePreview(view: AddStagePreviewView): GraphStagePreview {
  return { id: view.id, dependsOn: view.depends_on, extraDeps: view.extra_deps, warnings: view.warnings }
}

function newRequestId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `add-stage-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// AddStageDialog — dev-plan.md T9.6's modal for inserting a dynamic "agents: [auto]" stage into the live run.
// Mirrors AttachAssistantDialog's modal mechanics (useModalDialog's focus-trap and Escape-to-cancel,
// modal-overlay/modal-content classes, immediate-disable double-submit guard) and its
// non-committal-until-explicit-confirm posture: nothing here mutates the run until "Add stage" is clicked, and
// everything up to that point is a read-only preview rendered through GraphOperationPreview.
export function AddStageDialog({ capabilityEnabled, graphRevision, stages, onCommitted, onCancel }: AddStageDialogProps): ReactElement {
  const [kind, setKind] = useState<'insert_before' | 'append'>('append')
  const [targetId, setTargetId] = useState('')
  const [explicitDeps, setExplicitDeps] = useState<string[]>([])
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [prompt, setPrompt] = useState('')

  const [previewStatus, setPreviewStatus] = useState<PreviewStatus>('idle')
  const [previewData, setPreviewData] = useState<GraphStagePreview | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)

  const [submitting, setSubmitting] = useState(false)
  const [commitError, setCommitError] = useState<string | null>(null)

  const modalRef = useRef<HTMLDivElement | null>(null)
  const previewGenRef = useRef(0)
  // revisionRef — the latest graph revision a successful preview observed (graph_mutate.go's "preview and
  // commit both re-validate against the CURRENT registry"). Starts from the caller-supplied snapshot; a
  // stale_revision rejection retries with 0 ("don't check") to force a fresh read instead of leaving a dead
  // error on screen (TestDialog_StaleRevisionShowsFreshPreview).
  const revisionRef = useRef(graphRevision)

  const canPreview =
    capabilityEnabled &&
    id.trim() !== '' &&
    command.trim() !== '' &&
    prompt.trim() !== '' &&
    (kind !== 'insert_before' || targetId !== '')

  function buildRequest(expectedRevision: number, requestId?: string): AddStageRequest {
    const req: AddStageRequest = {
      kind,
      id: id.trim(),
      command: command.trim(),
      prompt,
      expected_revision: expectedRevision,
    }
    if (name.trim() !== '') req.name = name.trim()
    if (kind === 'insert_before') {
      req.target_id = targetId
    } else if (explicitDeps.length > 0) {
      req.depends_on = explicitDeps
    }
    if (requestId !== undefined) req.request_id = requestId
    return req
  }

  function runPreview(expectedRevision: number): void {
    const myGen = ++previewGenRef.current
    setPreviewStatus('loading')
    setPreviewError(null)
    previewAddStage(buildRequest(expectedRevision))
      .then((res) => {
        if (myGen !== previewGenRef.current) return
        revisionRef.current = res.graph_revision
        setPreviewData(toGraphStagePreview(res.stage))
        setPreviewStatus('ready')
      })
      .catch((err: unknown) => {
        if (myGen !== previewGenRef.current) return
        if (err instanceof FlowApiError && err.code === 'stale_revision') {
          // The draft was prepared against a revision that has since moved — re-fetch unconditionally rather
          // than show the user a dead error for a round trip they never asked to retry.
          runPreview(0)
          return
        }
        setPreviewData(null)
        setPreviewStatus('error')
        setPreviewError(err instanceof Error ? err.message : 'preview failed')
      })
  }

  useEffect(() => {
    if (!canPreview) {
      previewGenRef.current += 1 // invalidate any in-flight preview for the now-incomplete draft
      setPreviewStatus('idle')
      setPreviewData(null)
      setPreviewError(null)
      return
    }
    runPreview(revisionRef.current)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- revisionRef is a ref, intentionally not a dep
  }, [canPreview, kind, targetId, id, name, command, prompt, explicitDeps.join(',')])

  useModalDialog(modalRef, onCancel)

  function handleConfirm(): void {
    if (submitting || !capabilityEnabled || previewStatus !== 'ready') return
    setSubmitting(true)
    setCommitError(null)
    commitAddStage(buildRequest(revisionRef.current, newRequestId()))
      .then((res) => {
        onCommitted({ stageId: res.stage.id, graphRevision: res.graph_revision })
      })
      .catch((err: unknown) => {
        if (err instanceof FlowApiError && err.code === 'stale_revision') {
          setCommitError('The run changed since this preview — refreshing the preview.')
          setSubmitting(false)
          runPreview(0)
          return
        }
        setCommitError(err instanceof Error ? err.message : 'request failed')
        setSubmitting(false)
      })
  }

  function toggleExplicitDep(stageId: string, checked: boolean): void {
    setExplicitDeps((prev) => (checked ? [...prev, stageId] : prev.filter((s) => s !== stageId)))
  }

  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Add stage">
      <div className="modal-content add-stage-dialog" ref={modalRef}>
        <h2 className="add-stage-dialog-title">Add stage</h2>

        {!capabilityEnabled && (
          <p className="add-stage-dialog-disabled-note">This run does not support adding stages right now.</p>
        )}

        <fieldset className="add-stage-dialog-kind" disabled={!capabilityEnabled}>
          <legend>Where</legend>
          <label>
            <input
              type="radio"
              name="add-stage-kind"
              value="insert_before"
              checked={kind === 'insert_before'}
              onChange={() => setKind('insert_before')}
            />
            Insert before an existing stage
          </label>
          <label>
            <input
              type="radio"
              name="add-stage-kind"
              value="append"
              checked={kind === 'append'}
              onChange={() => setKind('append')}
            />
            Append to the end
          </label>
        </fieldset>

        {kind === 'insert_before' && (
          <label className="add-stage-dialog-field">
            Target stage
            <select disabled={!capabilityEnabled} value={targetId} onChange={(e) => setTargetId(e.target.value)}>
              <option value="">Select a stage…</option>
              {stages.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.title}
                </option>
              ))}
            </select>
          </label>
        )}

        {kind === 'append' && stages.length > 0 && (
          <fieldset className="add-stage-dialog-deps" disabled={!capabilityEnabled}>
            <legend>Wait for (optional — defaults to the current end of the flow)</legend>
            {stages.map((s) => (
              <label key={s.id}>
                <input
                  type="checkbox"
                  checked={explicitDeps.includes(s.id)}
                  onChange={(e) => toggleExplicitDep(s.id, e.target.checked)}
                />
                {s.title}
              </label>
            ))}
          </fieldset>
        )}

        <label className="add-stage-dialog-field">
          Stage id
          <input type="text" disabled={!capabilityEnabled} value={id} onChange={(e) => setId(e.target.value)} />
        </label>
        <label className="add-stage-dialog-field">
          Name (optional)
          <input type="text" disabled={!capabilityEnabled} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="add-stage-dialog-field">
          Command
          <input type="text" disabled={!capabilityEnabled} value={command} onChange={(e) => setCommand(e.target.value)} />
        </label>
        <label className="add-stage-dialog-field">
          Prompt
          <textarea disabled={!capabilityEnabled} value={prompt} onChange={(e) => setPrompt(e.target.value)} />
        </label>

        <div className="add-stage-dialog-preview">
          {previewStatus === 'idle' && <p className="add-stage-dialog-preview-hint">Fill in the draft to see what it changes.</p>}
          {previewStatus === 'loading' && <p className="add-stage-dialog-preview-loading">Checking…</p>}
          {previewStatus === 'error' && (
            <p className="add-stage-dialog-preview-error" role="alert">
              Preview failed: {previewError}
            </p>
          )}
          {previewStatus === 'ready' && previewData !== null && (
            <GraphOperationPreview kind={kind} targetId={kind === 'insert_before' ? targetId : undefined} preview={previewData} />
          )}
        </div>

        {commitError !== null && (
          <div className="add-stage-dialog-error" role="alert">
            {commitError}
          </div>
        )}

        <div className="modal-actions">
          <button type="button" className="btn btn-cancel" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-send"
            disabled={!capabilityEnabled || submitting || previewStatus !== 'ready'}
            onClick={handleConfirm}
          >
            {submitting ? 'Adding…' : 'Add stage'}
          </button>
        </div>
      </div>
    </div>
  )
}
