import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AssistantTaskRow } from './AssistantTaskRow'
import { buildTree } from '../../hooks/use-stage-assistants'
import type { AssistantTask } from '../../types/assistant-task'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: '', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

// chainOfDepth builds a 1 -> 2 -> ... -> n linear chain (each task's only child is the next one) — the
// shape dev-plan.md's TestTree_RendersBeyondThreeLevels/TestTree_IndentCappedButDataNot need: a tree deeper
// than AssistantTaskRow's own MAX_VISUAL_DEPTH constant.
function chainOfDepth(n: number): AssistantTask[] {
  const tasks: AssistantTask[] = []
  for (let i = 1; i <= n; i++) {
    tasks.push(
      makeTask({
        id: `t${i}`,
        parentTaskId: i === 1 ? null : `t${i - 1}`,
        title: `Level ${i}`,
        depth: i,
      }),
    )
  }
  return tasks
}

// allExpanded — AssistantTaskRow only renders a node's children when expanded.has(node.id); these tests
// render every level, so every ancestor's id must be in the set.
function allExpanded(tasks: AssistantTask[]): Set<string> {
  return new Set(tasks.map((t) => t.id))
}

describe('AssistantTaskRow', () => {
  it('renders beyond three levels — no hardcoded 1/2/3-level limit', () => {
    const tasks = chainOfDepth(6)
    const [root] = buildTree(tasks)
    render(
      <ul>
        <AssistantTaskRow
          node={root!}
          depth={1}
          selectedTaskId={null}
          expanded={allExpanded(tasks)}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
        />
      </ul>,
    )
    for (let i = 1; i <= 6; i++) {
      expect(screen.getByText(`Level ${i}`)).toBeInTheDocument()
    }
  })

  it('caps the visual indent but keeps walking every level of data', () => {
    const tasks = chainOfDepth(6)
    const [root] = buildTree(tasks)
    render(
      <ul>
        <AssistantTaskRow
          node={root!}
          depth={1}
          selectedTaskId={null}
          expanded={allExpanded(tasks)}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
        />
      </ul>,
    )
    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(6)
    const indents = items.map((li) => li.style.getPropertyValue('--assistant-depth'))
    // Root renders at depth=1 (StagesList's own convention); visual indent is capped at MAX_VISUAL_DEPTH=4 ...
    expect(indents).toEqual(['1', '2', '3', '4', '4', '4'])
    // ... but every level 1..6 is still genuinely present in the DOM (the data walk is NOT truncated).
    expect(items.map((li) => li.dataset.taskId)).toEqual(['t1', 't2', 't3', 't4', 't5', 't6'])
  })

  it('a collapsed branch hides its children but the selected task id and toggle state are untouched', () => {
    const tasks = chainOfDepth(3)
    const [root] = buildTree(tasks)
    // t1 is expanded (shows t2), but t2 itself is NOT in `expanded` — its own child (t3) stays hidden.
    const expanded = new Set(['t1'])
    render(
      <ul>
        <AssistantTaskRow
          node={root!}
          depth={1}
          selectedTaskId="t3"
          expanded={expanded}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
        />
      </ul>,
    )
    expect(screen.getByText('Level 1')).toBeInTheDocument()
    expect(screen.getByText('Level 2')).toBeInTheDocument()
    expect(screen.queryByText('Level 3')).not.toBeInTheDocument()
    // The selection (t3) is preserved in state even though its row is currently collapsed away — no crash,
    // and re-expanding would show it marked active again (selectedTaskId is independent of `expanded`).
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('long task names never force horizontal overflow — the title gets the ellipsis-truncation class', () => {
    const tasks = [makeTask({ title: 'A'.repeat(300) })]
    const [root] = buildTree(tasks)
    render(
      <ul>
        <AssistantTaskRow
          node={root!}
          depth={1}
          selectedTaskId={null}
          expanded={new Set()}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
        />
      </ul>,
    )
    const title = screen.getByText('A'.repeat(300))
    expect(title).toHaveClass('assistant-task-title')
    // .assistant-task-row/.assistant-task-label both carry min-width:0 in assistants.css, which is what lets
    // .assistant-task-title's overflow:hidden/text-overflow:ellipsis actually truncate inside a flex row —
    // asserting the class names here is the render-level half of that CSS contract.
    expect(title.closest('.assistant-task-label')).not.toBeNull()
  })

  // T7.9/§4.3: a required task explains WHAT it blocks — the root stage itself, or just its own immediate
  // parent helper — so the rail never shows an unqualified "Required" pill that leaves the user guessing.
  it('TestBadge_RequiredExplainsWhoIsBlocked', () => {
    const blocksStage = makeTask({ id: 'qa', title: 'QA review', mode: 'required', blocksRootCompletion: true })
    const parent = makeTask({ id: 'mobile', title: 'Mobile lead', mode: 'required', blocksRootCompletion: true })
    const blocksParent = makeTask({
      id: 'android',
      parentTaskId: 'mobile',
      title: 'Android check',
      mode: 'required',
      blocksRootCompletion: false,
      depth: 2,
    })
    const tasks = [blocksStage, parent, blocksParent]

    const [stageRoot] = buildTree([blocksStage])
    const { unmount } = render(
      <ul>
        <AssistantTaskRow
          node={stageRoot!}
          depth={1}
          selectedTaskId={null}
          expanded={new Set()}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
          allTasks={tasks}
        />
      </ul>,
    )
    expect(screen.getByText('Blocks stage completion')).toBeInTheDocument()
    unmount()

    const [parentRoot] = buildTree([parent, blocksParent])
    render(
      <ul>
        <AssistantTaskRow
          node={parentRoot!}
          depth={1}
          selectedTaskId={null}
          expanded={new Set(['mobile'])}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
          allTasks={tasks}
        />
      </ul>,
    )
    expect(screen.getByText('Blocks Mobile lead')).toBeInTheDocument()
  })

  // An advisory task never blocks anything — no explanation text is rendered next to its mode pill.
  it('renders no blocking explanation for an advisory task', () => {
    const task = makeTask({ mode: 'advisory', title: 'Advisory check' })
    const [root] = buildTree([task])
    render(
      <ul>
        <AssistantTaskRow
          node={root!}
          depth={1}
          selectedTaskId={null}
          expanded={new Set()}
          onToggleExpand={vi.fn()}
          onSelect={vi.fn()}
          allTasks={[task]}
        />
      </ul>,
    )
    expect(screen.queryByText(/Blocks/)).not.toBeInTheDocument()
  })
})
