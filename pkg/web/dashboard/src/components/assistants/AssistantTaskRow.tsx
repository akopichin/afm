import type { CSSProperties, ReactElement } from 'react'
import {
  assistantBlockingExplanation,
  assistantTaskStatusLabel,
  type AssistantTask,
  type AssistantTaskNode,
} from '../../types/assistant-task'

// Visual indent caps here; the tree itself is not limited — buildTree (use-stage-assistants) walks an
// arbitrary ParentTaskID chain, Этап 4a's recursion will produce real depth > 1 without this component
// needing a rewrite (dev-plan.md T3.9: "визуальный отступ ограничен, данные по глубине — нет").
const MAX_VISUAL_DEPTH = 4

type AssistantTaskRowProps = {
  node: AssistantTaskNode
  depth: number
  selectedTaskId: string | null
  // expanded — keyed by task_id (not a hardcoded depth level): the same Set a node with children of its own
  // would use for ITS OWN expand/collapse, one level down.
  expanded: ReadonlySet<string>
  onToggleExpand: (taskId: string) => void
  onSelect: (taskId: string) => void
  // allTasks (T7.9) — the SAME stage's full flat task list node was read from (useStageAssistants'
  // summary.tasks), used ONLY to resolve a required task's parent title for assistantBlockingExplanation
  // (never a second API call — see that function's own doc comment). Optional/defaults to [] so existing
  // call sites/tests that never pass it keep compiling (an advisory-only tree renders no blocking text either
  // way, same as AssistantTaskView's allTasks precedent).
  allTasks?: AssistantTask[]
}

// AssistantTaskRow — one helper-task row under its root stage in the rail, plus (recursively, depth-first)
// its own visible children. Deliberately NOT a stage row: no ordinal number, its own compact status dot, no
// row actions (the rail has no helper-task mutation UI; see run-client.ts for why attach/pause/continue/cancel
// aren't wired here).
//
// Renders as a flat Fragment of sibling <li>s — NOT nested inside its own <ul> — so StagesList can splice
// them directly into #stages-list's flat list, right after the parent stage's <li>. Nesting a <ul> inside
// the stage's own <li> would stretch that stage's box height, and .stage-connector's absolute positioning
// (fixed pixel offsets assuming one row per <li>) would no longer reach the next stage's dot.
export function AssistantTaskRow({
  node,
  depth,
  selectedTaskId,
  expanded,
  onToggleExpand,
  onSelect,
  allTasks,
}: AssistantTaskRowProps): ReactElement {
  const hasChildren = node.children.length > 0
  const isExpanded = expanded.has(node.id)
  const indent = Math.min(depth, MAX_VISUAL_DEPTH)
  const label = assistantTaskStatusLabel(node)
  // blockingText (T7.9/§4.3) — "Blocks stage completion" vs "Blocks <parent task>", shown only for a
  // required, non-waived task. null for advisory tasks (and for waived ones) renders nothing extra.
  const blockingText = assistantBlockingExplanation(node, allTasks ?? [])

  return (
    <>
      <li
        className={`assistant-task-item${node.id === selectedTaskId ? ' active' : ''}`}
        data-task-id={node.id}
        data-status={node.status}
        style={{ '--assistant-depth': indent } as CSSProperties}
      >
        {/* A real <button>, same accessibility rationale as .stage-row: Enter/Space/focus-visible out of
            the box. The expand/collapse toggle is a SEPARATE sibling button (never nested inside this one —
            same "no nested interactive elements" constraint StagesList documents for .stage-row). */}
        <button type="button" className="assistant-task-row" title={label} onClick={() => onSelect(node.id)}>
          <span
            className="assistant-task-status-dot"
            data-status={node.status}
            data-quota={node.status === 'queued' && node.quota !== undefined ? 'true' : undefined}
            aria-hidden="true"
          />
          <span className="assistant-task-label">
            <span className="assistant-task-title">{node.title !== '' ? node.title : node.profileId}</span>
            {/* Required/advisory is distinguished by TEXT, not just color (T3.10 accessibility requirement) —
                a colorblind user or a screen reader must still be able to tell them apart. */}
            <span className="assistant-task-mode" data-mode={node.mode}>
              {node.mode === 'required' ? 'Required' : 'Advisory'}
            </span>
            {blockingText !== null && (
              <span className="assistant-task-blocks" title={blockingText}>
                {blockingText}
              </span>
            )}
            <span className="assistant-task-status-sr">{label}</span>
          </span>
        </button>
        {hasChildren && (
          <button
            type="button"
            className="assistant-toggle"
            aria-label={isExpanded ? `Collapse helper tasks under ${node.title !== '' ? node.title : node.profileId}` : `Expand helper tasks under ${node.title !== '' ? node.title : node.profileId}`}
            aria-expanded={isExpanded}
            onClick={(e) => {
              e.stopPropagation() // не триггерить onSelect клика по строке
              onToggleExpand(node.id)
            }}
          >
            <span aria-hidden="true">{isExpanded ? '▾' : '▸'}</span>
          </button>
        )}
      </li>
      {hasChildren &&
        isExpanded &&
        node.children.map((child) => (
          <AssistantTaskRow
            key={child.id}
            node={child}
            depth={depth + 1}
            selectedTaskId={selectedTaskId}
            expanded={expanded}
            onToggleExpand={onToggleExpand}
            onSelect={onSelect}
            allTasks={allTasks}
          />
        ))}
    </>
  )
}
