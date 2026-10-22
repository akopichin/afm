import { useEffect, useRef, useState, type ReactElement, type SyntheticEvent } from 'react'
import {
  cancelAssistantTask,
  continueAssistantTask,
  getAssistantTaskEvents,
  getAssistantTaskRaw,
  pauseAssistantTask,
  waiveAssistantTask,
  type AssistantEventWire,
} from '../../api/run-client'
import {
  ASSISTANT_TASK_STATUS_LABELS,
  ASSISTANT_TASK_TERMINAL,
  buildBreadcrumbChain,
  parseAssistantResult,
  verdictGlyph,
  verdictLabel,
  type AssistantResult,
  type AssistantTask,
} from '../../types/assistant-task'
import { AssistantBreadcrumb } from './AssistantBreadcrumb'
import { MarkdownRenderer } from '../plan-panel'

const POLL_INTERVAL_MS = 3000
// MAX_ACCUMULATED_EVENTS bounds useTaskResult's in-memory accumulated event history (T11.2, "bounded
// memory") — a long-lived watcher task (checkpoints/on_completion, Этап 5) can accumulate many pages over a
// long run; only the most recent events are kept, which is all latestResult ever needs.
const MAX_ACCUMULATED_EVENTS = 2000

// latestResult scans a task's event page, newest first, for the latest entry whose payload already matches
// assistants.Result's shape (verdict + summary, see pkg/assistants/types.go). Since commit 419753b the
// backend DOES emit such a payload over GET /api/assistants/{id}/events: runHelperTask bundles a "task_result"
// effect into the SAME ApplyRunOperation call as the task_status_changed-to-"completed" transition (see
// pkg/orchestrator/assistants.go's taskResultEffect / assistants_lifecycle.go), in addition to (not instead
// of) the result.json persisted to disk. This is the live consumer of that event, end-to-end verified by
// cmd/afm/run_collaboration_integration_test.go — not forward-compatible dead code.
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
    // cursor/accumulated (T11.2, TestHistory_LargeHistoryNotFullyLoadedOnEveryPoll) — each poll tick fetches
    // only what's NEW since the last page it already consumed, instead of re-reading the whole history from
    // cursor 0 every 3s. A long-lived watcher task (checkpoints/on_completion) can accumulate a large event
    // history over a long run; re-reading all of it on every tick would make the poll cost grow with the
    // task's age, not stay flat.
    let cursor = 0
    let accumulated: AssistantEventWire[] = []

    const drainOnce = async (): Promise<void> => {
      for (;;) {
        const page = await getAssistantTaskEvents(taskId, cursor)
        if (cancelled || seq.current !== mySeq) return
        if (page.events.length > 0) {
          accumulated = accumulated.concat(page.events).slice(-MAX_ACCUMULATED_EVENTS)
          setResult(latestResult(accumulated))
        }
        cursor = page.nextCursor
        if (!page.hasMore) return
      }
    }

    const load = (): void => {
      drainOnce().catch(() => {
        /* transient network error — keep whatever result we already had; next tick retries from the SAME
           cursor, since cursor/accumulated are only advanced after a successful page. */
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
  // allTasks (T4a.8) — the SAME stage's full flat task list task was looked up from, used ONLY to walk its
  // ancestor chain for the breadcrumb (buildBreadcrumbChain) — defaults to just [task] (a chain of 1, so the
  // breadcrumb renders nothing) when the caller has no reason to pass more (e.g. every pre-4a.8 call site/test).
  allTasks?: AssistantTask[]
  // maxDepth (T4a.8) — the run's effective delegation-depth ceiling, from GET /api/assistant-profiles
  // (resolved once per run). null = unknown/collaboration off — the breadcrumb still shows the chain and
  // current depth, just not "of N".
  maxDepth?: number | null
}

// AssistantTaskView — the workspace body for the 'assistant' WorkspaceView (T3.10): one helper task's
// current state. Three states, exactly as dev-plan.md's T3.10 tests enumerate: empty (no task selected, or
// still running with nothing to show yet), error (failed/cancelled), and completed-with-result (rendered
// through the SAME safe markdown renderer the plan panel uses — never a second dangerouslySetInnerHTML).
export function AssistantTaskView({ task, allTasks, maxDepth = null }: AssistantTaskViewProps): ReactElement {
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
  const chain = buildBreadcrumbChain(allTasks ?? [task], task.id)
  // isWatcher (T5.6) — a checkpoints/on_completion task is a standing OBSERVER of the stage's own main-agent
  // output (Этап 5), not a one-shot advisory check — trigger kind + how many turns it has fired + the last
  // checkpoint it processed are shown here, nowhere else (not a parallel display surface).
  const isWatcher = task.trigger === 'checkpoints' || task.trigger === 'on_completion'

  return (
    <div className="assistant-task-view">
      <AssistantBreadcrumb chain={chain} maxDepth={maxDepth} />
      <div className="assistant-task-view-header">
        <span className="assistant-task-view-title">{title}</span>
        {/* Required/advisory is text, not just color — same accessibility rule as the rail row. */}
        <span className="assistant-task-view-mode" data-mode={task.mode}>
          {task.mode === 'required' ? 'Required' : 'Advisory'}
        </span>
        <span className="assistant-task-view-status" data-status={task.status}>{label}</span>
      </div>
      <TaskActions task={task} passed={result?.verdict === 'pass'} />
      {isWatcher && (
        <div className="assistant-task-view-watch" data-trigger={task.trigger}>
          <span className="assistant-task-view-watch-trigger">
            {task.trigger === 'on_completion' ? 'Watches: on completion' : 'Watches: checkpoints'}
          </span>
          <span className="assistant-task-view-watch-passes">
            {task.watchPasses !== null ? `Passes: ${task.watchPasses}` : 'Passes: 0'}
          </span>
          {task.watchLastCheckpointSeq !== null && (
            <span className="assistant-task-view-watch-checkpoint">Last checkpoint: #{task.watchLastCheckpointSeq}</span>
          )}
        </div>
      )}

      {isError ? (
        <div className="assistant-task-view-error" role="alert">
          <span className="assistant-task-view-error-icon" aria-hidden="true">⚠</span>
          <div>
            <span>
              {task.statusReason !== ''
                ? task.statusReason
                : task.status === 'cancelled'
                  ? 'This helper task was cancelled.'
                  : 'This helper task failed.'}
            </span>
            {/* key={task.id}: AssistantTaskView itself is never remounted when the selected task changes
                (the caller passes task={selectedTask} without a key) — without this key, this child's own
                raw/loaded state would survive the prop change and keep showing the PREVIOUS task's raw
                answer after switching to a different failed task. */}
            {task.statusReason.includes('protocol_error') && <RawAnswerExpander key={task.id} taskId={task.id} />}
          </div>
        </div>
      ) : result !== null ? (
        <div className="assistant-task-result">
          {/* Verdict is text + icon, never color alone — same rule as required/advisory above. */}
          <div className="assistant-task-verdict" data-verdict={result.verdict}>
            <span aria-hidden="true">{verdictGlyph(result.verdict)}</span>
            <span>{verdictLabel(result.verdict)}</span>
            {result.repaired && (
              <span className="assistant-task-repaired" title="afm repaired this answer's syntax (markdown fence/surrounding prose); the verdict is unchanged">
                repaired
              </span>
            )}
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

// RawAnswerExpander lets a human see the raw text behind a protocol_error failure — lazily: the fetch only
// happens once the <details> is actually opened, never on mount (a task view that never expands this must
// never pay for it). A rejected fetch (e.g. the task crashed before producing any text, GET 404s) leaves the
// plain error alone — no crash, no empty block.
function RawAnswerExpander({ taskId }: { taskId: string }): ReactElement {
  const [raw, setRaw] = useState<string | null>(null)
  const [loaded, setLoaded] = useState(false)

  function onToggle(e: SyntheticEvent<HTMLDetailsElement>): void {
    if (!e.currentTarget.open || loaded) return
    setLoaded(true)
    getAssistantTaskRaw(taskId)
      .then(setRaw)
      .catch(() => {
        /* no raw answer available — the plain error above is enough */
      })
  }

  return (
    <details className="assistant-task-raw-answer" onToggle={onToggle}>
      <summary>Show the helper&apos;s raw answer</summary>
      {raw !== null && <pre className="assistant-task-raw-answer-text">{raw}</pre>}
    </details>
  )
}

// TaskActions renders the human controls for one helper task (Этап 7: pause/continue/cancel + waive). It
// calls the task-level mutation endpoints directly and lets the stage-assistants poll refresh the view — the
// same "fire the mutation, let the existing poll reconcile" pattern the rest of the dashboard uses. Waive
// asks for a required reason inline (the server rejects an empty one). The buttons are gated by the task's
// current status so a stale action is never offered; the server's CAS is still the final authority.
function TaskActions({ task, passed }: { task: AssistantTask; passed: boolean }): ReactElement | null {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [waiveOpen, setWaiveOpen] = useState(false)
  const [reason, setReason] = useState('')

  const terminal = ASSISTANT_TASK_TERMINAL.has(task.status)
  const canPause = !terminal && task.status !== 'paused'
  const canContinue = task.status === 'paused'
  const canCancel = !terminal
  // A required task can be waived until it either passes cleanly or is already waived — waiving is exactly how
  // a human unblocks a stage held by a required task that returned needs_changes (which is a terminal
  // 'completed' status but an open gate), so this is intentionally NOT gated on `!terminal`.
  const canWaive = task.mode === 'required' && !task.waived && !passed

  if (!canPause && !canContinue && !canCancel && !canWaive) return null

  function run(fn: () => Promise<void>): void {
    setBusy(true)
    setError(null)
    fn()
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'request failed'))
      .finally(() => setBusy(false))
  }

  function confirmWaive(): void {
    if (reason.trim() === '') return
    run(() => waiveAssistantTask(task.id, reason.trim()).then(() => setWaiveOpen(false)))
  }

  return (
    <div className="assistant-task-actions">
      <div className="assistant-task-actions-row">
        {canPause && (
          <button type="button" className="assistant-task-action" disabled={busy} onClick={() => run(() => pauseAssistantTask(task.id))}>
            Pause
          </button>
        )}
        {canContinue && (
          <button type="button" className="assistant-task-action" disabled={busy} onClick={() => run(() => continueAssistantTask(task.id))}>
            Continue
          </button>
        )}
        {canCancel && (
          <button type="button" className="assistant-task-action" disabled={busy} onClick={() => run(() => cancelAssistantTask(task.id))}>
            Cancel
          </button>
        )}
        {canWaive && !waiveOpen && (
          <button type="button" className="assistant-task-action assistant-task-action-waive" disabled={busy} onClick={() => setWaiveOpen(true)}>
            Waive…
          </button>
        )}
      </div>
      {waiveOpen && (
        <div className="assistant-task-waive">
          <textarea
            className="assistant-task-waive-reason"
            placeholder="Why are you accepting this required task not passing?"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
          />
          <div className="assistant-task-actions-row">
            <button type="button" className="assistant-task-action" disabled={busy} onClick={() => { setWaiveOpen(false); setReason('') }}>
              Cancel
            </button>
            <button type="button" className="assistant-task-action assistant-task-action-waive" disabled={busy || reason.trim() === ''} onClick={confirmWaive}>
              Confirm waive
            </button>
          </div>
        </div>
      )}
      {error !== null && (
        <div className="assistant-task-actions-error" role="alert">{error}</div>
      )}
    </div>
  )
}
