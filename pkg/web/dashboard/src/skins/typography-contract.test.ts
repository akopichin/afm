import { describe, it, expect } from 'vitest'
import headerCss from '../../skins/base/header.css?raw'
import runMetricsCss from '../../skins/base/run-metrics.css?raw'
import layoutCss from '../../skins/base/layout.css?raw'
import stagesListCss from '../../skins/base/stages-list.css?raw'
import feedWorkspaceCss from '../../skins/base/feed-workspace.css?raw'
import meshCss from '../../skins/base/mesh.css?raw'
import assistantsCss from '../../skins/base/assistants.css?raw'
import chatComposerCss from '../../public/chat-composer.css?raw'
import gogaCss from '../../skins/goga/index.css?raw'
import { declared, media } from './css-blocks'

// Typography and compact-area contract: the sizes the design review fixed. Heights come from min-height and
// padding (never a fixed height), so wrapped text can still grow an area.

describe('typography contract — run header', () => {
  it('is 72px tall with a 3px gap between the run name and its caption', () => {
    expect(declared(headerCss, '.global-header', 'min-height')).toBe('72px')
    expect(declared(headerCss, '.gh-flow', 'gap')).toBe('3px')
  })
  it('sets the run name 22px, caption 13px, wordmark 26px and logo 32px', () => {
    expect(declared(headerCss, '.gh-flow-name', 'font-size')).toBe('22px')
    expect(declared(headerCss, '.gh-flow-desc', 'font-size')).toBe('13px')
    expect(declared(headerCss, '.gh-wordmark', 'font-size')).toBe('26px')
    expect(declared(headerCss, '.gh-mark', 'width')).toBe('32px')
    expect(declared(headerCss, '.gh-mark', 'height')).toBe('32px')
  })
  it('keeps the goga wordmark and logo at the same sizes', () => {
    expect(declared(gogaCss, '.gh-wordmark::before', 'font-size')).toBe('26px')
    expect(declared(gogaCss, '.gh-wordmark::after', 'font-size')).toBe('26px')
    expect(declared(gogaCss, '.gh-mark', 'width')).toBe('32px')
    expect(declared(gogaCss, '.gh-mark', 'height')).toBe('32px')
  })
  it('on a phone wraps an 18px run name beside a 28px logo', () => {
    const phone = media(headerCss, '(max-width: 600px)')
    expect(declared(phone, '.gh-flow-name', 'font-size')).toBe('18px')
    expect(declared(phone, '.gh-flow-name', 'white-space')).toBe('normal')
    expect(declared(phone, '.gh-mark', 'width')).toBe('28px')
    expect(declared(media(gogaCss, '(max-width: 600px)'), '.gh-mark', 'width')).toBe('28px')
  })
  it('on a phone a long run name takes at most two lines so the Feed keeps its height', () => {
    const phone = media(headerCss, '(max-width: 600px)')
    expect(declared(phone, '.gh-flow-name', '-webkit-line-clamp')).toBe('2')
    expect(declared(phone, '.gh-flow-name', 'line-clamp')).toBe('2')
    expect(declared(phone, '.gh-flow-name', 'overflow')).toBe('hidden')
  })
  it('sets metric labels 12px, values 14px and the connection state 12px', () => {
    expect(declared(runMetricsCss, '.metric-label', 'font-size')).toBe('12px')
    expect(declared(runMetricsCss, '.metric-value', 'font-size')).toBe('14px')
    expect(declared(headerCss, '.gh-connection', 'font-size')).toBe('12px')
  })
})

describe('typography contract — headings share one 16/24/600 style', () => {
  const heading = (css: string, selector: string): string[] =>
    ['font-size', 'line-height', 'font-weight'].map((property) => declared(css, selector, property) ?? 'unset')

  it.each([
    ['Stages', stagesListCss, '#stages-panel h2'],
    ['workspace tab', layoutCss, '.workspace-tab'],
    ['run conversations tab', layoutCss, '.run-conversations-toggle'],
    ['People', meshCss, '.team-roster-title'],
  ])('%s is 16px / 24px / 600', (_name, css, selector) => {
    expect(heading(css, selector)).toEqual(['16px', '24px', '600'])
  })

  it('an active tab keeps the same weight, so switching tabs does not shift the strip', () => {
    expect(declared(layoutCss, '.workspace-tab.active', 'font-weight')).toBeUndefined()
  })
})

describe('typography contract — compact service areas', () => {
  it('workspace tabs are ~45px: 10px padding around a 24px line plus the border', () => {
    expect(declared(layoutCss, '.workspace-tab', 'padding')).toBe('10px 0')
    expect(declared(layoutCss, '.workspace-tabs', 'min-height')).toBe('44px')
  })
  it('the stage bar is ~50px with 6px vertical padding and a 16px title', () => {
    expect(declared(layoutCss, '.workspace-header', 'min-height')).toBe('50px')
    expect(declared(layoutCss, '.workspace-header', 'padding')).toBe('6px 8px')
    expect(declared(layoutCss, '.workspace-title', 'font-size')).toBe('16px')
    expect(declared(layoutCss, '.status-badge', 'font-size')).toBe('11px')
    expect(declared(layoutCss, '.workspace-action', 'font-size')).toBe('12px')
    expect(declared(layoutCss, '.workspace-reply-needed', 'font-size')).toBe('12px')
  })
  it('the Feed filters are ~33px: row padding 0/6px, chips 4px, context 11px', () => {
    expect(declared(feedWorkspaceCss, '.feed-filter-row', 'padding')).toBe('0 8px 6px')
    expect(declared(feedWorkspaceCss, '.feed-filter-chip', 'padding')).toBe('4px 9px')
    expect(declared(feedWorkspaceCss, '.feed-filter-context', 'font-size')).toBe('11px')
  })
  it.each([
    ['run header', headerCss, '.global-header'],
    ['workspace tabs', layoutCss, '.workspace-tabs'],
    ['stage bar', layoutCss, '.workspace-header'],
    ['filters', feedWorkspaceCss, '.feed-filter-row'],
    ['feed composer', feedWorkspaceCss, '.feed-composer'],
  ])('%s has no fixed height', (_name, css, selector) => {
    expect(declared(css, selector, 'height')).toBeUndefined()
  })
})

