import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { countInFolder, FileCommentCount } from './FileCommentCount'

describe('FileCommentCount', () => {
  test('renders the dot and the pluralised accessible name', () => {
    const { rerender } = render(<FileCommentCount count={1} />)
    expect(screen.getByLabelText('1 comment')).toHaveTextContent('● 1')
    rerender(<FileCommentCount count={3} />)
    expect(screen.getByLabelText('3 comments')).toHaveTextContent('● 3')
  })

  test('renders nothing for zero comments', () => {
    const { container } = render(<FileCommentCount count={0} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('countInFolder', () => {
  const paths = ['foo/a.go', 'foo/sub/b.go', 'foobar/c.go', 'foo.go', 'other/foo/d.go']

  test('counts files at any depth below the folder', () => {
    expect(countInFolder(paths, 'foo')).toBe(2)
    expect(countInFolder(paths, 'foo/sub')).toBe(1)
  })

  test('matches on the path boundary: foo does not capture foobar or foo.go', () => {
    expect(countInFolder(paths, 'foobar')).toBe(1)
    expect(countInFolder(['foobar/c.go', 'foo.go'], 'foo')).toBe(0)
  })

  test('is zero for a folder without comments', () => {
    expect(countInFolder(paths, 'missing')).toBe(0)
    expect(countInFolder([], 'foo')).toBe(0)
  })
})
