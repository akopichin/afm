import { describe, it, expect } from 'vitest'
// Authoritative source skins, inlined as strings by Vite (?raw). Typed by vite/client.
import tokensCss from '../../skins/base/tokens.css?raw'
import feedWorkspaceCss from '../../skins/base/feed-workspace.css?raw'
import planPanelCss from '../../skins/base/plan-panel.css?raw'
import sideAgentCss from '../../public/side-agent.css?raw'
import assistantsCss from '../../skins/base/assistants.css?raw'
import graphiteIndexCss from '../../skins/graphite/index.css?raw'
import gogaIndexCss from '../../skins/goga/index.css?raw'
import novacorpsIndexCss from '../../skins/novacorps/index.css?raw'
import coffeeIndexCss from '../../skins/coffee/index.css?raw'

const tokens = () => tokensCss
const keyframe = (css: string, name: string) =>
  css.match(new RegExp(`@keyframes\\s+${name}\\s*\\{[\\s\\S]*?\\n\\}`))?.[0] ?? ''
const rule = (css: string, sel: string) =>
  css.match(new RegExp(`${sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{[\\s\\S]*?\\}`))?.[0] ?? ''

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
  it('.feed-reply-chip glows in via quoteChipIn', () => {
    expect(rule(feedWorkspaceCss, '.feed-reply-chip')).toMatch(/animation:\s*quoteChipIn/)
  })
  it('.feed-reply-chip-label springs its mark via quoteMarkPop', () => {
    expect(rule(feedWorkspaceCss, '.feed-reply-chip-label')).toMatch(/animation:\s*quoteMarkPop/)
  })
})

