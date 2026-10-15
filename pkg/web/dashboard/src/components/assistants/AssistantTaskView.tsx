import { useEffect, useRef, useState, type ReactElement } from 'react'
import { getAssistantTaskEvents, type AssistantEventWire } from '../../api/run-client'
import {
  ASSISTANT_TASK_STATUS_LABELS,
  ASSISTANT_TASK_TERMINAL,
  parseAssistantResult,
  verdictGlyph,
  verdictLabel,
  type AssistantResult,
  type AssistantTask,
} from '../../types/assistant-task'
import { MarkdownRenderer } from '../plan-panel'

const POLL_INTERVAL_MS = 3000

// latestResult scans a task's event page, newest first, for the latest entry whose payload already matches
// assistants.Result's shape (verdict + summary, see pkg/assistants/types.go). Today's committed backend
// (T3.7/T3.8) never actually emits such a payload over GET /api/assistants/{id}/events — a completed task's
// structured result (verdict/summary/findings) is persisted to result.json on disk, not published as a
// durable task event (see pkg/orchestrator/assistants_lifecycle.go's persistTaskResult). This stays a no-op
// against the real backend today, but the feed is ready the moment a future change starts including it,
// without a second parser to write then — and it's exactly what lets this view's "completed result" rendering
// be driven honestly in tests right now.
function latestResult(events: AssistantEventWire[]): AssistantResult | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const parsed = parseAssistantResult(events[i]?.data)
    if (parsed !== null) return parsed
  }
  return null
}

// useTaskResult polls a task's event history and extracts the latest decodable result from it (see
// latestResult above). Exported for direct testing alongside the view it backs.
export function useTaskResult(taskId: string | null): AssistantResult | null {
  const [result, setResult] = useState<AssistantResult | null>(null)
  const seq = useRef(0)

  useEffect(() => {
    setResult(null)
    if (taskId === null) return

    const mySeq = ++seq.current
    let cancelled = false
    const load = (): void => {
      // Этап 3 scope: a task's whole event history (creation + a handful of status changes) comfortably
      // fits the server's default page size — always re-reading from cursor 0 is simpler than threading
      // pagination state through a 3s poll for a page that never realistically grows past it.
      getAssistantTaskEvents(taskId, 0)
        .then(({ events }) => {
          if (cancelled || seq.current !== mySeq) return
          setResult(latestResult(events))
        })
        .catch(() => {
          /* transient network error — keep whatever result we already had, next tick retries */
        })
    }

    load()
    const timer = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [taskId])

  return result
}

type AssistantTaskViewProps = {
  // task — the selected AssistantTask, looked up by the caller from useStageAssistants' summary (this
  // component does not fetch the task list itself — its own concern is one task's result feed).
  task: AssistantTask | null
}

// AssistantTaskView — the workspace body for the 'assistant' WorkspaceView (T3.10): one helper task's
// current state. Three states, exactly as dev-plan.md's T3.10 tests enumerate: empty (no task selected, or
// still running with nothing to show yet), error (failed/cancelled), and completed-with-result (rendered
// through the SAME safe markdown renderer the plan panel uses — never a second dangerouslySetInnerHTML).
export function AssistantTaskView({ task }: AssistantTaskViewProps): ReactElement {
  const result = useTaskResult(task?.id ?? null)

  if (task === null) {
    return (
      <div className="assistant-task-view">
        <div className="assistant-task-view-empty empty-hint">Select a helper task to see its feed</div>
      </div>
    )
  }

  const label = ASSISTANT_TASK_STATUS_LABELS[task.status]
  const isError = task.status === 'failed' || task.status === 'cancelled'
  const isTerminal = ASSISTANT_TASK_TERMINAL.has(task.status)
  const title = task.title !== '' ? task.title : task.profileId

  return (
    <div className="assistant-task-view">
      <div className="assistant-task-view-header">
        <span className="assistant-task-view-title">{title}</span>
        {/* Required/advisory is text, not just color — same accessibility rule as the rail row. */}
        <span className="assistant-task-view-mode" data-mode={task.mode}>
          {task.mode === 'required' ? 'Required' : 'Advisory'}
        </span>
        <span className="assistant-task-view-status" data-status={task.status}>{label}</span>
      </div>

      {isError ? (
        <div className="assistant-task-view-error" role="alert">
          <span className="assistant-task-view-error-icon" aria-hidden="true">⚠</span>
          <span>
            {task.statusReason !== ''
              ? task.statusReason
              : task.status === 'cancelled'
                ? 'This helper task was cancelled.'
                : 'This helper task failed.'}
          </span>
        </div>
      ) : result !== null ? (
        <div className="assistant-task-result">
          {/* Verdict is text + icon, never color alone — same rule as required/advisory above. */}
          <div className="assistant-task-verdict" data-verdict={result.verdict}>
            <span aria-hidden="true">{verdictGlyph(result.verdict)}</span>
            <span>{verdictLabel(result.verdict)}</span>
          </div>
          <MarkdownRenderer source={result.summary} />
          {result.findings.length > 0 && (
            <ul className="assistant-task-findings">
              {result.findings.map((f) => (
                <li key={f.id} className="assistant-task-finding" data-severity={f.severity}>
                  <span className="assistant-task-finding-severity">{f.severity}</span>
                  {f.path !== null && (
                    <span className="assistant-task-finding-path">
                      {f.path}
                      {f.lineStart !== null ? `:${f.lineStart}` : ''}
                    </span>
                  )}
                  <span className="assistant-task-finding-detail">{f.detail}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : isTerminal ? (
        <div className="assistant-task-view-empty empty-hint">
          {task.statusReason !== '' ? task.statusReason : 'Completed. No detailed result was reported.'}
        </div>
      ) : (
        <div className="assistant-task-view-empty empty-hint">
          <span className="assistant-task-view-spinner" aria-hidden="true" />
          Waiting for the first result…
        </div>
      )}
    </div>
  )
}
