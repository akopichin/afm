import { describe, it, expect } from 'vitest'
import runMetricsCss from '../../../skins/base/run-metrics.css?raw'

// extractMediaBlock — returns the body of the first `@media (<query>) { ... }` block, with braces
// properly balanced (a naive non-greedy regex would stop at the first NESTED rule's closing brace
// instead of the media block's own). jsdom does not evaluate @media the way a real browser does,
// so narrow-viewport assertions here read the stylesheet source directly instead — same pattern
// as src/skins/skins-contract.test.ts.
function extractMediaBlock(css: string, query: string): string {
  const start = css.indexOf(`@media (${query})`)
  if (start === -1) return ''
  const openBrace = css.indexOf('{', start)
  if (openBrace === -1) return ''
  let depth = 0
  for (let i = openBrace; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}') {
      depth -= 1
      if (depth === 0) return css.slice(openBrace + 1, i)
    }
  }
  return ''
}

// T7.6: at the app's documented narrow-viewport breakpoint (900px — the same value header.css,
// layout.css and side-agent.css all use for their own narrow-viewport collapse), the five run
// metrics (Started/Elapsed/Idle/Backoff/Est. tokens) must wrap onto more lines instead of
// disappearing behind the "..." popover the way the medium (1279px/1079px) tiers do above it.
describe('run-metrics.css narrow-viewport contract (T7.6)', () => {
  const narrow = extractMediaBlock(runMetricsCss, 'max-width: 900px')

  it('has a narrow-viewport (900px) block', () => {
    expect(narrow).not.toBe('')
  })

  it('wraps the metrics bar instead of hiding metrics', () => {
    expect(narrow).toMatch(/flex-wrap:\s*wrap/)
  })

  it('does not hide the metrics bar or any individual tile at the narrow breakpoint', () => {
    expect(narrow).not.toMatch(/\.run-metrics\s*>\s*\.metric\s*\{[^}]*display:\s*none/)
    expect(narrow).not.toMatch(/data-metric="(idle|backoff|tokens|cost)"[^{]*\{[^}]*display:\s*none/)
  })

  it('does not hide the metric labels at the narrow breakpoint (Started/Elapsed/... stay named)', () => {
    expect(narrow).not.toMatch(/\.metric-label\s*\{[^}]*display:\s*none/)
  })

  it('the medium tiers above 900px still fold secondary metrics behind "..." (unchanged design)', () => {
    const medium = extractMediaBlock(runMetricsCss, 'max-width: 1279px')
    expect(medium).toMatch(/data-metric="idle"/)
    expect(medium).toMatch(/display:\s*none/)
  })
})
