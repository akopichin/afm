import type { ReactElement } from 'react'

export type ActionCardProps = {
  roleTitle: string
  stageTitle: string
  title: string
  instruction: string
  // maxDepth/remainingDepth — effective_max_depth and the depth budget left AFTER this task is created
  // (dev-plan.md T4.6, §12.4). Этап 4 scope has no recursion yet (Этап 4a), so this is always a top-level
  // attach: remainingDepth = maxDepth - 1.
  maxDepth: number
  remainingDepth: number
  atDepthLimit: boolean
}

// ActionCard — the read-only policy summary inside AttachAssistantDialog: everything §12.4 requires a
// confirmation card to show before a helper task is actually created.
export function ActionCard({ roleTitle, stageTitle, title, instruction, maxDepth, remainingDepth, atDepthLimit }: ActionCardProps): ReactElement {
  return (
    <div className="action-card">
      <dl className="action-card-fields">
        <dt>Role</dt>
        <dd>{roleTitle}</dd>
        <dt>Stage</dt>
        <dd>{stageTitle}</dd>
        <dt>Task</dt>
        <dd>{title}</dd>
        <dt>Instruction</dt>
        <dd>{instruction}</dd>
        <dt>Mode</dt>
        <dd>Advisory</dd>
        <dt>Trigger</dt>
        <dd>One-shot</dd>
        <dt>Delegation depth</dt>
        <dd>
          {maxDepth} max, {remainingDepth} remaining after this task
        </dd>
      </dl>
      {atDepthLimit ? (
        <p className="action-card-note action-card-note-warning" role="alert">
          This run is at its delegation depth limit — attaching a helper here is not allowed.
        </p>
      ) : (
        <p className="action-card-note">
          Any helper this task delegates to within its own work won&apos;t need a separate click per child.
        </p>
      )}
    </div>
  )
}
