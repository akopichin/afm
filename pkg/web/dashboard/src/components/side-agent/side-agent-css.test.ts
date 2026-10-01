import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import sideAgentCss from '../../../public/side-agent.css?raw'

describe('side-agent.css visibility contract', () => {
  let style: HTMLStyleElement
  let overlay: HTMLDivElement

  beforeEach(() => {
    style = document.createElement('style')
    style.textContent = sideAgentCss
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
})
