import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { handleTextareaSubmit } from './textarea-submit'

describe('handleTextareaSubmit', () => {
  it.each([{}, { ctrlKey: true }, { metaKey: true }])('submits Enter and preserves existing shortcuts: %j', (keys) => {
    const submit = vi.fn()
    render(<textarea onKeyDown={(event) => { handleTextareaSubmit(event, submit) }} />)
    expect(fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ...keys })).toBe(false)
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it.each([
    { shiftKey: true },
    { shiftKey: true, ctrlKey: true },
    { shiftKey: true, metaKey: true },
    { altKey: true },
    { isComposing: true },
    { keyCode: 229 },
    { key: 'Escape' },
  ])('leaves newline and composition keys untouched: %j', (keys) => {
    const submit = vi.fn()
    render(<textarea onKeyDown={(event) => { handleTextareaSubmit(event, submit) }} />)
    expect(fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ...keys })).toBe(true)
    expect(submit).not.toHaveBeenCalled()
  })

  it.each([{ canSubmit: false, repeat: false }, { canSubmit: true, repeat: true }])(
    'consumes Enter without submitting when unavailable or held down: %j', ({ canSubmit, repeat }) => {
      const submit = vi.fn()
      render(<textarea onKeyDown={(event) => { handleTextareaSubmit(event, submit, canSubmit) }} />)
      expect(fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', repeat })).toBe(false)
      expect(submit).not.toHaveBeenCalled()
    },
  )

  it('respects an earlier handler that consumed the key', () => {
    const submit = vi.fn()
    render(<textarea onKeyDown={(event) => { event.preventDefault(); handleTextareaSubmit(event, submit) }} />)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
  })
})
