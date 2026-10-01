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