describe('skins CSS contract — side-agent quote chip (Task 5)', () => {
  it('.side-agent-quote-chip glows in via quoteChipIn', () => {
    expect(rule(sideAgentCss, '.side-agent-quote-chip')).toMatch(/animation:\s*quoteChipIn/)
  })
  it('.side-agent-quote-chip-label springs its mark via quoteMarkPop', () => {
    expect(rule(sideAgentCss, '.side-agent-quote-chip-label')).toMatch(/animation:\s*quoteMarkPop/)
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

// assistants.css (T3.9/T3.10's helper-task rail rows + task feed) is a structural, skin-agnostic stylesheet
// — §15.3 requires it to apply independently of whichever skin is active, so EVERY skin must import it
// explicitly. Checked by exact string per skin (no glob) so a forgotten import fails loudly here instead of
// silently rendering an unstyled rail branch under whichever skin's author forgot the line.
describe('skins CSS contract — assistants.css is imported by every skin (T3.10)', () => {
  const IMPORT_LINE = '@import url("../base/assistants.css");'

  it('graphite imports assistants.css', () => expect(graphiteIndexCss).toContain(IMPORT_LINE))
  it('goga imports assistants.css', () => expect(gogaIndexCss).toContain(IMPORT_LINE))
  it('novacorps imports assistants.css', () => expect(novacorpsIndexCss).toContain(IMPORT_LINE))
  it('coffee imports assistants.css', () => expect(coffeeIndexCss).toContain(IMPORT_LINE))
})

describe('skins CSS contract — composer options bar', () => {
  it('.composer-opts pins the single toggle to the right edge', () => {
    const r = rule(feedWorkspaceCss, '.composer-opts')
    expect(r).toMatch(/justify-content:\s*flex-end/)
    // space-between с ОДНИМ ребёнком прижало бы тумблер влево — это и был баг.
    expect(r).not.toMatch(/space-between/)
  })
  it('drops the orphaned-hint rule along with the span it styled', () => {
    expect(feedWorkspaceCss).not.toMatch(/\.composer-opts-hint/)
  })
})

// T11.1 — visual theme matrix (§15.4): every helper-agents/collaboration UI surface built across Stages
// 0-10 lives in this ONE structural stylesheet (assistants.css's own header comment: "must render sanely
// under a skin_dir override that has never heard of this feature"). "Один удачный Graphite-скриншот
// завершением тематизации не является" — a screenshot can't be taken in this environment (no browser
// tooling here), so these tests are the actual mechanical proof: every custom property this file touches
// really is part of the shared semantic layer every skin (including a brand-new one, or a user's own
// skin_dir that predates this feature and knows nothing about it) already provides, not a color literal or
// an undefined/skin-specific name that only happens to resolve correctly under the 3 shipped skins today.
describe('skins CSS contract — helper-agents UI surfaces lean ONLY on the semantic token layer (T11.1)', () => {
  // allVarNames extracts every custom-property NAME referenced via var(--x...) in css (dedup by Set).
  function allVarNames(css: string): Set<string> {
    return new Set([...css.matchAll(/var\((--[a-zA-Z0-9-]+)/g)].map((m) => m[1] as string))
  }
  // declaredVarNames extracts every custom-property NAME declared (left-hand side) anywhere in css.
  function declaredVarNames(css: string): Set<string> {
    return new Set([...css.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)].map((m) => m[1] as string))
  }

  // --assistant-depth is the ONE documented exception: a per-row value set inline from React
  // (AssistantTaskRow.tsx), not a global design token any skin is expected to declare — it always carries
  // its own `, 1` fallback in assistants.css, which is exactly how a locally-scoped custom property (as
  // opposed to a global token) is supposed to be used.
  const LOCAL_INLINE_VARS = new Set(['--assistant-depth'])

  it('every var(--x) assistants.css references is declared in tokens.css\'s shared semantic layer', () => {
    const declared = declaredVarNames(tokensCss)
    const used = allVarNames(assistantsCss)
    const undeclared = [...used].filter((name) => !declared.has(name) && !LOCAL_INLINE_VARS.has(name))
    // A regression here means assistants.css grew a dependency on something only the 3 shipped skins'
    // index.css files happen to define (or, worse, a plain typo like the real --surface-base bug this test
    // was written to catch) — invisible on Graphite/Goga/Nova Corps today, but broken on day one for any
    // skin_dir that only promises tokens.css's own documented contract.
    expect(undeclared, `assistants.css references undeclared custom properties: ${undeclared.join(', ')}`).toEqual([])
  })

  it('assistants.css never hardcodes a color literal (hex/rgb/hsl) outside of its token references', () => {
    // Strip comments first so documentation examples never produce a false positive.
    const withoutComments = assistantsCss.replace(/\/\*[\s\S]*?\*\//g, '')
    const hexLiterals = withoutComments.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []
    const rawColorFns = withoutComments.match(/\b(?:rgb|rgba|hsl|hsla)\(\s*[0-9]/g) ?? []
    expect(hexLiterals, `hardcoded hex colors found: ${hexLiterals.join(', ')}`).toEqual([])
    expect(rawColorFns, `hardcoded rgb()/hsl() colors found: ${rawColorFns.join(', ')}`).toEqual([])
  })

  // Confidence check (not a browser smoke test — none is available in this environment, see dev-plan.md's
  // Review decisions for this known limitation): every UI surface §15.4/T11.1 names has SOME rule in this
  // stylesheet, so "renders sanely under an unaware skin" is actually exercised by the two tests above for
  // all of them, not just whichever one a manual screenshot happened to cover.
  it.each([
    ['helper-task rail branch toggle', '.assistant-branch-toggle-wrap'],
    ['helper-task rail row (nested delegation uses the SAME row + --assistant-depth, no per-depth variant)', '.assistant-task-item'],
    ['selected child row', '.assistant-task-item.active'],
    ['delegation breadcrumb (depth up to effective_max_depth)', '.assistant-breadcrumb-depth'],
    ['discussion', '.stage-discussion'],
    ['proposal card', '.proposal-card'],
    ['proposal card — stale shows refresh, not accept', '.proposal-card-refresh'],
    ['required task pill (active/required state)', '.assistant-task-mode[data-mode="required"]'],
    ['failed/cancelled task dot', '.assistant-task-status-dot[data-status="failed"]'],
    ['paused task dot', '.assistant-task-status-dot[data-status="paused"]'],
    ['@-mention menu', '.assistant-mention-menu'],
    ['@-mention menu — highlighted row', '.assistant-mention-item.active'],
    ['Side agent action card', '.action-card'],
    ['Side agent action card — depth-limit warning', '.action-card-note-warning'],
    ['add-stage preview', '.add-stage-dialog-preview'],
    ['add-stage dialog — feature-disabled note', '.add-stage-dialog-disabled-note'],
    ['empty state (no task selected)', '.assistant-task-view-empty'],
  ])('%s (%s) has a real rule in assistants.css', (_label, selector) => {
    expect(assistantsCss.includes(selector), `expected a rule for ${selector}`).toBe(true)
  })
})
