import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { GraphOperationPreview } from './GraphOperationPreview'

describe('GraphOperationPreview', () => {
  test('insert_before renders the target stage dependency diff, before vs after', () => {
    render(
      <GraphOperationPreview
        kind="insert_before"
        targetId="deploy"
        preview={{ id: 'new-stage', dependsOn: ['build'], extraDeps: { deploy: ['new-stage'] } }}
      />,
    )

    const before = screen.getByText(/before — deploy depends on/i).closest('.graph-preview-diff-col')
    const after = screen.getByText(/after — deploy depends on/i).closest('.graph-preview-diff-col')
    expect(before).not.toBeNull()
    expect(after).not.toBeNull()
    expect(before).toHaveTextContent('build')
    expect(before).not.toHaveTextContent('new-stage')
    expect(after).toHaveTextContent('build')
    expect(after).toHaveTextContent('new-stage')
  })

  test('append renders the explicit wait-for list and flags warnings', () => {
    render(
      <GraphOperationPreview
        kind="append"
        preview={{ id: 'new-stage', dependsOn: ['review', 'docs'], warnings: ['dependency "review" is currently failed'] }}
      />,
    )

    expect(screen.getByText('review')).toBeInTheDocument()
    expect(screen.getByText('docs')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/review.*currently failed/i)
  })

  test('shows the executor by its readable name, with the raw command in the tooltip', () => {
    render(<GraphOperationPreview kind="append" preview={{ id: 'review', dependsOn: [], command: 'codex-as-claude', commandName: 'Codex Reviewer' }} />)
    expect(screen.getByText('Codex Reviewer')).toHaveAttribute('title', 'Codex Reviewer\nCommand: codex-as-claude')
    expect(screen.queryByText('codex-as-claude')).toBeNull()
  })

  test('without a preview command there is no executor line', () => {
    render(<GraphOperationPreview kind="append" preview={{ id: 'review', dependsOn: [] }} />)
    expect(screen.queryByText(/executor/i)).toBeNull()
  })

  test('append with no dependencies says it starts immediately', () => {
    render(<GraphOperationPreview kind="append" preview={{ id: 'new-stage', dependsOn: [] }} />)
    expect(screen.getByText(/starts immediately/i)).toBeInTheDocument()
  })
})
