import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import commentsCss from '../../../skins/base/comments.css?raw'
import coffeeCss from '../../../skins/coffee/index.css?raw'
import gogaCss from '../../../skins/goga/index.css?raw'
import graphiteCss from '../../../skins/graphite/index.css?raw'
import novacorpsCss from '../../../skins/novacorps/index.css?raw'
import { CommentCard, type CommentCardProps } from './CommentCard'

function renderCard(overrides: Partial<CommentCardProps> = {}) {
  const props: CommentCardProps = {
    title: 'Comment on line 3',
    quote: 'quoted source',
    text: 'first line\nsecond line',
    onEdit: vi.fn(),
    onRemove: vi.fn(),
    ...overrides,
  }
  return { props, ...render(<CommentCard {...props} />) }
}

describe('CommentCard', () => {
  test('renders the full multi-line text without truncation', () => {
    const long = `${'word '.repeat(200)}\nlast line`
    const { container } = renderCard({ text: long })
    expect(container.querySelector('.comment-text')?.textContent).toBe(long)
  })

  test('text and quote are plain text, never markup', () => {
    const { container } = renderCard({ text: '<i>x</i> # not a heading', quote: '<b>q</b>' })
    expect(screen.getByText('<i>x</i> # not a heading')).toBeInTheDocument()
    expect(screen.getByText('<b>q</b>')).toBeInTheDocument()
    expect(container.querySelector('i, b, h1')).toBeNull()
  })

  test('Draft badge only for an unsaved edit', () => {
    const { rerender, props } = renderCard({ draft: true })
    expect(screen.getByText('Draft')).toBeInTheDocument()
    rerender(<CommentCard {...props} draft={false} />)
    expect(screen.queryByText('Draft')).toBeNull()
  })

  test('Edit and Remove are real buttons with subject labels', () => {
    const { props } = renderCard()
    fireEvent.click(screen.getByRole('button', { name: 'Edit comment on line 3' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove comment on line 3' }))
    expect(props.onEdit).toHaveBeenCalledTimes(1)
    expect(props.onRemove).toHaveBeenCalledTimes(1)
  })

  test('the cross is optional: without onRemove only the explicit actions delete', () => {
    renderCard({ onRemove: undefined, actions: <button type="button">Delete</button> })
    expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
  })

  test('Edit is optional and extra actions are rendered', () => {
    renderCard({ onEdit: undefined, actions: <button type="button">Send</button> })
    expect(screen.queryByRole('button', { name: /^Edit/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Send' })).toBeInTheDocument()
  })

  test('no quote element for an empty quote', () => {
    const { container } = renderCard({ quote: '' })
    expect(container.querySelector('.line-comment-quote')).toBeNull()
  })
})

describe('comments.css', () => {
  test('wraps long words in text and quote', () => {
    expect(commentsCss).toMatch(/\.comment-text[^{]*\{[^}]*overflow-wrap:\s*anywhere/)
    expect(commentsCss).toMatch(/\.line-comment-quote-src[^{]*\{[^}]*overflow-wrap:\s*anywhere/)
  })

  test.each([
    ['graphite', graphiteCss],
    ['coffee', coffeeCss],
    ['goga', gogaCss],
    ['novacorps', novacorpsCss],
  ])('%s imports comments.css', (_name, css) => {
    expect(css).toContain('@import url("../base/comments.css");')
  })
})
