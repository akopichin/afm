import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { CommentEditor, type CommentEditorProps } from './CommentEditor'

function renderEditor(overrides: Partial<CommentEditorProps> = {}) {
  const props: CommentEditorProps = {
    context: 'plan',
    stageId: 's1',
    title: 'Comment on line 3',
    quote: 'the quoted source line',
    value: 'draft text',
    onChange: vi.fn(),
    onSave: vi.fn(),
    onFold: vi.fn(),
    saveLabel: 'Save',
    ...overrides,
  }
  return { props, ...render(<CommentEditor {...props} />) }
}

describe('CommentEditor', () => {
  test('shows title, quote and the full controlled text', () => {
    renderEditor({ value: 'line one\nline two' })
    expect(screen.getByText('Comment on line 3')).toBeInTheDocument()
    expect(screen.getByText('the quoted source line')).toBeInTheDocument()
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('line one\nline two')
  })

  test('every keystroke goes to onChange with the full text', () => {
    const { props } = renderEditor()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'draft text!' } })
    expect(props.onChange).toHaveBeenCalledWith('draft text!')
  })

  test.each(['plan', 'question', 'file', 'review'] as const)('%s: Cmd/Ctrl+Enter and the button both save', (context) => {
    const { props } = renderEditor({ context })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', metaKey: true })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(props.onSave).toHaveBeenCalledTimes(3)
  })

  test('saveDisabled blocks the button and the shortcut', () => {
    const { props } = renderEditor({ saveDisabled: true })
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })
    expect(props.onSave).not.toHaveBeenCalled()
  })

  test('Escape folds without discarding and does not reach a parent handler', () => {
    const parentKey = vi.fn()
    const { props } = renderEditor({ onDiscard: vi.fn(), discardLabel: 'Discard' })
    document.addEventListener('keydown', parentKey)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    document.removeEventListener('keydown', parentKey)
    expect(props.onFold).toHaveBeenCalledTimes(1)
    expect(props.onDiscard).not.toHaveBeenCalled()
    expect(parentKey).not.toHaveBeenCalled()
  })

  test('the cross folds, the discard button is explicit and optional', () => {
    const { props, rerender } = renderEditor({ onDiscard: vi.fn(), discardLabel: 'Delete' })
    fireEvent.click(screen.getByRole('button', { name: 'Close comment on line 3' }))
    expect(props.onFold).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(props.onDiscard).toHaveBeenCalledTimes(1)
    rerender(<CommentEditor {...props} onDiscard={undefined} />)
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  })

  test('autofocuses the textarea', () => {
    renderEditor()
    expect(screen.getByRole('textbox')).toHaveFocus()
  })

  test('error is announced', () => {
    renderEditor({ error: 'stale content' })
    expect(screen.getByRole('alert')).toHaveTextContent('stale content')
  })

  test.each(['plan', 'question'] as const)('%s allows attachments', (context) => {
    renderEditor({ context })
    expect(screen.getByRole('button', { name: 'Attach' })).toBeInTheDocument()
  })

  test('allowAttachments=false hides Attach even in plan', () => {
    renderEditor({ context: 'plan', allowAttachments: false })
    expect(screen.queryByRole('button', { name: 'Attach' })).toBeNull()
  })

  test.each(['file', 'review'] as const)('%s never offers attachments and needs no stage', (context) => {
    renderEditor({ context, stageId: undefined })
    expect(screen.queryByRole('button', { name: 'Attach' })).toBeNull()
    expect(screen.getByRole('textbox')).toBeInTheDocument()
  })

  test('quote is plain text, never markup', () => {
    const { container } = renderEditor({ quote: '<b>bold</b> **x**' })
    expect(screen.getByText('<b>bold</b> **x**')).toBeInTheDocument()
    expect(container.querySelector('b')).toBeNull()
  })

  test('no quote element for an empty quote', () => {
    const { container } = renderEditor({ quote: '' })
    expect(container.querySelector('.line-comment-quote')).toBeNull()
  })
  test('a blank source line is still identified by its number: title, line attribute, no quote', () => {
    const { container } = renderEditor({ title: 'Comment on line 12', line: 12, quote: '' })
    expect(screen.getByText('Comment on line 12')).toBeInTheDocument()
    expect(container.querySelector('.comment-editor')?.getAttribute('data-comment-line')).toBe('12')
    expect(container.querySelector('.line-comment-quote')).toBeNull()
    expect(screen.getByRole('button', { name: 'Close comment on line 12' })).toBeInTheDocument()
  })

  test('one action row: hint, then Cancel, Delete and the primary save; Cancel folds and keeps the draft', () => {
    const { props, container } = renderEditor({ saveLabel: 'Save comment', onDiscard: vi.fn(), discardLabel: 'Delete', draft: true })
    const bar = container.querySelector('.pasteable-toolbar') as HTMLElement
    expect(bar.querySelector('.comment-actions-hint')?.textContent).toBe('Save here · send when ready')
    expect(Array.from(bar.querySelectorAll('.comment-actions button')).map((b) => b.textContent)).toEqual(['Cancel', 'Delete', 'Save comment'])
    expect(screen.getByText('Draft')).toBeInTheDocument()
    expect(container.querySelector('.comment-help')?.textContent).toContain('Esc to collapse and keep draft')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(props.onFold).toHaveBeenCalledTimes(1)
    expect(props.onDiscard).not.toHaveBeenCalled()
  })

  test('the plain-textarea editor (files) has the same action row; review notes have no send hint', () => {
    const file = renderEditor({ context: 'file', stageId: undefined })
    expect(file.container.querySelector('.pasteable-toolbar .comment-actions-hint')).not.toBeNull()
    file.unmount()
    const review = renderEditor({ context: 'review', stageId: undefined })
    expect(review.container.querySelector('.comment-actions-hint')).toBeNull()
  })
})
