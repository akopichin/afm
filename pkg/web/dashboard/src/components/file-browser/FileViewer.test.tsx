import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { FileContent } from '../../api/files-client'
import { FileViewer } from './FileViewer'

function makeContent(overrides: Partial<FileContent> = {}): FileContent {
  return {
    path: 'a.go',
    displayPath: 'a.go',
    reference: '[AFM file: "/w/afm/a.go"]',
    language: 'go',
    size: 11,
    modifiedAt: '2026-09-03T00:00:00Z',
    content: 'package a',
    etag: 'W/"1"',
    ...overrides,
  }
}

describe('FileViewer', () => {
  test('shows a loading hint while loading', () => {
    render(<FileViewer content={null} loading error={null} />)
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  test('shows an empty hint when nothing is selected', () => {
    render(<FileViewer content={null} loading={false} error={null} />)
    expect(screen.getByText(/select a file/i)).toBeInTheDocument()
  })

  test('shows the error message as plain text', () => {
    render(<FileViewer content={null} loading={false} error="binary_file" />)
    expect(screen.getByText(/binary_file/)).toBeInTheDocument()
  })

  test('renders highlighted, escaped source into a single code element (the only dangerouslySetInnerHTML in the tree)', () => {
    const { container } = render(<FileViewer content={makeContent({ language: 'go', content: 'package a' })} loading={false} error={null} />)
    const code = container.querySelector('code.hljs.language-go')
    expect(code).not.toBeNull()
    expect(code?.innerHTML).toContain('hljs-keyword')
    expect(code?.textContent).toBe('package a')
  })

  test('does not execute source as HTML — a script/img payload renders as inert text', () => {
    const pwned = '<img src=x onerror="window.__pwned=1">'
    const w = window as unknown as { __pwned?: number }
    delete w.__pwned

    const { container } = render(<FileViewer content={makeContent({ language: 'typescript', content: pwned })} loading={false} error={null} />)

    expect(container.querySelector('img')).toBeNull()
    expect(w.__pwned).toBeUndefined()
    expect(container.querySelector('code')?.textContent).toBe(pwned)
  })

  test('plain language renders escaped text without hljs token spans', () => {
    const { container } = render(<FileViewer content={makeContent({ language: 'plain', content: 'hello <world>' })} loading={false} error={null} />)
    const code = container.querySelector('code.language-plain')
    expect(code?.innerHTML).toBe('hello &lt;world&gt;')
  })
})
