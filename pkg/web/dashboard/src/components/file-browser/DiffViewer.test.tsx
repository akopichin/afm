import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { FilesApiError, type FileDiff } from '../../api/files-client'
import { DiffViewer } from './DiffViewer'

function makeDiff(overrides: Partial<FileDiff> = {}): FileDiff {
  return {
    path: 'a.go',
    baseline: 'HEAD',
    status: 'modified',
    binary: false,
    truncated: false,
    diff: '--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n context\n',
    ...overrides,
  }
}

describe('DiffViewer', () => {
  test('shows a loading hint', () => {
    render(<DiffViewer diff={null} loading error={null} />)
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  test('renders lines with prefix-based classes, as plain text nodes (no HTML injection)', () => {
    const { container } = render(<DiffViewer diff={makeDiff()} loading={false} error={null} />)
    expect(container.querySelectorAll('.diff-line-add')).toHaveLength(1)
    expect(container.querySelectorAll('.diff-line-del')).toHaveLength(1)
    expect(container.querySelectorAll('.diff-line-hunk')).toHaveLength(1)
    expect(container.querySelectorAll('.diff-line-header')).toHaveLength(2)
    expect(screen.getByText('+new')).toBeInTheDocument()
    expect(screen.getByText('-old')).toBeInTheDocument()
    // no dangerouslySetInnerHTML anywhere in this tree
    expect(container.innerHTML).not.toContain('<script')
  })

  test('a diff line containing HTML-looking text is never parsed as markup', () => {
    const { container } = render(
      <DiffViewer diff={makeDiff({ diff: '+<img src=x onerror="window.__pwned=1">\n' })} loading={false} error={null} />,
    )
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText('+<img src=x onerror="window.__pwned=1">')).toBeInTheDocument()
  })

  test('shows a banner and skips lines for a binary file', () => {
    render(<DiffViewer diff={makeDiff({ binary: true, diff: '' })} loading={false} error={null} />)
    expect(screen.getByText(/binary/i)).toBeInTheDocument()
  })

  test('shows a truncated banner above the (partial) diff lines', () => {
    render(<DiffViewer diff={makeDiff({ truncated: true })} loading={false} error={null} />)
    expect(screen.getByText(/truncated/i)).toBeInTheDocument()
    expect(screen.getByText('+new')).toBeInTheDocument()
  })

  test('shows "no changes" for a clean status with an empty body', () => {
    render(<DiffViewer diff={makeDiff({ status: 'clean', diff: '' })} loading={false} error={null} />)
    expect(screen.getByText(/no changes/i)).toBeInTheDocument()
  })

  test('shows a dedicated message when the diff is unavailable (409)', () => {
    render(<DiffViewer diff={null} loading={false} error={new FilesApiError('diff_unavailable', 409)} />)
    expect(screen.getByText(/diff.*not available/i)).toBeInTheDocument()
  })

  test('shows a generic message for any other error', () => {
    render(<DiffViewer diff={null} loading={false} error={new FilesApiError('read_failed', 500)} />)
    expect(screen.getByText(/failed to load diff/i)).toBeInTheDocument()
  })
})
