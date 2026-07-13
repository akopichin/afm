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
