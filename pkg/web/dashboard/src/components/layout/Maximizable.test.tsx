import { useState } from 'react'
import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MaximizeProvider, Maximizable, useMaximize } from './Maximizable'

function Toggle({ id }: { id: string }) {
  const { toggle } = useMaximize()
  return <button onClick={() => toggle(id)}>toggle</button>
}

describe('Maximizable', () => {
  it('рендерит inline; по toggle уходит в overlay-портал; Esc сворачивает', () => {
    const { container } = render(
      <MaximizeProvider>
        <Maximizable id="plan">
          <p>plan-content</p>
        </Maximizable>
        <Toggle id="plan" />
      </MaximizeProvider>,
    )
    expect(container.querySelector('.maximize-overlay')).toBeNull()
    expect(screen.getByText('plan-content')).toBeInTheDocument()

    fireEvent.click(screen.getByText('toggle'))
    expect(document.querySelector('.maximize-overlay')).not.toBeNull()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(document.querySelector('.maximize-overlay')).toBeNull()
  })
})

function Counter() {
  const [count, setCount] = useState(0)
  return (
    <button onClick={() => setCount((c) => c + 1)}>{`clicks:${count}`}</button>
  )
}

describe('Maximizable state preservation', () => {
  it('preserves child component state across maximize and restore (no remount)', () => {
    render(
      <MaximizeProvider>
        <Maximizable id="feed">
          <Counter />
        </Maximizable>
        <Toggle id="feed" />
      </MaximizeProvider>,
    )

    fireEvent.click(screen.getByText('clicks:0'))
    expect(screen.getByText('clicks:1')).toBeInTheDocument()

    // Maximize — if Maximizable remounts its children, this resets to clicks:0.
    fireEvent.click(screen.getByText('toggle'))
    expect(screen.getByText('clicks:1')).toBeInTheDocument()

    fireEvent.click(screen.getByText('clicks:1'))
    expect(screen.getByText('clicks:2')).toBeInTheDocument()

    // Restore — same check in the other direction.
    fireEvent.click(screen.getByText('toggle'))
    expect(screen.getByText('clicks:2')).toBeInTheDocument()
  })
})
