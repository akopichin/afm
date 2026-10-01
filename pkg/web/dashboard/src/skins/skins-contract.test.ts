import { describe, it, expect } from 'vitest'
// Authoritative source skins, inlined as strings by Vite (?raw). Typed by vite/client.
import tokensCss from '../../skins/base/tokens.css?raw'

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
