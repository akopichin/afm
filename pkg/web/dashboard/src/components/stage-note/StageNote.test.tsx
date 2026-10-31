import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { StageNote, type StageNoteStage } from './StageNote'
import * as runClient from '../../api/run-client'

const pending: StageNoteStage = { id: 's1', status: 'pending', isScript: false, preNote: '' }

type Props = Partial<Parameters<typeof StageNote>[0]>

function renderNote(props: Props = {}) {
  const onDraftChange = vi.fn()
  const onChanged = vi.fn()
  const view = render(<StageNote stage={pending} onDraftChange={onDraftChange} onChanged={onChanged} {...props} />)
  return { ...view, onDraftChange, onChanged }
}

function failWith(status: number): Promise<never> {
  return Promise.reject(new runClient.FlowApiError('/api/stages/s1/note', status, undefined))
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('StageNote visibility', () => {
  test('a pending stage without a note offers Add note', () => {
    renderNote()
    expect(screen.getByRole('region', { name: 'Stage note' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add note' })).toBeInTheDocument()
    expect(screen.queryByText('Read only')).toBeNull()
  })

  test('a pending stage with a note shows its text and Edit note', () => {
    renderNote({ stage: { ...pending, preNote: 'line one\nline two' } })
    expect(screen.getByText(/line one/)).toHaveTextContent('line one line two')
    expect(screen.getByRole('button', { name: 'Edit note' })).toBeInTheDocument()
  })

  test('the note is escaped, not rendered as markup', () => {
    const { container } = renderNote({ stage: { ...pending, preNote: '<img src=x onerror=alert(1)><b>bold</b>' } })
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('b')).toBeNull()
    expect(container.textContent).toContain('<b>bold</b>')
  })

  test('a started stage shows its note read only, without Edit', () => {
    renderNote({ stage: { ...pending, status: 'running', preNote: 'mind the limits' } })
    expect(screen.getByText('mind the limits')).toBeInTheDocument()
    expect(screen.getByText('Read only')).toBeInTheDocument()
    expect(screen.getByText('Before start')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /note/i })).toBeNull()
  })

  test.each([
    ['a started stage without a note', { ...pending, status: 'running' as const }],
    ['a finished stage without a note', { ...pending, status: 'done' as const }],
    ['a script stage without a note', { ...pending, isScript: true }],
  ])('%s has no block', (_name, stage) => {
    const { container } = renderNote({ stage })
    expect(container).toBeEmptyDOMElement()
  })
})

describe('StageNote editing', () => {
  test('Add note opens a focused empty editor and Enter inserts a line break without saving', () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const { onDraftChange } = renderNote()
    fireEvent.click(screen.getByRole('button', { name: 'Add note' }))
    const editor = screen.getByRole('textbox', { name: 'Stage note' })
    expect(editor).toHaveFocus()
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', '')
    fireEvent.change(editor, { target: { value: 'first' } })
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', 'first')
    fireEvent.keyDown(editor, { key: 'Enter' })
    expect(save).not.toHaveBeenCalled()
    expect(editor).toHaveValue('first')
  })

  test('Edit note starts from the saved text', () => {
    renderNote({ stage: { ...pending, preNote: 'saved text' } })
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('saved text')
  })

  test('mounting with a stored draft opens the editor with it, without taking focus', () => {
    renderNote({ initialDraft: 'unsaved words', stage: { ...pending, preNote: 'saved' } })
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('unsaved words')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).not.toHaveFocus()
  })

  test('a re-render with a new saved note does not overwrite the open draft', () => {
    const { rerender, onDraftChange, onChanged } = renderNote({ initialDraft: 'my draft' })
    rerender(<StageNote stage={{ ...pending, preNote: 'someone else' }} initialDraft="my draft" onDraftChange={onDraftChange} onChanged={onChanged} />)
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('my draft')
  })

  test('Save is unavailable for an empty or blank note', () => {
    renderNote({ initialDraft: '   ' })
    expect(screen.getByRole('button', { name: 'Save note' })).toBeDisabled()
  })

  test('Save posts the trimmed text, clears the draft, refreshes and focuses Edit note', async () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const { onDraftChange, onChanged } = renderNote({ initialDraft: '  keep it small \n' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect(save).toHaveBeenCalledWith('s1', 'keep it small')
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', null)
    expect(screen.queryByRole('textbox', { name: 'Stage note' })).toBeNull()
    expect(screen.getByRole('button', { name: /(Add|Edit) note/ })).toHaveFocus()
  })

  test.each([['Meta', { metaKey: true }], ['Ctrl', { ctrlKey: true }]])('%s+Enter saves', async (_name, modifier) => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    renderNote({ initialDraft: 'ship it' })
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Stage note' }), { key: 'Enter', ...modifier })
    await waitFor(() => expect(save).toHaveBeenCalledWith('s1', 'ship it'))
  })

  test('Cmd+Enter on a blank note does nothing', () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    renderNote({ initialDraft: ' ' })
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Stage note' }), { key: 'Enter', metaKey: true })
    expect(save).not.toHaveBeenCalled()
  })

  test('Escape cancels without a request, stops propagation and focuses the trigger', () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const outer = vi.fn()
    const { onDraftChange } = renderNote({ initialDraft: 'discard me', stage: { ...pending, preNote: 'saved' } })
    document.addEventListener('keydown', outer)
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Stage note' }), { key: 'Escape' })
    document.removeEventListener('keydown', outer)
    expect(outer).not.toHaveBeenCalled()
    expect(save).not.toHaveBeenCalled()
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', null)
    expect(screen.getByText('saved')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Edit note' })).toHaveFocus()
  })

  test('the Cancel button does the same as Escape', () => {
    const { onDraftChange } = renderNote({ initialDraft: 'discard me' })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', null)
    expect(screen.getByRole('button', { name: 'Add note' })).toHaveFocus()
  })

  test('Remove note posts an empty text and offers only a saved note', async () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const first = renderNote({ initialDraft: 'new' })
    expect(screen.queryByRole('button', { name: 'Remove note' })).toBeNull()
    first.unmount()
    const { onChanged } = renderNote({ initialDraft: 'edited', stage: { ...pending, preNote: 'saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove note' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(save).toHaveBeenCalledWith('s1', '')
    expect(screen.getByRole('button', { name: /(Add|Edit) note/ })).toHaveFocus()
  })

  test('the editor and every button are locked while the request is in flight', async () => {
    let finish: () => void = () => {}
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((resolve) => { finish = resolve }))
    renderNote({ initialDraft: 'slow', stage: { ...pending, preNote: 'saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toBeDisabled()
    for (const name of ['Save note', 'Cancel', 'Remove note']) expect(screen.getByRole('button', { name })).toBeDisabled()
    await act(async () => { finish() })
  })
})

describe('StageNote editor footer', () => {
  function openEditor(stage: StageNoteStage = pending) {
    const view = renderNote({ stage })
    fireEvent.click(screen.getByRole('button', { name: stage.preNote === '' ? 'Add note' : 'Edit note' }))
    return view
  }

  test('Attach, the hint and the buttons share one footer row', () => {
    const { container } = openEditor()
    const toolbar = container.querySelector('.pasteable-toolbar') as HTMLElement
    expect(toolbar).not.toBeNull()
    expect(toolbar).toContainElement(screen.getByRole('button', { name: 'Attach' }))
    expect(toolbar).toContainElement(screen.getByText(/Included in the agent’s context/))
    expect(toolbar).toContainElement(screen.getByRole('button', { name: 'Cancel' }))
    expect(toolbar).toContainElement(screen.getByRole('button', { name: 'Save note' }))
  })

  test('there is no separate attach strip while nothing is attached', () => {
    const { container } = openEditor()
    expect(container.querySelector('.pasteable-attachments')).toBeNull()
  })

  test('the editor is described by the hint', () => {
    openEditor()
    const hint = screen.getByText(/Included in the agent’s context/)
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveAccessibleDescription(hint.textContent ?? '')
  })

  test('Remove note sits in the same footer for a saved note', () => {
    const { container } = openEditor({ ...pending, preNote: 'saved' })
    expect(container.querySelector('.pasteable-toolbar')).toContainElement(screen.getByRole('button', { name: 'Remove note' }))
  })
})

describe('StageNote failures keep the draft', () => {
  test('a 409 says the stage started at the same moment and never reports delivery', async () => {
    vi.spyOn(runClient, 'setStageNote').mockImplementation(() => failWith(409))
    const { onDraftChange, onChanged } = renderNote({ initialDraft: 'late note' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Saved, but the stage started at the same moment — the agent may not have received this note.')
    expect(onDraftChange).not.toHaveBeenCalledWith('s1', null)
    expect(onChanged).toHaveBeenCalled()
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('late note')
  })

  test('a 400 for a stage that is no longer pending says it has already started', async () => {
    let reject: (reason: unknown) => void = () => {}
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((_resolve, rej) => { reject = rej }))
    const view = renderNote({ initialDraft: 'too late' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    // The status poll reports the start while the request is still in flight.
    view.rerender(<StageNote stage={{ ...pending, status: 'running' }} initialDraft="too late" onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    await act(async () => { reject(new runClient.FlowApiError('/api/stages/s1/note', 400, undefined)) })
    expect(screen.getByRole('alert')).toHaveTextContent('Not saved — the stage has already started.')
  })

  test.each([[400], [404], [500]])('a %i on a pending stage gives the generic text and keeps the draft', async (status) => {
    vi.spyOn(runClient, 'setStageNote').mockImplementation(() => failWith(status))
    const { onDraftChange } = renderNote({ initialDraft: 'keep me' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the note. Try again.')
    expect(onDraftChange).not.toHaveBeenCalledWith('s1', null)
    const editor = screen.getByRole('textbox', { name: 'Stage note' })
    expect(editor).toHaveValue('keep me')
    expect(editor).toBeEnabled()
    expect(editor).toHaveFocus()
  })

  test('a network failure gives the generic text', async () => {
    vi.spyOn(runClient, 'setStageNote').mockRejectedValue(new TypeError('Failed to fetch'))
    renderNote({ initialDraft: 'offline' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the note. Try again.')
  })

  test('a failed Remove keeps the editor and the draft', async () => {
    vi.spyOn(runClient, 'setStageNote').mockImplementation(() => failWith(500))
    renderNote({ initialDraft: 'edited', stage: { ...pending, preNote: 'saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove note' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the note. Try again.')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('edited')
  })

  test('a retry clears the previous error', async () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockImplementationOnce(() => failWith(500)).mockResolvedValue()
    renderNote({ initialDraft: 'again' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await screen.findByRole('alert')
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
    expect(save).toHaveBeenCalledTimes(2)
  })
})

describe('StageNote when the stage starts while editing', () => {
  const started: StageNoteStage = { ...pending, status: 'running' }

  test('the editor turns read only with the reason and the draft is not removed', () => {
    const { onDraftChange } = renderNote({ stage: started, initialDraft: 'unsent thoughts' })
    const editor = screen.getByRole('textbox', { name: 'Stage note' })
    expect(editor).toHaveValue('unsent thoughts')
    expect(editor).toHaveAttribute('readonly')
    expect(screen.getByText(/stage has started/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save note' })).toBeNull()
    expect(onDraftChange).not.toHaveBeenCalledWith('s1', null)
  })

  test('Discard draft removes it', () => {
    const { onDraftChange, container } = renderNote({ stage: started, initialDraft: 'unsent thoughts' })
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', null)
    expect(container).toBeEmptyDOMElement()
  })

  test('Discard draft keeps showing the saved note read only', () => {
    renderNote({ stage: { ...started, preNote: 'saved before start' }, initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    expect(screen.getByText('saved before start')).toBeInTheDocument()
  })

  test('a stage that becomes pending again (Retry) makes the draft editable', () => {
    const view = renderNote({ stage: started, initialDraft: 'unsent' })
    view.rerender(<StageNote stage={pending} initialDraft="unsent" onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    expect(screen.getByRole('textbox', { name: 'Stage note' })).not.toHaveAttribute('readonly')
    expect(screen.getByRole('button', { name: 'Save note' })).toBeEnabled()
  })

  test('a focus request that found no editor does not pull focus when the stage is pending again', async () => {
    let reject: (reason: unknown) => void = () => {}
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((_resolve, rej) => { reject = rej }))
    const view = renderNote({ initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    view.rerender(<StageNote stage={started} initialDraft="unsent" onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    await act(async () => { reject(new runClient.FlowApiError('/api/stages/s1/note', 409, undefined)) })
    view.rerender(<StageNote stage={pending} initialDraft="unsent" onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    expect(screen.getByRole('textbox', { name: 'Stage note' })).not.toHaveFocus()
  })
})

describe('StageNote save outcome', () => {
  function pendingSave(): { resolve: () => void; reject: (reason: unknown) => void } {
    const handle = { resolve: () => {}, reject: (_reason: unknown) => {} }
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((resolve, reject) => {
      handle.resolve = resolve
      handle.reject = reject
    }))
    return handle
  }

  test('under StrictMode a saved note closes the editor', async () => {
    const save = pendingSave()
    render(<StrictMode><StageNote stage={pending} initialDraft="ship it" onDraftChange={vi.fn()} onChanged={vi.fn()} /></StrictMode>)
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await act(async () => { save.resolve() })
    expect(screen.queryByRole('textbox', { name: 'Stage note' })).toBeNull()
    expect(screen.getByRole('button', { name: /(Add|Edit) note/ })).toBeInTheDocument()
  })

  test('under StrictMode a failed save shows the error and unlocks the editor', async () => {
    const save = pendingSave()
    render(<StrictMode><StageNote stage={pending} initialDraft="ship it" onDraftChange={vi.fn()} onChanged={vi.fn()} /></StrictMode>)
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await act(async () => { save.reject(new Error('offline')) })
    expect(screen.getByRole('alert')).toHaveTextContent('Could not save the note')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toBeEnabled()
  })

  test('the answer of a component that is gone leaves the newer draft alone', async () => {
    const save = pendingSave()
    const onDraftChange = vi.fn()
    const first = render(<StageNote stage={pending} initialDraft="old text" onDraftChange={onDraftChange} onChanged={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    first.unmount()
    render(<StageNote stage={pending} initialDraft="old text" onDraftChange={onDraftChange} onChanged={vi.fn()} />)
    fireEvent.change(screen.getByRole('textbox', { name: 'Stage note' }), { target: { value: 'new text' } })
    onDraftChange.mockClear()
    await act(async () => { save.resolve() })
    expect(onDraftChange).not.toHaveBeenCalled()
  })

  test('Discard draft is locked while a save is in flight', async () => {
    const save = pendingSave()
    const view = renderNote({ initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    view.rerender(<StageNote stage={{ ...pending, status: 'running' }} initialDraft="unsent" onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    expect(screen.getByRole('button', { name: 'Discard draft' })).toBeDisabled()
    await act(async () => { save.reject(new runClient.FlowApiError('/api/stages/s1/note', 409, undefined)) })
    expect(screen.getByRole('button', { name: 'Discard draft' })).toBeEnabled()
  })
})
