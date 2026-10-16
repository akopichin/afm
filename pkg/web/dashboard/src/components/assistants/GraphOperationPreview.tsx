import type { ReactElement } from 'react'

// GraphStagePreview — the app-facing (camelCase) shape of one resolved graph.InsertionPlan, decoupled from
// the wire's AddStagePreviewView (src/api/run-client.ts) so this component stays reusable outside the specific
// fetch flow AddStageDialog drives it with.
export type GraphStagePreview = {
  id: string
  dependsOn: string[]
  extraDeps?: Record<string, string[]>
  warnings?: string[]
}

export type GraphOperationPreviewProps = {
  kind: 'insert_before' | 'append'
  // targetId — insert_before only: the stage the new one is inserted before, and the key into
  // preview.extraDeps for the dependency it gains.
  targetId?: string
  preview: GraphStagePreview
}

function DepList({ ids, emptyLabel, addedIds }: { ids: string[]; emptyLabel: string; addedIds?: Set<string> }): ReactElement {
  if (ids.length === 0) {
    return <p className="graph-preview-deps-empty">{emptyLabel}</p>
  }
  return (
    <ul className="graph-preview-deps">
      {ids.map((id) => (
        <li key={id} className="graph-preview-dep" data-added={addedIds?.has(id) ? 'true' : undefined}>
          {id}
        </li>
      ))}
    </ul>
  )
}

function Warnings({ warnings }: { warnings?: string[] }): ReactElement | null {
  if (!warnings || warnings.length === 0) return null
  return (
    <ul className="graph-preview-warnings" role="alert">
      {warnings.map((w) => (
        <li key={w} className="graph-preview-warning">
          {w}
        </li>
      ))}
    </ul>
  )
}

// GraphOperationPreview is the read-only summary of what POST /api/run/graph/{preview|commit} will actually
// change (dev-plan.md §11.3): for insert_before this is the dependency DIFF of the target stage — what it
// depended on BEFORE the new stage existed vs. AFTER — not merely "a new row will appear in the list"
// (TestDialog_ShowsDependencyDiffNotJustPosition). For append it is the explicit list of stages the new one
// will wait for, with any non-fatal warnings (e.g. a failed/blocked dependency) flagged visibly rather than
// buried in prose (TestDialog_ShowsWhatNewStageWillWaitFor). Pure presentational: props in, JSX out.
export function GraphOperationPreview({ kind, targetId, preview }: GraphOperationPreviewProps): ReactElement {
  if (kind === 'insert_before') {
    const before = preview.dependsOn
    const added = (targetId !== undefined ? preview.extraDeps?.[targetId] : undefined) ?? []
    const after = [...before, ...added]
    const addedIds = new Set(added)
    return (
      <div className="graph-preview" data-kind="insert_before">
        <p className="graph-preview-summary">
          New stage <code>{preview.id}</code> will be inserted before <code>{targetId}</code>.
        </p>
        <div className="graph-preview-diff">
          <div className="graph-preview-diff-col" data-side="before">
            <div className="graph-preview-diff-label">Before — {targetId} depends on</div>
            <DepList ids={before} emptyLabel="nothing" />
          </div>
          <div className="graph-preview-diff-arrow" aria-hidden="true">
            →
          </div>
          <div className="graph-preview-diff-col" data-side="after">
            <div className="graph-preview-diff-label">After — {targetId} depends on</div>
            <DepList ids={after} emptyLabel="nothing" addedIds={addedIds} />
          </div>
        </div>
        <Warnings warnings={preview.warnings} />
      </div>
    )
  }

  return (
    <div className="graph-preview" data-kind="append">
      <p className="graph-preview-summary">
        New stage <code>{preview.id}</code> will wait for:
      </p>
      <DepList ids={preview.dependsOn} emptyLabel="nothing — it starts immediately" />
      <Warnings warnings={preview.warnings} />
    </div>
  )
}
