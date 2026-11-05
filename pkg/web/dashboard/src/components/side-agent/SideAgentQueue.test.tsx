import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { SideAgentQueue, type SideAgentQueueProps } from './SideAgentQueue'
import type { QueuedMessage } from '../../hooks/use-side-agent'

const msg = (id: string, text = `text ${id}`, stageId = ''): QueuedMessage => ({ messageId: id, text, stageId, seq: 1 })

function renderQueue(over: Partial<SideAgentQueueProps> = {}) {
  const props: SideAgentQueueProps = {
    queue: [msg('b'), msg('d')],
    removal: {},
    stages: [{ id: 'build', title: 'Build stage' }],
    onRemove: vi.fn(),
    onFocusComposer: vi.fn(),
    ...over,
  }
  return { props, ...render(<SideAgentQueue {...props} />) }
}

describe('SideAgentQueue', () => {
  it('renders nothing for an empty queue', () => {
    const { container } = renderQueue({ queue: [] })
    expect(container).toBeEmptyDOMElement()
  })

  it('shows Queued (N), each text, its context and a Remove action; says delivery is not reading', () => {
    renderQueue({ queue: [msg('b', 'first', 'build'), msg('d', 'second')] })
    expect(screen.getByRole('region', { name: 'Queued messages' })).toBeTruthy()
    expect(screen.getByText('Queued (2)')).toBeTruthy()
    expect(screen.getByText('first')).toBeTruthy()
    expect(screen.getByText('second')).toBeTruthy()
    expect(screen.getByText('Context: Build stage')).toBeTruthy()
    expect(screen.getByText('Context: No stage')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Remove from queue' })).toHaveLength(2)
    expect(screen.getByText(/not read/i)).toBeTruthy()
  })

  it('falls back to the raw stage id for an unknown stage', () => {
    renderQueue({ queue: [msg('b', 'x', 'gone')] })
    expect(screen.getByText('Context: gone')).toBeTruthy()
  })

  it('renders message text as plain text, not markup', () => {
    renderQueue({ queue: [msg('b', '<b>bold</b> **x**')] })
    expect(screen.getByText('<b>bold</b> **x**')).toBeTruthy()
  })

  it('removes without a confirmation dialog', () => {
    const { props } = renderQueue()
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove from queue' })[1]!)
    expect(props.onRemove).toHaveBeenCalledWith('d')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows the removing state on an entry and ignores another click', () => {
    const { props } = renderQueue({ removal: { b: { status: 'removing' } } })
    const removing = screen.getByRole('button', { name: 'Removing…' })
    expect(removing).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(removing)
    expect(props.onRemove).not.toHaveBeenCalled()
  })

  it('shows the error with a Retry action that removes again', () => {
    const { props } = renderQueue({ removal: { b: { status: 'error', message: 'Request failed' } } })
    expect(screen.getByRole('alert')).toHaveTextContent('Request failed')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(props.onRemove).toHaveBeenCalledWith('b')
  })

  it('collapses a long message and expands it on demand', () => {
    renderQueue({ queue: [msg('b', 'x'.repeat(400))] })
    const toggle = screen.getByRole('button', { name: 'Show more' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: 'Show less' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('has no expand control for a short message', () => {
    renderQueue({ queue: [msg('b', 'short')] })
    expect(screen.queryByRole('button', { name: 'Show more' })).toBeNull()
  })

  describe('focus after the entry leaves the queue', () => {
    function Harness({ onFocusComposer }: { onFocusComposer: () => void }) {
      const [queue, setQueue] = useState([msg('a'), msg('b'), msg('c')])
      return (
        <>
          <button type="button" onClick={() => setQueue((q) => q.filter((m) => m.messageId !== 'b'))}>confirm b</button>
          <button type="button" onClick={() => setQueue([])}>confirm all</button>
          <SideAgentQueue queue={queue} removal={{}} stages={[]} onRemove={() => {}} onFocusComposer={onFocusComposer} />
        </>
      )
    }

    it('moves focus to the next entry action', () => {
      render(<Harness onFocusComposer={vi.fn()} />)
      const [, second] = screen.getAllByRole('button', { name: 'Remove from queue' })
      second!.focus()
      fireEvent.click(second!)
      fireEvent.click(screen.getByRole('button', { name: 'confirm b' }))
      expect(screen.getAllByRole('button', { name: 'Remove from queue' })[1]).toHaveFocus() // c took b's place
    })

    it('falls back to the composer when the queue becomes empty', () => {
      const onFocusComposer = vi.fn()
      render(<Harness onFocusComposer={onFocusComposer} />)
      const [first] = screen.getAllByRole('button', { name: 'Remove from queue' })
      first!.focus()
      fireEvent.click(first!)
      fireEvent.click(screen.getByRole('button', { name: 'confirm all' }))
      expect(onFocusComposer).toHaveBeenCalledTimes(1)
    })

    it('does not steal focus the user already moved elsewhere', () => {
      const onFocusComposer = vi.fn()
      render(<Harness onFocusComposer={onFocusComposer} />)
      const [first] = screen.getAllByRole('button', { name: 'Remove from queue' })
      fireEvent.click(first!)
      const other = screen.getByRole('button', { name: 'confirm b' })
      other.focus()
      fireEvent.click(other)
      expect(other).toHaveFocus()
      expect(onFocusComposer).not.toHaveBeenCalled()
    })
  })
})
