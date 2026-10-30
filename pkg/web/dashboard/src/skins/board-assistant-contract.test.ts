import { describe, expect, it } from 'vitest'
import meshCss from '../../skins/base/mesh.css?raw'
import sideCss from '../../side-agent.css?raw'
import publicSideCss from '../../public/side-agent.css?raw'
import composerCss from '../../chat-composer.css?raw'

function block(css: string, selector: string): string {
  const start = css.indexOf(`${selector} {`)
  expect(start, `Missing ${selector}`).toBeGreaterThanOrEqual(0)
  const opening = css.indexOf('{', start)
  let depth = 1
  let end = opening + 1
  for (; end < css.length && depth > 0; end++) {
    if (css[end] === '{') depth++
    if (css[end] === '}') depth--
  }
  expect(depth, `Unclosed ${selector}`).toBe(0)
  return css.slice(opening + 1, end - 1)
}

describe('Board layout', () => {
  it('keeps three lanes within the available workspace and stacks them on mobile', () => {
    expect(block(meshCss, '.board-lanes')).toContain('grid-template-columns: repeat(3, minmax(0, 1fr))')
    expect(block(meshCss, '.board-lanes')).toContain('overflow-y: auto')
    expect(block(meshCss, '.team-board-list')).toContain('minmax(min(260px, 100%), 1fr)')
    const mobile = block(meshCss, '@media (max-width: 760px)')
    expect(block(mobile, '.board-lanes')).toContain('grid-template-columns: minmax(0, 1fr)')
  })

  it.each([['todo', 'todo'], ['in_progress', 'in-progress'], ['done', 'done']])('uses the shared %s palette for cards, labels and details', (status, token) => {
    const mapped = block(meshCss, `:is(.board-task-card, .board-task-status, .board-task-details)[data-status="${status}"]`)
    expect(mapped).toContain(`--task-fg: var(--task-${token})`)
    expect(mapped).toContain(`--task-bg: var(--task-${token}-bg)`)
    expect(mapped).toContain(`--task-line: var(--task-${token}-line)`)
    expect(block(meshCss, '.board-task-card')).toContain('background: var(--task-bg)')
    expect(block(meshCss, '.board-task-status')).toContain('color: var(--task-fg)')
  })

  it('reads secondary text where a card would paint muted text on its fixed task fill', () => {
    expect(block(meshCss, '.board-task-card')).toContain('--text-muted: var(--text-secondary)')
  })

  it('keeps the task heading outside a bounded, keyboard-focusable scrolling body', () => {
    const dialog = block(meshCss, '.board-task-details')
    expect(dialog).toContain('width: min(820px, calc(100vw - 32px))')
    expect(dialog).toContain('max-height: calc(100dvh - 32px)')
    expect(dialog).toContain('padding: 0')
    expect(dialog).toContain('overflow: hidden')
    expect(block(meshCss, '.board-task-details-top')).toContain('flex: 0 0 auto')
    const body = block(meshCss, '.board-task-details-body')
    expect(body).toContain('min-height: 0')
    expect(body).toContain('overflow-y: auto')
    expect(block(meshCss, '.board-task-details-body:focus-visible')).toContain('var(--focus-ring)')
    expect(block(meshCss, '.team-board-entry-artifacts > li')).toContain('overflow-wrap: anywhere')
    expect(meshCss).not.toMatch(/\.board-task-details-assignees\s+li\s*\{/)
  })

  it('gives Board form controls solid borders, readable placeholders and focus indicators', () => {
    expect(block(meshCss, '.board-task-details-assign select')).toContain('border: 1px solid var(--control-border)')
    expect(block(meshCss, '.board-view-assignee')).toContain('border: 1px solid var(--control-border)')
    expect(block(meshCss, '.board-view-search::placeholder')).toContain('var(--text-placeholder)')
    expect(block(meshCss, '.board-task-details-assign select:focus-visible')).toContain('var(--focus-ring)')
  })

  it('stops card hover motion under reduced motion', () => {
    const media = meshCss.slice(meshCss.indexOf('@media (prefers-reduced-motion: reduce)', meshCss.indexOf('.board-new-task-title')))
    expect(block(media, '.board-task-card')).toContain('transition: none')
    expect(block(media, '.board-task-card:hover')).toContain('transform: none')
  })
})

describe('Assistant layout', () => {
  it('ships the same structural CSS in the source and Vite public copy', () => {
    expect(publicSideCss).toBe(sideCss)
  })

  it('bounds desktop size and resets both height caps for mobile and short viewports', () => {
    const dialog = block(sideCss, '.side-agent-modal')
    expect(dialog).toContain('width: min(1040px, calc(100vw - 48px))')
    expect(dialog).toContain('height: min(760px, calc(100dvh - 64px))')
    expect(dialog).toContain('box-sizing: border-box')
    for (const query of ['@media (max-width: 720px)', '@media (max-height: 600px)']) {
      const mobile = block(sideCss, query)
      const modal = block(mobile, '.side-agent-modal')
      expect(modal).toContain('height: calc(100dvh - 16px)')
      expect(modal).toContain('max-height: calc(100dvh - 16px)')
    }
  })

  it('scrolls history independently while keeping the header, context and composer pinned', () => {
    expect(block(sideCss, '.side-agent-modal')).toContain('overflow: hidden')
    for (const selector of ['.side-agent-header', '.side-agent-context-row', '.side-agent-composer']) {
      expect(block(sideCss, selector)).toContain('flex: 0 0 auto')
    }
    expect(block(sideCss, '.side-agent-body')).toContain('min-height: 0')
    expect(block(sideCss, '.side-agent-history')).toContain('overflow-y: auto')
    expect(block(sideCss, '.side-agent-history')).toContain('min-width: 0')
    expect(block(sideCss, '.side-agent-history:focus-visible')).toContain('var(--focus-ring)')
    expect(block(sideCss, '.side-agent-composer .chat-composer-input')).toContain('max-height: min(200px, 22dvh)')
    expect(block(sideCss, '.side-agent-quote-chip-body')).toContain('overflow-wrap: anywhere')
    expect(block(composerCss, '.chat-composer-toolbar')).toContain('display: flex')
    expect(sideCss).not.toMatch(/\.side-agent-composer \.chat-send,\s*\.side-agent-composer \.chat-stop/)
  })

  it('keeps close and context controls visible and distinguishable on narrow screens', () => {
    expect(block(sideCss, '.side-agent-header')).toContain('flex-wrap: wrap')
    expect(block(sideCss, '.side-agent-close')).toContain('flex: 0 0 auto')
    expect(block(sideCss, '.side-agent-context-select')).toContain('min-width: 0')
    expect(block(sideCss, '.side-agent-context-select')).toContain('var(--control-border)')
    expect(block(sideCss, '.side-agent-attention-shortcut')).toContain('var(--control-border)')
    expect(block(sideCss, '.side-agent-close:focus-visible')).toContain('var(--focus-ring)')
  })

  it('stops modal, quote and history motion under reduced motion and disables closing hit-testing', () => {
    const start = sideCss.lastIndexOf('@media (prefers-reduced-motion: reduce)')
    const reduced = block(sideCss.slice(start), '@media (prefers-reduced-motion: reduce)')
    expect(reduced).toContain('.side-agent-overlay *')
    expect(reduced).toContain('animation: none !important')
    expect(reduced).toContain('transition: none !important')
    expect(reduced).toContain('scroll-behavior: auto !important')
    expect(block(sideCss, '.side-agent-overlay.side-agent-closing')).toContain('pointer-events: none')
  })
})
