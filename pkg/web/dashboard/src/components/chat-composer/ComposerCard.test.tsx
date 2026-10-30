import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ComposerCard, ComposerHint, ComposerRecipients } from './ComposerCard'

describe('ComposerCard', () => {
  it('draws the card, paused and plain-row variants of the input row', () => {
    const { container } = render(<><ComposerCard>a</ComposerCard><ComposerCard card={false} paused>b</ComposerCard></>)
    const [card, plain] = Array.from(container.children) as [Element, Element]
    expect(card).toHaveClass('chat-composer-row', 'chat-composer-card')
    expect(plain).toHaveClass('chat-composer-row', 'paused')
    expect(plain).not.toHaveClass('chat-composer-card')
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
