import { describe, it, expect } from 'vitest'
import layoutCss from '../../skins/base/layout.css?raw'
import meshCss from '../../skins/base/mesh.css?raw'
import feedWorkspaceCss from '../../skins/base/feed-workspace.css?raw'
import { declared, media, rules, valueOf } from './css-blocks'

// Feed focus contract: the .dashboard-body--feed-focus class hides the run header, tabs, Stages, People and Run
// conversations with CSS only (the nodes stay mounted), and the stage bar gains Focus / waiting-item buttons.

// The left/right padding of a `padding` shorthand, in px; both sides must match.
function inlinePx(padding: string): number {
  const parts = padding.split(/\s+/)
  const right = parts[1] ?? parts[0] ?? ''
  const left = parts[3] ?? right
  expect(left).toBe(right)
  return Number.parseFloat(right)
}

const FOCUS = '.dashboard-body--feed-focus'
const PHONE = '(max-width: 600px)'
const focusRules = [...rules(layoutCss), ...rules(meshCss), ...rules(feedWorkspaceCss)].filter((rule) =>
  rule.selectors.some((s) => s.includes(FOCUS) || s.includes('.feed-focus-')),
)
const hiddenInFocus = (query: string): string[] =>
  focusRules
    .filter((rule) => rule.media === query && valueOf(rule.body, 'display') === 'none')
    .flatMap((rule) => rule.selectors.filter((s) => s.startsWith(FOCUS)))
    .sort()

describe('feed focus contract — what the mode hides', () => {
  it('lets the hidden attribute beat the run header display: grid', () => {
    expect(declared(layoutCss, '.global-header[hidden]', 'display')).toBe('none')
  })

  it('hides Stages, its divider, the tabs, People and Run conversations at every width, and nothing else', () => {
    expect(hiddenInFocus('')).toEqual(
      [
        `${FOCUS} .rail-slot`,
        `${FOCUS} .rail-resizer`,
        `${FOCUS} .rail-scrim`,
        `${FOCUS} .rail-toggle`,
        `${FOCUS} .workspace-tabs`,
        `${FOCUS} .run-conversations-toggle`,
        `${FOCUS} .workspace-header .stage-people-toggle`,
        `${FOCUS} .stage-people`,
        `${FOCUS} .stage-people-scrim`,
        `${FOCUS} .run-conversations`,
        `${FOCUS} .run-conversations-scrim`,
      ].sort(),
    )
  })

  it('gives the Feed the whole width in one column', () => {
    expect(declared(layoutCss, `.dashboard-body${FOCUS}`, 'grid-template-columns')).toBe('minmax(0, 1fr)')
  })

  it('keeps the stage bar with Pause / Continue / Retry, stage buttons, filters and composer', () => {
    const kept = ['.workspace-header', '.workspace-action', '.stage-buttons', '.feed-filter-row', '.feed-scroll', '.feed-composer']
    const hidden = [...hiddenInFocus(''), ...hiddenInFocus(PHONE)]
    for (const selector of kept) expect(hidden.some((s) => s.endsWith(` ${selector}`)), selector).toBe(false)
  })
})

describe('feed focus contract — Focus and waiting-item buttons', () => {
  it('Focus is a 28px stage action with a 16px icon', () => {
    expect(declared(layoutCss, '.workspace-action', 'height')).toBe('28px')
    expect(declared(layoutCss, '.feed-focus-toggle', 'height')).toBeUndefined()
    expect(declared(layoutCss, '.feed-focus-toggle', 'padding')).toBe('4px 6px')
    expect(declared(layoutCss, '.feed-focus-toggle svg', 'width')).toBe('16px')
    expect(declared(layoutCss, '.feed-focus-toggle svg', 'height')).toBe('16px')
  })

  it('draws the Esc hint as a small key cap from tokens', () => {
    expect(declared(layoutCss, '.feed-focus-toggle kbd', 'border')).toBe('1px solid var(--control-border)')
    expect(declared(layoutCss, '.feed-focus-toggle kbd', 'color')).toBe('var(--text-secondary)')
    expect(declared(layoutCss, '.feed-focus-toggle kbd', 'font')).toBe('10px var(--font-ui)')
  })

  it('drops the label while the People panel is a drawer, but shows it in focus mode', () => {
    expect(declared(media(layoutCss, '(max-width: 1180px)'), '.feed-focus-toggle-label', 'display')).toBe('none')
    expect(declared(layoutCss, `${FOCUS} .feed-focus-toggle-label`, 'display')).toBe('inline')
  })

  it('hides the Esc hint on a phone', () => {
    expect(hiddenInFocus(PHONE)).toEqual([`${FOCUS} .feed-focus-toggle kbd`])
  })

  it('the buttons never wrap their label inside the 28px height', () => {
    expect(declared(layoutCss, '.feed-focus-toggle', 'white-space')).toBe('nowrap')
    expect(declared(layoutCss, '.feed-focus-wait', 'white-space')).toBe('nowrap')
    expect(declared(layoutCss, '.feed-focus-wait', 'padding')).toBe('4px 6px')
  })

  it('the waiting item takes the beacon tab tone: attention, danger for failures', () => {
    expect(declared(layoutCss, '.workspace-action.feed-focus-wait', 'color')).toBe('var(--attention-text)')
    for (const kind of ['failed', 'hook_failed']) {
      expect(declared(layoutCss, `.workspace-action.feed-focus-wait[data-kind="${kind}"]`, 'color')).toBe('var(--danger-text)')
    }
  })

  it('the waiting count is the beacon count badge', () => {
    for (const property of ['padding', 'border-radius', 'font-family', 'font-size', 'line-height', 'font-variant-numeric']) {
      expect(declared(layoutCss, '.feed-focus-wait-count', property), property).toBe(declared(layoutCss, '.workspace-tab-count', property))
    }
    const tone = (selector: string): string[] => ['background', 'color'].map((p) => declared(layoutCss, selector, p) ?? 'unset')
    expect(tone('.feed-focus-wait-count')).toEqual(tone('.workspace-tab-attention .workspace-tab-count'))
    for (const kind of ['failed', 'hook_failed']) {
      expect(tone(`.feed-focus-wait[data-kind="${kind}"] .feed-focus-wait-count`)).toEqual(
        tone(`.workspace-tab-attention[data-kind="${kind}"] .workspace-tab-count`),
      )
    }
  })
})

