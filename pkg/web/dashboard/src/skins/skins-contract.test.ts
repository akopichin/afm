import { describe, it, expect } from 'vitest'
// Authoritative source skins, inlined as strings by Vite (?raw). Typed by vite/client.
import tokensCss from '../../skins/base/tokens.css?raw'
import feedWorkspaceCss from '../../skins/base/feed-workspace.css?raw'
import planPanelCss from '../../skins/base/plan-panel.css?raw'

const tokens = () => tokensCss
const keyframe = (css: string, name: string) =>
  css.match(new RegExp(`@keyframes\\s+${name}\\s*\\{[\\s\\S]*?\\n\\}`))?.[0] ?? ''

describe('skins CSS contract — motion layer (Task 1)', () => {
  it('defines the motion tokens', () => {
    const t = tokens()
    expect(t).toMatch(/--motion-fast:\s*120ms/)
    expect(t).toMatch(/--motion-base:\s*240ms/)
    expect(t).toMatch(/--motion-slow:\s*420ms/)
    expect(t).toMatch(/--ease-out:/)
    expect(t).toMatch(/--ease-spring:/)
  })
  it('defines the three shared entrance keyframes', () => {
    const t = tokens()
    expect(t).toMatch(/@keyframes\s+quoteChipIn/)
    expect(t).toMatch(/@keyframes\s+quoteMarkPop/)
    expect(t).toMatch(/@keyframes\s+feedItemIn/)
  })
  it('quoteChipIn takes its glow from the active accent (no graphite-only -rgb)', () => {
    const b = keyframe(tokens(), 'quoteChipIn')
    expect(b).toMatch(/color-mix\(in srgb, var\(--accent-primary\)/)
    expect(b).not.toMatch(/--accent-primary-rgb/)
  })
  it('quoteMarkPop animates transform only — no opacity (avoids an end-of-anim opacity jump)', () => {
    expect(keyframe(tokens(), 'quoteMarkPop')).not.toMatch(/opacity/)
  })
})

describe('skins CSS contract — reply-to-thought chip (Task 2)', () => {
  const rule = (css: string, sel: string) =>
    css.match(new RegExp(`${sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{[\\s\\S]*?\\}`))?.[0] ?? ''
  it('.feed-reply-chip glows in via quoteChipIn', () => {
    expect(rule(feedWorkspaceCss, '.feed-reply-chip')).toMatch(/animation:\s*quoteChipIn/)
  })
  it('.feed-reply-chip-label springs its mark via quoteMarkPop', () => {
    expect(rule(feedWorkspaceCss, '.feed-reply-chip-label')).toMatch(/animation:\s*quoteMarkPop/)
  })
})

describe('skins CSS contract — line-comment quote (Task 3)', () => {
  it('.line-comment-quote glows in via quoteChipIn', () => {
    expect(planPanelCss).toMatch(/\.line-comment-quote\s*\{[\s\S]*?animation:\s*quoteChipIn[\s\S]*?\}/)
  })
  it('.line-comment-quote::before springs its mark via quoteMarkPop', () => {
    expect(planPanelCss).toMatch(/\.line-comment-quote::before\s*\{[\s\S]*?animation:\s*quoteMarkPop[\s\S]*?\}/)
  })
})

describe('skins CSS contract — feed item entrance (Task 4)', () => {
  it('.feed-item--enter animates via feedItemIn', () => {
    expect(feedWorkspaceCss).toMatch(/\.feed-item--enter\s*\{[\s\S]*?animation:\s*feedItemIn[\s\S]*?\}/)
  })
  it('.feed-item--enter runs at --motion-slow (420ms, more visible than the base speed)', () => {
    expect(feedWorkspaceCss).toMatch(
      /\.feed-item--enter\s*\{[\s\S]*?animation:\s*feedItemIn\s+var\(--motion-slow\)[\s\S]*?\}/,
    )
  })
  it('feedItemIn glows in via an accent box-shadow (no graphite-only -rgb)', () => {
    const b = keyframe(tokens(), 'feedItemIn')
    expect(b).toMatch(/color-mix\(in srgb, var\(--accent-primary\)/)
    expect(b).not.toMatch(/--accent-primary-rgb/)
  })
  it('feedItemIn glow fades out completely (no lingering halo)', () => {
    expect(keyframe(tokens(), 'feedItemIn')).toMatch(/box-shadow:\s*0 0 0 0 transparent/)
  })
  it('feedItemIn translate is strengthened to 10px', () => {
    expect(keyframe(tokens(), 'feedItemIn')).toMatch(/translateY\(10px\)/)
  })
  it('feedItemIn never sets background (glow must stay box-shadow so it does not clobber tone-danger\'s own background)', () => {
    expect(keyframe(tokens(), 'feedItemIn')).not.toMatch(/background/)
  })
})

describe('skins CSS contract — reduced motion (Task 5)', () => {
  const rmBlock = () =>
    tokens().match(/@media[^{]*prefers-reduced-motion[^{]*\{[\s\S]*?\}\s*\}/)?.[0] ?? ''
  it('turns off the pure mount animations under reduced motion', () => {
    const b = rmBlock()
    expect(b).toMatch(/\.feed-reply-chip/)
    expect(b).toMatch(/\.line-comment-quote::before/)
    expect(b).toMatch(/animation:\s*none/)
  })
  it('does NOT disable .feed-item--enter (it needs animationend to self-clear)', () => {
    expect(rmBlock()).not.toMatch(/\.feed-item--enter/)
  })
})
