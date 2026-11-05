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
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByText('mind the limits', { selector: 'p' })).toBeInTheDocument()
    expect(screen.getByText('Read only')).toBeInTheDocument()
    expect(screen.getByText('Before start')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /(Add|Edit) note/ })).toBeNull()
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

describe('StageNote shows the saved text before the status catches up', () => {
  test('after Save the card shows the new text while preNote is unchanged, and a changed preNote wins again', async () => {
    vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const stage = { ...pending, preNote: 'old text' }
    const { rerender, onDraftChange, onChanged } = renderNote({ stage })
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Stage note' }), { target: { value: 'new text' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Edit note' })).toHaveFocus())
    expect(screen.getByText('new text')).toBeInTheDocument()
    expect(screen.queryByText('old text')).not.toBeInTheDocument()

    rerender(<StageNote stage={{ ...stage, preNote: 'new text' }} onDraftChange={onDraftChange} onChanged={onChanged} />)
    expect(screen.getByText('new text')).toBeInTheDocument()
    rerender(<StageNote stage={{ ...stage, preNote: 'from elsewhere' }} onDraftChange={onDraftChange} onChanged={onChanged} />)
    expect(screen.getByText('from elsewhere')).toBeInTheDocument()
  })

  async function saveNote(replaced: string, saved: string) {
    vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const stage = { ...pending, preNote: replaced }
    const view = renderNote({ stage })
    fireEvent.click(screen.getByRole('button', { name: replaced === '' ? 'Add note' : 'Edit note' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Stage note' }), { target: { value: saved } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Edit note' })).toHaveFocus())
    const show = (preNote: string) => view.rerender(<StageNote stage={{ ...stage, preNote }} onDraftChange={view.onDraftChange} onChanged={view.onChanged} />)
    return show
  }

  async function saveThenCatchUp(replaced: string, saved: string) {
    const show = await saveNote(replaced, saved)
    show(saved)
    return show
  }

  test('once the status caught up, a server note back to the old value is shown', async () => {
    const show = await saveThenCatchUp('old text', 'new text')
    show('old text')
    expect(screen.getByText('old text')).toBeInTheDocument()
    expect(screen.queryByText('new text')).not.toBeInTheDocument()
  })

  test('once the status caught up, a server note removed elsewhere is not revived', async () => {
    const show = await saveThenCatchUp('', 'new text')
    show('')
    expect(screen.queryByText('new text')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add note' })).toBeInTheDocument()
  })

  test('once the status caught up, a server note changed to a third value wins, then back to empty', async () => {
    const show = await saveThenCatchUp('old text', 'new text')
    show('third')
    expect(screen.getByText('third')).toBeInTheDocument()
    show('')
    expect(screen.queryByText('new text')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add note' })).toBeInTheDocument()
  })

  test('a second edit after Save starts from the saved text and is shown', async () => {
    const show = await saveNote('old text', 'new text')
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('new text')
    fireEvent.change(screen.getByRole('textbox', { name: 'Stage note' }), { target: { value: 'newer text' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Stage note' })).not.toBeInTheDocument())
    expect(screen.getByText('newer text')).toBeInTheDocument()
    show('newer text')
    expect(screen.getByText('newer text')).toBeInTheDocument()
  })

  test('Remove note hides the note right away', async () => {
    vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    renderNote({ stage: { ...pending, preNote: 'old text' } })
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove note' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add note' })).toBeInTheDocument())
    expect(screen.queryByText('old text')).not.toBeInTheDocument()
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
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    const editor = screen.getByRole('textbox', { name: 'Stage note' })
    expect(editor).toHaveValue('unsent thoughts')
    expect(editor).toHaveAttribute('readonly')
    expect(screen.getByText(/stage has started/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save note' })).toBeNull()
    expect(onDraftChange).not.toHaveBeenCalledWith('s1', null)
  })

  test('Discard draft removes it', () => {
    const { onDraftChange, container } = renderNote({ stage: started, initialDraft: 'unsent thoughts' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    expect(onDraftChange).toHaveBeenLastCalledWith('s1', null)
    expect(container).toBeEmptyDOMElement()
  })

  test('Discard draft keeps showing the saved note read only', () => {
    renderNote({ stage: { ...started, preNote: 'saved before start' }, initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByText('saved before start', { selector: 'p' })).toBeInTheDocument()
  })

  test('a stage that becomes pending again (Retry) makes the draft editable', () => {
    const view = renderNote({ stage: started, initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByRole('button', { name: 'Discard draft' })).toBeDisabled()
    await act(async () => { save.reject(new runClient.FlowApiError('/api/stages/s1/note', 409, undefined)) })
    expect(screen.getByRole('button', { name: 'Discard draft' })).toBeEnabled()
  })
})

describe('StageNote presentation defaults', () => {
  const notStarted = { started: false }
  const startedTiming = { started: true }
  const withTiming = (stage: StageNoteStage, timing: object): StageNoteStage => ({ ...stage, timing: timing as StageNoteStage['timing'] })

  test.each([
    ['pending with a note', { ...pending, preNote: 'saved note' }, 'expanded'],
    ['pending without a note', pending, 'expanded'],
    ['pending, not started, long note', { ...pending, preNote: 'x'.repeat(5000) }, 'expanded'],
    ['running', { ...pending, status: 'running' as const, preNote: 'saved note' }, 'compact'],
    ['failed', { ...pending, status: 'failed' as const, preNote: 'saved note' }, 'compact'],
    ['done', { ...pending, status: 'done' as const, preNote: 'saved note' }, 'compact'],
  ])('%s opens %s', (_name, stage, mode) => {
    const { container } = renderNote({ stage })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', mode)
  })

  test('a Retry stage (pending, already started) opens compact', () => {
    const { container } = renderNote({ stage: withTiming({ ...pending, preNote: 'saved' }, startedTiming) })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
  })

  test('timing.started false wins over a running status fallback', () => {
    const { container } = renderNote({ stage: withTiming({ ...pending, status: 'ready', preNote: 'saved' }, notStarted) })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
  })

  test('a stored draft before the start opens the editor expanded even if compact was chosen', () => {
    const { container } = renderNote({ initialDraft: 'unsaved', initialPresentation: { presentation: 'compact', started: false } })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('unsaved')
  })

  test('a stored choice with the same start fact is restored', () => {
    const { container } = renderNote({ stage: { ...pending, status: 'running', preNote: 'n' }, initialPresentation: { presentation: 'expanded', started: true } })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
  })

  test('a stored choice made before the start gives way to the started default', () => {
    const { container } = renderNote({ stage: { ...pending, status: 'running', preNote: 'n' }, initialPresentation: { presentation: 'expanded', started: false } })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
  })

  test('the chosen presentation is reported on mount', () => {
    const onPresentationChange = vi.fn()
    renderNote({ stage: { ...pending, status: 'running', preNote: 'n' }, onPresentationChange })
    expect(onPresentationChange).toHaveBeenLastCalledWith('s1', { presentation: 'compact', started: true })
  })
})

describe('StageNote expand button', () => {
  const running: StageNoteStage = { ...pending, status: 'running', preNote: 'first line\n  second   line' }

  test('is a real button with aria-expanded and aria-controls pointing at the body', () => {
    const { container } = renderNote({ stage: running })
    const toggle = screen.getByRole('button', { name: 'Expand stage note' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    const body = container.querySelector(`[id="${toggle.getAttribute('aria-controls')}"]`)
    expect(body).not.toBeNull()
    expect(body).toHaveAttribute('hidden')
  })

  test('the compact row shows the note as one line preview inside the button', () => {
    renderNote({ stage: running })
    expect(screen.getByRole('button', { name: 'Expand stage note' })).toHaveTextContent('first line second line')
    expect(screen.getByText('Read only')).toBeInTheDocument()
  })

  test('compact hides the body content from the document and the tab order', () => {
    renderNote({ stage: running, initialDraft: 'unsent' })
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Discard draft' })).toBeNull()
    expect(screen.queryByText('Before start')).toBeNull()
    expect(screen.getByText('Unsaved draft')).toBeInTheDocument()
  })

  test('click expands, click collapses, without any request or draft change', () => {
    const save = vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const { container, onDraftChange } = renderNote({ stage: running })
    const section = container.querySelector('section')
    screen.getByRole('button', { name: 'Expand stage note' }).focus()
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(section).toHaveAttribute('data-presentation', 'expanded')
    const toggle = screen.getByRole('button', { name: 'Collapse stage note' })
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveFocus()
    fireEvent.click(toggle)
    expect(section).toHaveAttribute('data-presentation', 'compact')
    expect(save).not.toHaveBeenCalled()
    expect(onDraftChange).not.toHaveBeenCalled()
  })

  test('is a native type=button element, so Enter and Space activate it without custom key handling', () => {
    renderNote({ stage: running })
    const toggle = screen.getByRole('button', { name: 'Expand stage note' })
    expect(toggle.tagName).toBe('BUTTON')
    expect(toggle).toHaveAttribute('type', 'button')
  })

  test('expanding a pending note does not focus an editor', () => {
    renderNote({ stage: { ...pending, preNote: 'saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Collapse stage note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  test('a manually collapsed empty pending note keeps the title row and Add note without a preview', () => {
    renderNote()
    fireEvent.click(screen.getByRole('button', { name: 'Collapse stage note' }))
    expect(screen.getByRole('button', { name: 'Add note' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Expand stage note' })).toHaveTextContent(/^Stage note$/)
  })

  test('Add note from the compact row expands the card and focuses the editor', () => {
    const { container } = renderNote({ stage: { ...pending, preNote: 'saved' } })
    fireEvent.click(screen.getByRole('button', { name: 'Collapse stage note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveFocus()
  })

  test('is locked while the editor is open before the start and while saving', async () => {
    let finish: () => void = () => {}
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((resolve) => { finish = resolve }))
    renderNote({ initialDraft: 'typing' })
    expect(screen.getByRole('button', { name: 'Collapse stage note' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    expect(screen.getByRole('button', { name: 'Collapse stage note' })).toBeDisabled()
    await act(async () => { finish() })
  })

  test('an unsaved draft after the start can be collapsed and keeps its text', () => {
    renderNote({ stage: running, initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('unsent')
    fireEvent.click(screen.getByRole('button', { name: 'Collapse stage note' }))
    expect(screen.getByText('Unsaved draft')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('unsent')
  })

  test('Discard draft after the start returns to the row and focuses the expand button', () => {
    const { container } = renderNote({ stage: running, initialDraft: 'unsent' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
    expect(screen.getByRole('button', { name: 'Expand stage note' })).toHaveFocus()
  })

  test('manual choices are reported with the start fact', () => {
    const onPresentationChange = vi.fn()
    renderNote({ stage: running, onPresentationChange })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(onPresentationChange).toHaveBeenLastCalledWith('s1', { presentation: 'expanded', started: true })
  })
})

describe('StageNote first start', () => {
  const view = (stage: StageNoteStage, props: Props = {}) => (
    <StageNote stage={stage} onDraftChange={vi.fn()} onChanged={vi.fn()} {...props} />
  )

  test('collapses once when the stage starts, and a later manual expand survives polls', () => {
    const onPresentationChange = vi.fn()
    const { container, rerender } = render(view({ ...pending, preNote: 'saved' }, { onPresentationChange }))
    const section = container.querySelector('section')
    expect(section).toHaveAttribute('data-presentation', 'expanded')
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }, { onPresentationChange }))
    expect(section).toHaveAttribute('data-presentation', 'compact')
    expect(onPresentationChange).toHaveBeenLastCalledWith('s1', { presentation: 'compact', started: true })
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }, { onPresentationChange }))
    rerender(view({ ...pending, status: 'done', preNote: 'saved' }, { onPresentationChange }))
    expect(section).toHaveAttribute('data-presentation', 'expanded')
  })

  test('Retry back to pending does not restore the before-start behaviour', () => {
    const { container, rerender } = render(view({ ...pending, status: 'failed', preNote: 'saved' }))
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    rerender(view({ ...pending, status: 'pending', preNote: 'saved', timing: { started: true } as StageNoteStage['timing'] }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
    rerender(view({ ...pending, status: 'ready', preNote: 'saved' }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
  })

  test('timing disappearing after the start does not reopen the first-start collapse', () => {
    const { container, rerender } = render(view({ ...pending, preNote: 'saved' }))
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }))
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    rerender(view({ ...pending, status: 'pending', preNote: 'saved' }))
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
  })

  test('starting while typing keeps the draft and collapses behind Unsaved draft', () => {
    const { container, rerender } = render(view(pending, { initialDraft: 'half typed' }))
    rerender(view({ ...pending, status: 'running' }, { initialDraft: 'half typed' }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
    expect(screen.getByText('Unsaved draft')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('half typed')
  })

  test('starting during a save does not cancel the request and keeps the 409 error visible after expanding', async () => {
    let reject: (reason: unknown) => void = () => {}
    const save = vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((_resolve, rej) => { reject = rej }))
    const onChanged = vi.fn()
    const { container, rerender } = render(view(pending, { initialDraft: 'late', onChanged }))
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    rerender(view({ ...pending, status: 'running' }, { initialDraft: 'late', onChanged }))
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
    await act(async () => { reject(new runClient.FlowApiError('/api/stages/s1/note', 409, undefined)) })
    expect(save).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Expand stage note' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Saved, but the stage started')
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveValue('late')
  })

  test('a successful save started at the same moment ends compact', async () => {
    let resolve: () => void = () => {}
    vi.spyOn(runClient, 'setStageNote').mockReturnValue(new Promise<void>((res) => { resolve = res }))
    const { container, rerender } = render(view({ ...pending, preNote: 'old' }, { initialDraft: 'new' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    rerender(view({ ...pending, status: 'running', preNote: 'old' }, { initialDraft: 'new' }))
    await act(async () => { resolve() })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
  })

  test('focus inside the note moves to the expand button on auto-collapse', () => {
    const { rerender } = render(view({ ...pending, preNote: 'saved' }))
    fireEvent.click(screen.getByRole('button', { name: 'Edit note' }))
    expect(screen.getByRole('textbox', { name: 'Stage note' })).toHaveFocus()
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }))
    expect(screen.getByRole('button', { name: 'Expand stage note' })).toHaveFocus()
  })

  test('focus outside the note is left alone on auto-collapse', () => {
    const outside = document.createElement('input')
    document.body.append(outside)
    const { rerender } = render(view({ ...pending, preNote: 'saved' }))
    outside.focus()
    rerender(view({ ...pending, status: 'running', preNote: 'saved' }))
    expect(outside).toHaveFocus()
    outside.remove()
  })

  test('Save and Cancel return to the expanded default before the start', async () => {
    vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const cancelled = renderNote({ initialDraft: 'x', initialPresentation: { presentation: 'compact', started: false } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(cancelled.container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
    cancelled.unmount()

    const saved = renderNote({ initialDraft: 'x', initialPresentation: { presentation: 'compact', started: false } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(saved.onChanged).toHaveBeenCalled())
    expect(saved.container.querySelector('section')).toHaveAttribute('data-presentation', 'expanded')
  })

  test('Save and Cancel return to compact on an already started Retry stage', async () => {
    vi.spyOn(runClient, 'setStageNote').mockResolvedValue()
    const retry = { ...pending, preNote: 'old' }
    const cancelled = renderNote({ stage: retry, initialDraft: 'x', initialPresentation: { presentation: 'expanded', started: true } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(cancelled.container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
    cancelled.unmount()

    const saved = renderNote({ stage: retry, initialDraft: 'x', initialPresentation: { presentation: 'expanded', started: true } })
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }))
    await waitFor(() => expect(saved.onChanged).toHaveBeenCalled())
    expect(saved.container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
  })

  test('a remembered start survives a remount of a Retry stage without timing', () => {
    const onPresentationChange = vi.fn()
    const retry = { ...pending, preNote: 'saved' }
    const { container } = renderNote({ stage: retry, initialPresentation: { presentation: 'compact', started: true }, onPresentationChange })
    expect(container.querySelector('section')).toHaveAttribute('data-presentation', 'compact')
    expect(onPresentationChange).toHaveBeenLastCalledWith('s1', { presentation: 'compact', started: true })
  })

  test('typing never reports a presentation change', () => {
    const onPresentationChange = vi.fn()
    renderNote({ initialDraft: 'a', onPresentationChange })
    onPresentationChange.mockClear()
    fireEvent.change(screen.getByRole('textbox', { name: 'Stage note' }), { target: { value: 'ab' } })
    expect(onPresentationChange).not.toHaveBeenCalled()
  })
})