describe('feed focus contract — insets and layout guards', () => {
  // The scroller carries no padding (the virtual list measures rows): the inset sits on the rows.
  const FEED_PARTS = ['.feed-vrow', '.feed-filter-row', '.feed-composer']
  const insetOf = (css: string, selector: string): number =>
    inlinePx(declared(css, selector, 'padding') ?? declared(css, selector, 'padding-inline') ?? '')

  it('the Feed, filters and composer sit 20px from the window edge on wide screens', () => {
    const body = inlinePx(declared(layoutCss, '.workspace-body', 'padding') ?? '')
    for (const selector of FEED_PARTS) expect(body + insetOf(feedWorkspaceCss, selector), selector).toBe(20)
  })

  it('keeps the virtual scroller free of padding, gap and flex layout, with browser scroll anchoring off', () => {
    expect(declared(feedWorkspaceCss, '.feed-scroll', 'padding')).toBeUndefined()
    expect(declared(feedWorkspaceCss, '.feed-scroll', 'gap')).toBeUndefined()
    expect(declared(feedWorkspaceCss, '.feed-scroll', 'display')).toBeUndefined()
    expect(declared(feedWorkspaceCss, '.feed-scroll', 'overflow-anchor')).toBe('none')
  })

  it('keeps the 18px gap before every conversation bubble, even though conversation rows are flow rows', () => {
    expect(declared(feedWorkspaceCss, '.feed-vrow--conversation', 'padding-top')).toBe('18px')
    expect(declared(feedWorkspaceCss, '.feed-vrow--flow:not(.feed-vrow--start):not(.feed-vrow--conversation)', 'padding-top')).toBe('0')
  })

  it('and 16px on a phone', () => {
    const body = inlinePx(declared(media(layoutCss, PHONE), `${FOCUS} .workspace-body`, 'padding-inline') ?? '')
    const phoneFeed = media(feedWorkspaceCss, PHONE)
    for (const selector of FEED_PARTS) expect(body + insetOf(phoneFeed, selector), selector).toBe(16)
  })

  it('lets the stage bar actions wrap on a phone instead of running off the screen', () => {
    expect(declared(media(layoutCss, PHONE), `${FOCUS} .workspace-header-actions`, 'max-width')).toBe('100%')
  })

  it('uses colour tokens only, keeps panel widths and sets no fixed height except on icons', () => {
    expect(focusRules.length).toBeGreaterThan(0)
    for (const rule of focusRules) {
      const where = `${rule.media} ${rule.selectors.join(', ')}`
      expect(rule.body, where).not.toMatch(/#[0-9a-f]{3,8}\b|\b(?:rgb|hsl)a?\(\s*\d/i)
      expect(rule.body, where).not.toMatch(/--(?:rail|people|run-conversations)-width\s*:/)
      if (!rule.selectors.every((s) => s.endsWith(' svg'))) expect(valueOf(rule.body, 'height'), where).toBeUndefined()
    }
  })

  it('keeps the jump-to-latest icon 16px / 30px from the corner', () => {
    const overlay = '.jump-latest--overlay'
    expect(declared(layoutCss, overlay, 'right')).toBe('16px')
    expect(declared(layoutCss, overlay, 'bottom')).toBe('30px')
  })
})
