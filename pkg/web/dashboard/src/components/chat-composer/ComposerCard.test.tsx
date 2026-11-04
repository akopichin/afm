import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ComposerCard, ComposerHint, ComposerRecipients } from './ComposerCard'

describe('ComposerCard', () => {
  it('draws the input row as a card, tinted while paused', () => {
    const { container } = render(<><ComposerCard>a</ComposerCard><ComposerCard paused>b</ComposerCard></>)
    const [card, paused] = Array.from(container.children) as [Element, Element]
    expect(card).toHaveClass('chat-composer-row', 'chat-composer-card')
    expect(card).not.toHaveClass('paused')
    expect(paused).toHaveClass('chat-composer-row', 'chat-composer-card', 'paused')
  })

  it('labels the recipients row "To" by default and takes a loop label', () => {
    const { container } = render(<><ComposerRecipients>x</ComposerRecipients><ComposerRecipients label="Loop" ariaLabel="Loop target" className="mesh-loop-target">y</ComposerRecipients></>)
    const [to, loop] = Array.from(container.children) as [Element, Element]
    expect(to).toHaveAttribute('aria-label', 'Recipients')
    expect(to.querySelector('.mesh-recipients-label')).toHaveTextContent('To')
    expect(loop).toHaveAttribute('aria-label', 'Loop target')
    expect(loop).toHaveClass('mesh-loop-target')
    expect(loop.querySelector('.mesh-recipients-label')).toHaveTextContent('Loop')
  })

  it('puts the send shortcut next to the note', () => {
    const { container } = render(<ComposerHint note="Note." />)
    expect(container.textContent).toBe('Note.Enter to send · Shift+Enter for a new line')
  })
})
