import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AssistantBreadcrumb } from './AssistantBreadcrumb'
import { buildBreadcrumbChain } from '../../types/assistant-task'
import type { AssistantTask } from '../../types/assistant-task'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: '', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

const TASKS: AssistantTask[] = [
  makeTask({ id: 'frontend', parentTaskId: null, title: 'Frontend', depth: 1 }),
  makeTask({ id: 'qa', parentTaskId: 'frontend', title: 'QA', depth: 2 }),
  makeTask({ id: 'mobile', parentTaskId: 'qa', title: 'Mobile', depth: 3 }),
]

describe('buildBreadcrumbChain', () => {
  it('walks the parent chain root-first, including the task itself', () => {
    expect(buildBreadcrumbChain(TASKS, 'mobile').map((t) => t.id)).toEqual(['frontend', 'qa', 'mobile'])
  })

  it('a root task yields a chain of exactly itself', () => {
    expect(buildBreadcrumbChain(TASKS, 'frontend').map((t) => t.id)).toEqual(['frontend'])
  })

  it('an unknown task id yields an empty chain rather than throwing', () => {
    expect(buildBreadcrumbChain(TASKS, 'does-not-exist')).toEqual([])
  })
})

describe('AssistantBreadcrumb', () => {
  it('shows the chain and current depth / max_depth, with the immediate parent distinguished', () => {
    const chain = buildBreadcrumbChain(TASKS, 'mobile')
    render(<AssistantBreadcrumb chain={chain} maxDepth={4} />)

    expect(screen.getByText('Frontend')).toBeInTheDocument()
    expect(screen.getByText('QA')).toBeInTheDocument()
    expect(screen.getByText('Mobile')).toBeInTheDocument()
    expect(screen.getByText(/Depth 3 of 4/)).toBeInTheDocument()

    // QA is the immediate parent of Mobile — distinguished from Frontend (an earlier ancestor) and from
    // Mobile itself (the current task).
    expect(screen.getByText('QA').closest('li')).toHaveAttribute('data-immediate-parent', 'true')
    expect(screen.getByText('Frontend').closest('li')).not.toHaveAttribute('data-immediate-parent')
    expect(screen.getByText('Mobile').closest('li')).toHaveAttribute('data-current', 'true')
  })

  it('renders nothing for a root-only chain (length 1)', () => {
    const { container } = render(<AssistantBreadcrumb chain={[makeTask()]} maxDepth={4} />)
    expect(container.firstChild).toBeNull()
  })

  it('omits "of N" when max_depth is not yet known', () => {
    const chain = buildBreadcrumbChain(TASKS, 'mobile')
    render(<AssistantBreadcrumb chain={chain} maxDepth={null} />)
    expect(screen.getByText(/Depth 3/)).toBeInTheDocument()
    expect(screen.queryByText(/of \d/)).not.toBeInTheDocument()
  })
})