describe('typography contract — Feed and composer text', () => {
  it('Feed body text is 15px, 14px on a phone', () => {
    expect(declared(feedWorkspaceCss, '.feed-item', 'font-size')).toBe('15px')
    expect(declared(feedWorkspaceCss, '.feed-conversation .feed-message .team-conversation-message-body', 'font-size')).toBe('15px')
    const phone = media(feedWorkspaceCss, '(max-width: 600px)')
    expect(declared(phone, '.feed-item', 'font-size')).toBe('14px')
    expect(declared(phone, '.feed-conversation .feed-message .team-conversation-message-body', 'font-size')).toBe('14px')
  })
  it('the author is 14px/600, the role 12px', () => {
    expect(declared(feedWorkspaceCss, '.feed-author-row .feed-actor', 'font-size')).toBe('14px')
    expect(declared(feedWorkspaceCss, '.feed-author-row .feed-actor', 'font-weight')).toBe('600')
    expect(declared(feedWorkspaceCss, '.feed-role', 'font-size')).toBe('12px')
  })
  it('the composer input is 15px, 16px on a phone (no iOS zoom)', () => {
    expect(declared(chatComposerCss, '.chat-composer-card .chat-composer-input', 'font-size')).toBe('15px')
    expect(declared(media(chatComposerCss, '(max-width: 600px)'), '.chat-composer-card .chat-composer-input', 'font-size')).toBe('16px')
  })
  it('the Feed composer is compact: 8/10px outer, 8px card, 6px after the tabs, a two-line input', () => {
    expect(declared(feedWorkspaceCss, '.feed-composer', 'padding')).toBe('8px 8px 10px')
    expect(declared(feedWorkspaceCss, '.feed-composer', 'gap')).toBe('6px')
    expect(declared(chatComposerCss, '.chat-composer-row.chat-composer-card', 'padding')).toBe('8px 12px')
    expect(declared(feedWorkspaceCss, '.feed-composer .chat-composer-card .chat-composer-input', 'min-height')).toBe('60px')
    expect(declared(feedWorkspaceCss, '.feed-composer .chat-composer-card .chat-composer-input', 'padding')).toBe('6px 0')
  })
  it('recipients are 12px and the hint 11px', () => {
    expect(declared(chatComposerCss, '.chat-composer-card .chat-composer-recipients', 'font-size')).toBe('12px')
    expect(declared(chatComposerCss, '.chat-composer-hint', 'font-size')).toBe('11px')
  })
})

describe('typography contract — Stages rail and People', () => {
  it('stage names are 14px and their status 12px', () => {
    expect(declared(stagesListCss, '.stage-name', 'font-size')).toBe('14px')
    expect(declared(stagesListCss, '.stage-status', 'font-size')).toBe('12px')
  })
  it('People: names 12px, details 11px, group titles 11px, hint and search 12px', () => {
    expect(declared(meshCss, '.stage-people .team-person-name', 'font-size')).toBe('12px')
    expect(declared(meshCss, '.stage-people .team-person-detail', 'font-size')).toBe('11px')
    expect(declared(meshCss, '.stage-people .team-roster-group > h3', 'font-size')).toBe('11px')
    expect(declared(meshCss, '.stage-people .team-roster-hint', 'font-size')).toBe('12px')
    expect(declared(meshCss, '.stage-people .team-roster-top input', 'font-size')).toBe('12px')
  })
  it('the People heading takes the shared 16px style, with no smaller override', () => {
    expect(declared(meshCss, '.stage-people .team-roster-title', 'font-size')).toBeUndefined()
  })
})

describe('typography contract — mention menu', () => {
  it('names are 14px/600, roles and aliases 12px, avatars 34px', () => {
    expect(declared(assistantsCss, '.assistant-mention-title', 'font-size')).toBe('14px')
    expect(declared(assistantsCss, '.assistant-mention-title', 'font-weight')).toBe('600')
    expect(declared(assistantsCss, '.assistant-mention-description', 'font-size')).toBe('12px')
    expect(declared(assistantsCss, '.assistant-mention-id', 'font-size')).toBe('12px')
    expect(declared(assistantsCss, '.assistant-mention-option .mesh-avatar', 'width')).toBe('34px')
    expect(declared(assistantsCss, '.assistant-mention-option .mesh-avatar', 'height')).toBe('34px')
  })
  it('rows have more air: a 14px gap, 13px/12px padding and a 3px margin', () => {
    expect(declared(assistantsCss, '.assistant-mention-option', 'gap')).toBe('14px')
    expect(declared(assistantsCss, '.assistant-mention-option', 'padding')).toBe('13px 12px')
    expect(declared(assistantsCss, '.assistant-mention-item', 'margin-block')).toBe('3px')
    expect(declared(assistantsCss, '.assistant-mention-person', 'gap')).toBe('4px')
  })
  it('the menu height is limited like the prototype, by the viewport', () => {
    expect(declared(chatComposerCss, '.assistant-mention-menu', 'max-height')).toBe('min(560px, calc(100dvh - 120px))')
  })
})
