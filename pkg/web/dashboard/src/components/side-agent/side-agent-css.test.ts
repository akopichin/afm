import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import headerCss from '../../../public/skins/base/header.css?raw'
import sideAgentCss from '../../../public/side-agent.css?raw'

describe('side-agent.css visibility contract', () => {
  let style: HTMLStyleElement
  let overlay: HTMLDivElement

  beforeEach(() => {
    style = document.createElement('style')
    style.textContent = `${headerCss}\n${sideAgentCss}`
    document.head.appendChild(style)

    overlay = document.createElement('div')
    overlay.className = 'side-agent-overlay side-agent-closed'
    document.body.appendChild(overlay)
  })

  afterEach(() => {
    overlay.remove()
    style.remove()
  })

  it('keeps a mounted closed overlay invisible and makes it flex only when shown', () => {
    overlay.hidden = true
    expect(getComputedStyle(overlay).display).toBe('none')

    overlay.hidden = false
    expect(getComputedStyle(overlay).display).toBe('flex')
  })

  it('lets the header button fit its one-line label instead of keeping the icon width', () => {
    const actions = document.createElement('div')
    actions.className = 'header-actions'

    const button = document.createElement('button')
    button.className = 'icon-btn gh-side-agent-btn'
    const label = document.createElement('span')
    label.className = 'gh-side-agent-label'
    label.textContent = 'Side agent'
    button.appendChild(label)
    actions.appendChild(button)
    document.body.appendChild(actions)

    expect(getComputedStyle(button).width).toBe('auto')
    expect(getComputedStyle(button).height).toBe('34px')
    expect(getComputedStyle(button).paddingLeft).toBe('14px')
    expect(getComputedStyle(button).whiteSpace).toBe('nowrap')
    expect(getComputedStyle(label).whiteSpace).toBe('nowrap')

    actions.remove()
  })
})

// T7.6: the Side agent header button must stay a visible text button ("Side agent" label) at
// every viewport width, including the app's narrow breakpoint — never collapsed to an icon-only
// square the way .gh-files-btn is allowed to (Side agent is not in scope for that collapse).
describe('side-agent.css narrow-viewport contract (T7.6)', () => {
  it('never hides the "Side agent" label behind display:none at any breakpoint', () => {
    expect(sideAgentCss).not.toMatch(/\.gh-side-agent-label\s*\{[^}]*display:\s*none/)
  })

  it('never shrinks the button to a fixed icon-only width at any breakpoint', () => {
    expect(sideAgentCss).not.toMatch(/\.gh-side-agent-btn[^{]*\{[^}]*width:\s*38px/)
  })
})

describe('side-agent.css quote chip contract', () => {
  let style: HTMLStyleElement
  let body: HTMLPreElement

  beforeEach(() => {
    style = document.createElement('style')
    style.textContent = sideAgentCss
    document.head.appendChild(style)

    body = document.createElement('pre')
    body.className = 'side-agent-quote-chip-body'
    document.body.appendChild(body)
  })

  afterEach(() => {
    body.remove()
    style.remove()
  })

  it('scrolls a long multi-line quote instead of clamping it', () => {
    const computed = getComputedStyle(body)
    expect(computed.overflowY).toBe('auto')
    expect(computed.maxHeight).not.toBe('none')
    expect(parseFloat(computed.maxHeight)).toBeGreaterThan(0)
  })

  it('puts the chip on its own full-width line above the input instead of beside it', () => {
    // A long comment used to grow the chip inside the composer row and squeeze the
    // textarea into a sliver. The composer is a column: the chip takes a whole line above the input row.
    const composer = document.createElement('div')
    composer.className = 'side-agent-composer'
    const chip = document.createElement('div')
    chip.className = 'side-agent-quote-chip'
    composer.appendChild(chip)
    document.body.appendChild(composer)

    expect(getComputedStyle(composer).flexDirection).toBe('column')

    composer.remove()
  })
})
