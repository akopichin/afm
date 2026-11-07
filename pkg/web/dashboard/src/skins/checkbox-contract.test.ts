import { describe, expect, it } from 'vitest'
import { declared, media, rules } from './css-blocks'

// Every shipped stylesheet, read as source (?raw), so one rule can be proven the only owner of checkbox colours.
const shipped = import.meta.glob(
  ['../../skins/base/*.css', '../../skins/*/index.css', '../../public/chat-composer.css', '../../public/side-agent.css'],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>

const resetCss = shipped['../../skins/base/reset.css'] ?? ''
const feedWorkspaceCss = shipped['../../skins/base/feed-workspace.css'] ?? ''
const CHECKBOX = 'input[type="checkbox"]'

describe('checkbox contract — the single themed rule in reset.css', () => {
  it('draws the box from semantic tokens without the native appearance', () => {
    expect(declared(resetCss, CHECKBOX, 'appearance')).toBe('none')
    expect(declared(resetCss, CHECKBOX, 'border')).toContain('var(--control-border)')
    expect(declared(resetCss, CHECKBOX, 'background')).toBe('var(--surface-canvas)')
    expect(declared(resetCss, `${CHECKBOX}:hover`, 'border-color')).toBe('var(--accent-primary)')
    expect(declared(resetCss, `${CHECKBOX}:checked`, 'background')).toBe('var(--accent-primary)')
    expect(declared(resetCss, `${CHECKBOX}:checked`, 'border-color')).toBe('var(--accent-primary)')
    expect(declared(resetCss, `${CHECKBOX}:focus-visible`, 'outline')).toContain('var(--focus-ring)')
  })

  it('paints the tick in the canvas colour and keeps it visible in forced colours', () => {
    expect(declared(resetCss, `${CHECKBOX}::before`, 'background-color')).toBe('var(--surface-canvas)')
    expect(declared(resetCss, `${CHECKBOX}::before`, 'clip-path')).toBeDefined()
    expect(declared(media(resetCss, '(forced-colors: active)'), `${CHECKBOX}::before`, 'background-color')).toBe('CanvasText')
  })

  it('mutes a disabled box by opacity because --text-disabled equals --control-border', () => {
    expect(declared(resetCss, `${CHECKBOX}:disabled`, 'opacity')).toBe('0.5')
  })
})

const CHECKBOX_SELECTOR =
  /type=["']?checkbox|feed-composer-mode-option input|file-tree-row input|team-create-conversation-participant input|team-board-notify-option input|send-dialog-confirm input|add-stage-dialog-deps input/
const APPEARANCE_PROPERTIES = /(^|;)\s*(color|background(-color|-image)?|border(-[a-z]+)*|outline(-color)?|appearance|accent-color)\s*:/

describe('checkbox contract — nothing else restyles a checkbox', () => {
  it('never uses accent-color in a shipped stylesheet', () => {
    const offenders = Object.entries(shipped).filter(([, css]) => /accent-color/.test(css)).map(([file]) => file)
    expect(offenders).toEqual([])
  })

  it('lets checkbox-targeted rules outside reset.css change layout only', () => {
    const offenders = Object.entries(shipped)
      .filter(([file]) => !file.endsWith('/base/reset.css'))
      .flatMap(([file, css]) =>
        rules(css)
          .filter(({ selectors, body }) => selectors.some((s) => CHECKBOX_SELECTOR.test(s)) && APPEARANCE_PROPERTIES.test(body))
          .map(({ selectors }) => `${file}: ${selectors.join(', ')}`),
      )
    expect(offenders).toEqual([])
  })
})

describe('checkbox contract — the slider keeps its hidden input', () => {
  it('hides the input of a real slider only', () => {
    const hiding = rules(feedWorkspaceCss).filter(({ body }) => /opacity:\s*0\b/.test(body) && body.includes('width: 1px'))
    expect(hiding.flatMap(({ selectors }) => selectors)).toEqual(['.switch:not(.feed-composer-mode-option) input'])
    const body = hiding[0]?.body ?? ''
    expect(body).toContain('position: absolute')
    expect(body).toContain('height: 1px')
    // The shared checkbox border would otherwise grow the 1px box to 2px.
    expect(body).toMatch(/border:\s*0\b/)
  })

  it('leaves the composer option checkbox to the shared rule', () => {
    const own = rules(feedWorkspaceCss).filter(({ selectors }) => selectors.some((s) => s.includes('.feed-composer-mode-option input')))
    expect(own).toEqual([])
  })
})
