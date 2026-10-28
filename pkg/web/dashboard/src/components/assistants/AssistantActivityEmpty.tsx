import type { ReactElement } from 'react'
import type { ActivityStatus } from '../../hooks/use-assistant-activity'
import type { AssistantTaskStatus } from '../../types/assistant-task'

// What the feed shows while it has no rows. Each state says only what is known: "waiting" is not claimed for a
// helper that never started, and "no history" is not claimed while the first page is still loading.
type AssistantActivityEmptyProps = { status: ActivityStatus; taskStatus: AssistantTaskStatus; terminal: boolean; onRetry: () => void }

export function AssistantActivityEmpty({ status, taskStatus, terminal, onRetry }: AssistantActivityEmptyProps): ReactElement {
  switch (status) {
    case 'loading':
      return <div className="assistant-task-view-empty empty-hint">Loading activity…</div>
    case 'error':
      return (
        <div className="assistant-task-view-empty empty-hint" role="alert">
          {terminal ? 'Could not load activity.' : 'Could not load activity. Retrying…'}
          <button type="button" className="assistant-activity-retry" onClick={onRetry}>Retry</button>
        </div>
      )
    case 'unavailable':
      return <div className="assistant-task-view-empty empty-hint">Activity is not available for this task.</div>
    case 'ready':
      break
  }
  if (terminal) {
    return (
      <div className="assistant-task-view-empty empty-hint">
        No activity was recorded for this task. Runs made before activity history existed have none.
      </div>
    )
  }
  if (taskStatus === 'running') {
    return (
      <div className="assistant-task-view-empty empty-hint">
        <span className="assistant-task-view-spinner" aria-hidden="true" />
        Waiting for the first message or tool call…
      </div>
    )
  }
  return <div className="assistant-task-view-empty empty-hint">No activity yet.</div>
}
