import { describe, it, expect } from 'vitest'
// Authoritative source skins, inlined as strings by Vite (?raw). Typed by vite/client.
import tokensCss from '../../skins/base/tokens.css?raw'
import feedWorkspaceCss from '../../skins/base/feed-workspace.css?raw'
import planPanelCss from '../../skins/base/plan-panel.css?raw'
import fileBrowserCss from '../../skins/base/file-browser.css?raw'
import sideAgentCss from '../../public/side-agent.css?raw'
import assistantsCss from '../../skins/base/assistants.css?raw'
import meshCss from '../../skins/base/mesh.css?raw'
import layoutCss from '../../skins/base/layout.css?raw'
import chatComposerCss from '../../public/chat-composer.css?raw'
import dialogChannelCss from '../../skins/base/dialog-channel.css?raw'
import headerCss from '../../skins/base/header.css?raw'
import stagesListCss from '../../skins/base/stages-list.css?raw'
import resetCss from '../../skins/base/reset.css?raw'
import runMetricsCss from '../../skins/base/run-metrics.css?raw'
import reviewBannerCss from '../../skins/base/review-banner.css?raw'
import reviewNotesModalCss from '../../skins/base/review-notes-modal.css?raw'
import costPanelCss from '../../skins/base/cost-panel.css?raw'
import commentsCss from '../../skins/base/comments.css?raw'
import markdownCss from '../../skins/base/markdown.css?raw'
import pasteableTextareaCss from '../../skins/base/pasteable-textarea.css?raw'
import stageNoteCss from '../../skins/base/stage-note.css?raw'
import graphiteIndexCss from '../../skins/graphite/index.css?raw'
import gogaIndexCss from '../../skins/goga/index.css?raw'
import novacorpsIndexCss from '../../skins/novacorps/index.css?raw'
import coffeeIndexCss from '../../skins/coffee/index.css?raw'
import { MEASURED_TEXT_TOKENS } from './palette'

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
  it('quoteChipIn takes its glow from the active accent (no -rgb triplet)', () => {
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
})

describe('skins CSS contract — table separator row', () => {
  it('draws no comment icon, so it cannot share space with the neighbour row icons', () => {
    expect(planPanelCss).toMatch(/tr\.md-table-rule > td::after\s*\{\s*display:\s*none/)
  })
})

describe('skins CSS contract — on-accent text contrast', () => {
  // Every rule that paints text on the accent fill: it must use the canvas token (readable in all skins), never white.
  const onAccentBlocks = Array.from(planPanelCss.matchAll(/([^{}]*(?:\.btn-send|#btn-approve|#btn-revise)[^{}]*)\{([^}]*)\}/g))
    .map((m) => ({ selector: (m[1] ?? '').trim(), body: m[2] ?? '' }))
    .filter((b) => /background:\s*var\(--accent-primary(-hover)?\)/.test(b.body))

  it('finds the primary action rules', () => {
    expect(onAccentBlocks.length).toBeGreaterThanOrEqual(4)
  })
  it('uses var(--surface-canvas), not hard-coded white', () => {
    for (const { selector, body } of onAccentBlocks) {
      expect(body, selector).toMatch(/\bcolor:\s*var\(--surface-canvas\)/)
      expect(body, selector).not.toMatch(/#fff/i)
    }
  })
})

describe('skins CSS contract — comment editor on a phone', () => {
  const phone = planPanelCss.slice(planPanelCss.indexOf('@media (max-width: 600px)', planPanelCss.indexOf('.comment-help')))
  it('plan editor drops the gutter indent and keeps the action row unwrapped', () => {
    expect(phone).toMatch(/\.line-comment-form\s*\{\s*margin-left:\s*0/)
    expect(phone).toMatch(/\.comment-actions-hint\s*\{\s*flex:\s*1 1 100%/)
    expect(phone).toMatch(/\.comment-actions\s*\{[^}]*flex-wrap:\s*nowrap/)
    expect(phone).toMatch(/\.comment-actions \.btn\s*\{\s*white-space:\s*nowrap/)
  })
  it('Files editor and card span the width instead of the 52px gutter indent', () => {
    expect(fileBrowserCss).toMatch(
      /@media \(max-width: 600px\)\s*\{\s*\.file-viewer-source > \.comment-card,\s*\.file-viewer-source > \.comment-editor\s*\{\s*margin:\s*8px 10px 12px/,
    )
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
  it('feedItemIn glows in via an accent box-shadow (no -rgb triplet)', () => {
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

describe('skins CSS contract — tool-call count bump', () => {
  it('pops the number with transform and flashes it with the accent text colour, nothing else', () => {
    const b = keyframe(feedWorkspaceCss, 'feedToolCountBump')
    expect(b).toMatch(/transform:\s*scale\(1\.18\)/)
    expect(b).toMatch(/color:\s*var\(--accent-text\)/)
    const properties = [...b.matchAll(/([a-z-]+)\s*:/g)].map((m) => m[1])
    expect(new Set(properties)).toEqual(new Set(['transform', 'color']))
  })
  it('plays at --motion-slow and keeps the layout still', () => {
    expect(rule(feedWorkspaceCss, '.feed-tool-count--bump')).toMatch(/animation:\s*feedToolCountBump\s+var\(--motion-slow\)/)
    expect(rule(feedWorkspaceCss, '.feed-tool-count')).toMatch(/display:\s*inline-block/)
  })
  it('is off under reduced motion', () => {
    expect(feedWorkspaceCss).toMatch(/@media \(prefers-reduced-motion: reduce\)\s*\{[^@]*\.feed-tool-count--bump\s*\{[^}]*animation:\s*none/)
  })
})

describe('skins CSS contract — reduced motion (Task 5)', () => {
  const rmBlock = () =>
    tokens().match(/@media[^{]*prefers-reduced-motion[^{]*\{[\s\S]*?\}\s*\}/)?.[0] ?? ''
  it('turns off the pure mount animations under reduced motion', () => {
    const b = rmBlock()
    expect(b).toMatch(/\.feed-reply-chip/)
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

// cost-panel.css styles the Cost tab, which is always present, so every skin imports it explicitly.
describe('skins CSS contract — cost-panel.css is imported by every skin', () => {
  const IMPORT_LINE = '@import url("../base/cost-panel.css");'

  it('graphite imports cost-panel.css', () => expect(graphiteIndexCss).toContain(IMPORT_LINE))
  it('goga imports cost-panel.css', () => expect(gogaIndexCss).toContain(IMPORT_LINE))
  it('novacorps imports cost-panel.css', () => expect(novacorpsIndexCss).toContain(IMPORT_LINE))
  it('coffee imports cost-panel.css', () => expect(coffeeIndexCss).toContain(IMPORT_LINE))
})

// stage-note.css styles the Stage note block above the feed: structural and skin-agnostic like assistants.css, so
// every skin imports it explicitly.
describe('skins CSS contract — stage-note.css is imported by every skin', () => {
  const IMPORT_LINE = '@import url("../base/stage-note.css");'

  it('graphite imports stage-note.css', () => expect(graphiteIndexCss).toContain(IMPORT_LINE))
  it('goga imports stage-note.css', () => expect(gogaIndexCss).toContain(IMPORT_LINE))
  it('novacorps imports stage-note.css', () => expect(novacorpsIndexCss).toContain(IMPORT_LINE))
  it('coffee imports stage-note.css', () => expect(coffeeIndexCss).toContain(IMPORT_LINE))

  it('bounds the block height with its own scroll and gives the editor room on a narrow screen', () => {
    expect(rule(stageNoteCss, '.stage-note-wrap')).toMatch(/max-height:\s*48%;[\s\S]*overflow-y:\s*auto/)
    const narrow = stageNoteCss.slice(stageNoteCss.indexOf('@media (max-width: 600px)'))
    expect(narrow).toMatch(/\.stage-note-wrap \{[^}]*max-height:\s*55%/)
  })

  it('takes every colour from semantic tokens', () => {
    expect(stageNoteCss).not.toMatch(/#[0-9a-fA-F]{3,8}\b|rgba?\(/)
  })
})

// mesh.css (Agent Mesh Phase 7, T7.3's Team view) is a structural, skin-agnostic stylesheet, same posture as
// assistants.css above — every skin must import it explicitly, checked by exact string per skin.
describe('skins CSS contract — mesh.css is imported by every skin (T7.3)', () => {
  const IMPORT_LINE = '@import url("../base/mesh.css");'

  it('graphite imports mesh.css', () => expect(graphiteIndexCss).toContain(IMPORT_LINE))
  it('goga imports mesh.css', () => expect(gogaIndexCss).toContain(IMPORT_LINE))
  it('novacorps imports mesh.css', () => expect(novacorpsIndexCss).toContain(IMPORT_LINE))
  it('coffee imports mesh.css', () => expect(coffeeIndexCss).toContain(IMPORT_LINE))
})

describe('skins CSS contract — shared message time', () => {
  it('uses one grid cell for both labels and pads the target without changing row width', () => {
    expect(rule(meshCss, '.message-time')).toMatch(/display:\s*inline-grid/)
    expect(rule(meshCss, '.message-time')).toMatch(/padding:\s*8px 10px/)
    expect(rule(meshCss, '.message-time')).toMatch(/margin:\s*-8px -10px/)
    expect(rule(meshCss, '.message-time')).toMatch(/font-variant-numeric:\s*tabular-nums/)
    expect(rule(meshCss, '.message-time > span')).toMatch(/grid-area:\s*1 \/ 1/)
  })

  it('reveals the gap on hover, keyboard focus, and focus within interactive feed rows', () => {
    expect(meshCss).toMatch(/\.message-time:hover \.message-time-clock/)
    expect(meshCss).toMatch(/\.message-time:focus-visible \.message-time-gap/)
    expect(meshCss).toMatch(/\.feed-item-navigable:focus-visible \.message-time \.message-time-gap/)
    expect(meshCss).toMatch(/\.feed-thought-row:focus-within \.message-time \.message-time-gap/)
  })

  it('switches without animation under reduced motion', () => {
    expect(meshCss).toMatch(/@media \(prefers-reduced-motion: reduce\)\s*\{[^}]*\.message-time > span\s*\{[^}]*transition:\s*none/)
  })

  it('keeps the reply button clear of the thought timestamp hover target', () => {
    const thought = rule(feedWorkspaceCss, '.feed-item-thought')
    const reply = rule(feedWorkspaceCss, '.feed-thought-reply')
    const time = rule(meshCss, '.message-time')
    const reserved = Number(thought.match(/padding-right:\s*(\d+)px/)?.[1] ?? 0)
    const buttonRight = Number(reply.match(/right:\s*(\d+)px/)?.[1] ?? 0)
    const buttonWidth = Number(reply.match(/width:\s*(\d+)px/)?.[1] ?? 0)
    const timeHoverExtension = Number(time.match(/margin:\s*-\d+px\s+-(\d+)px/)?.[1] ?? 0)
    expect(reserved).toBeGreaterThanOrEqual(buttonRight + buttonWidth + timeHoverExtension)
  })
})

describe('skins CSS contract — Team view leans ONLY on the semantic token layer (T7.3)', () => {
  function allVarNames(css: string): Set<string> {
    return new Set([...css.matchAll(/var\((--[a-zA-Z0-9-]+)/g)].map((m) => m[1] as string))
  }
  function declaredVarNames(css: string): Set<string> {
    return new Set([...css.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)].map((m) => m[1] as string))
  }

  it('every var(--x) mesh.css references is declared in tokens.css\'s shared semantic layer (or locally in mesh.css)', () => {
    // A custom property mesh.css declares itself (e.g. the avatar's --agent-color) needs nothing from a skin.
    const declared = new Set([...declaredVarNames(tokensCss), ...declaredVarNames(meshCss)])
    const used = allVarNames(meshCss)
    const undeclared = [...used].filter((name) => !declared.has(name))
    expect(undeclared, `mesh.css references undeclared custom properties: ${undeclared.join(', ')}`).toEqual([])
  })

  it('mesh.css never hardcodes a color literal (hex/rgb/hsl) outside of its token references', () => {
    const withoutComments = meshCss.replace(/\/\*[\s\S]*?\*\//g, '')
    const hexLiterals = withoutComments.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []
    const rawColorFns = withoutComments.match(/\b(?:rgb|rgba|hsl|hsla)\(\s*[0-9]/g) ?? []
    expect(hexLiterals, `hardcoded hex colors found: ${hexLiterals.join(', ')}`).toEqual([])
    expect(rawColorFns, `hardcoded rgb()/hsl() colors found: ${rawColorFns.join(', ')}`).toEqual([])
  })

  it.each([
    ['roster', '.team-roster'],
    ['person row', '.team-person-row'],
    ['selected person/thread row', '.team-person-row.active'],
    ['conversations list', '.team-thread-list'],
    ['tabs (Messages/Board/Activity)', '.team-tabs'],
    ['board entry', '.team-board-entry'],
    ['create-conversation form', '.team-create-conversation-form'],
    ['Add run agent — read-only Configured executor', '.add-run-agent-executor'],
  ])('%s (%s) has a real rule in mesh.css', (_label, selector) => {
    expect(meshCss.includes(selector), `expected a rule for ${selector}`).toBe(true)
  })
})

describe('skins CSS contract — shared chat composer (feed, Team, side agent)', () => {
  it('the composer row is the positioning anchor for the @-mention menu', () => {
    expect(rule(chatComposerCss, '.chat-composer-row')).toMatch(/position:\s*relative/)
  })
  it('the @-mention menu floats above the input instead of joining the flex row (in the skin-proof stylesheet)', () => {
    const r = rule(chatComposerCss, '.assistant-mention-menu')
    expect(r).toMatch(/position:\s*fixed/)
    expect(r).toMatch(/z-index:\s*200/)
    expect(r).toMatch(/max-height:\s*380px/)
    expect(r, 'the width has one source of truth: the inline style from AssistantMentionMenu').not.toMatch(/(^|[\s;{])width:/)
  })
  it('recipient chip variants carry the card prefix so they outrank the base chip rule', () => {
    expect(rule(chatComposerCss, '.chat-composer-card .chat-composer-recipient.is-default')).toMatch(/border:\s*1px dashed/)
    expect(rule(chatComposerCss, '.chat-composer-card .chat-composer-recipient.has-problem')).toMatch(/color:\s*var\(--danger-text\)/)
    expect(chatComposerCss).not.toMatch(/(^|\n)\.chat-composer-recipient\.(is-default|has-problem)/)
  })
  it('chip look lives in chat-composer.css only: feed-workspace.css keeps no .mesh-recipient-chip or .mesh-recipients container rule', () => {
    expect(feedWorkspaceCss).not.toMatch(/\.mesh-recipient-chip|\.mesh-recipients\s*\{/)
  })
  it('stage agent messages sit under an author row with no accent stripe or frame', () => {
    expect(rule(feedWorkspaceCss, '.feed-authored')).toMatch(/display:\s*grid/)
    expect(rule(feedWorkspaceCss, '.feed-author-row .message-time')).toMatch(/margin-left:\s*auto/)
    expect(rule(feedWorkspaceCss, '.feed-group[data-actor="agent"] .feed-item.feed-kind-message')).not.toMatch(/border-left/)
    expect(rule(feedWorkspaceCss, '.feed-rows')).not.toMatch(/border|background/)
  })
  it('the send button is a round icon button', () => {
    const r = rule(chatComposerCss, '.chat-send,\n.chat-stop')
    expect(r).toMatch(/border-radius:\s*50%/)
    expect(r).toMatch(/width:\s*38px/)
  })
  it('chat-composer.css only uses declared semantic tokens and no color literals (it ships under any skin)', () => {
    const declared = new Set([...tokensCss.matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1]))
    const used = [...chatComposerCss.matchAll(/var\((--[a-z0-9-]+)/g)].map((m) => m[1])
    expect(used.filter((name) => !declared.has(name))).toEqual([])
    const withoutComments = chatComposerCss.replace(/\/\*[\s\S]*?\*\//g, '')
    expect(withoutComments.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []).toEqual([])
    expect(withoutComments.match(/\b(?:rgb|hsl)a?\(\s*[0-9]/g) ?? []).toEqual([])
  })
})

describe('skins CSS contract — Team on narrow screens', () => {
  it('gives the measured narrow Team panel one full-width slot for list and detail', () => {
    expect(rule(meshCss, '.team-view.drawer')).toMatch(/grid-template-columns:\s*minmax\(0, 1fr\)/)
    expect(meshCss).toMatch(/\.team-view\.drawer\s+\.team-roster:not\(\.team-roster-embedded\)\s*\{[^}]*width:\s*100%/)
    expect(meshCss).toMatch(/\.team-view\.drawer\.mobile-detail\s+\.team-roster:not\(\.team-roster-embedded\),[\s\S]*?\.team-view\.drawer\s+\.team-roster-resizer\s*\{\s*display:\s*none/)
  })

  it('keeps the narrow header and Back control visible while hiding inactive detail', () => {
    expect(rule(meshCss, '.team-main')).toMatch(/display:\s*contents/)
    expect(meshCss).toMatch(/\.team-view\.drawer:not\(\.mobile-detail\)\s+\.team-main\s*>\s*:not\(\.team-header\)/)
    expect(rule(meshCss, '.team-back-button')).toMatch(/color:\s*var\(--text-secondary\)/)
  })

  it('lays Team out as one grid: the heading spans the top row, the resizer stays in the roster cell', () => {
    expect(rule(meshCss, '.team-view')).toMatch(/display:\s*grid/)
    expect(rule(meshCss, '.team-header')).toMatch(/grid-column:\s*1 \/ -1/)
    expect(rule(meshCss, '.team-roster-resizer')).toMatch(/grid-row:\s*2/)
  })

  it('draws the Conversations | People switch as underline tabs, not a segmented pill', () => {
    expect(rule(meshCss, '.team-list-tab.active')).toMatch(/border-bottom-color:\s*var\(--accent-primary\)/)
    expect(rule(meshCss, '.team-list-tabs')).not.toMatch(/border-radius|background/)
  })

  it('leaves the stage Feed People drawer separate from Team layout rules', () => {
    expect(meshCss).toMatch(/\.team-view\.drawer\s+\.team-roster:not\(\.team-roster-embedded\)/)
    expect(meshCss).toMatch(/\.team-roster\.team-roster-embedded\s*\{[^}]*width:\s*auto/)
  })
})

describe('skins CSS contract — standalone Board lanes', () => {
  it('fits three status lanes within the available workspace width', () => {
    const lanes = rule(meshCss, '.board-lanes')
    expect(lanes).toMatch(/display:\s*grid/)
    expect(lanes).toMatch(/grid-template-columns:\s*repeat\(3, minmax\(0, 1fr\)\)/)
    expect(lanes).toMatch(/overflow-x:\s*hidden/)
  })

  it('stacks the lanes into one column on a narrow viewport', () => {
    expect(meshCss).toMatch(/@media \(max-width: 760px\)\s*\{[^@]*\.board-lanes\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\)/)
  })

  it('lets lane cards shrink instead of widening the lane', () => {
    expect(rule(meshCss, '.board-lane')).toMatch(/min-width:\s*0/)
  })

  it.each(['todo', 'in_progress', 'done'])('colors the %s lane from the shared status tokens', (status) => {
    expect(meshCss).toMatch(new RegExp(`\\.board-lane\\[data-status="${status}"\\]`))
  })

  it('shows keyboard focus on the search and assignee controls', () => {
    expect(meshCss).toMatch(/\.board-view-search:focus-visible/)
    expect(meshCss).toMatch(/\.board-view-assignee:focus-visible/)
  })

  it('keeps the announcement region out of sight but readable by assistive tech', () => {
    const announce = rule(meshCss, '.board-view-announce')
    expect(announce).toMatch(/position:\s*absolute/)
    expect(announce).toMatch(/clip:\s*rect\(0, 0, 0, 0\)/)
  })
})

describe('skins CSS contract — stage people panel', () => {
  it('is a right drawer opened by the people toggle when the feed and the panel do not fit side by side', () => {
    const narrow = meshCss.slice(meshCss.indexOf('@media (max-width: 1180px)'))
    expect(narrow).toMatch(/\.stage-people \{[^}]*position:\s*fixed[^}]*right:\s*0/)
    expect(narrow).toMatch(/\.stage-people\.open \{[^}]*visibility:\s*visible/)
    expect(rule(meshCss, '.stage-people-scrim')).toMatch(/display:\s*none/)
    expect(narrow).toMatch(/\.stage-people-scrim \{[^}]*display:\s*block/)
  })

  it('leaves the people toggle visible at every width: only layout.css styles it', () => {
    expect(meshCss).not.toMatch(/\.stage-people-toggle/)
    expect(rule(layoutCss, '.workspace-header .stage-people-toggle')).toMatch(/display:\s*inline-flex/)
  })

  it('on a phone puts the stage buttons under the title without a divider and keeps the actions on their row', () => {
    const phone = layoutCss.slice(layoutCss.indexOf('@media (max-width: 600px)', layoutCss.indexOf('.workspace-header-actions')))
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\)\s*\{[^}]*display:\s*grid/)
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\) \.workspace-header-actions\s*\{[^}]*grid-row:\s*2/)
    expect(phone).toMatch(/\n  \.stage-buttons\s*\{[^}]*grid-row:\s*2[^}]*border-inline-start:\s*0/)
  })

  it('on a phone gives the title of a header with stage buttons its natural width, not a percentage of its own grid column', () => {
    const phone = layoutCss.slice(layoutCss.indexOf('@media (max-width: 600px)', layoutCss.indexOf('.workspace-header-actions')))
    // A percentage max-width resolves against the auto column the title sits in and squeezes it to "Build …".
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\) \.workspace-title\s*\{[^}]*max-width:\s*none/)
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\)\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*max-content\)\s+auto\s+minmax\(0,\s*1fr\)\s+auto/)
    // Buttons span title, status and the free column; the right-hand group stays in the last column.
    expect(phone).toMatch(/\n  \.stage-buttons\s*\{[^}]*grid-column:\s*1 \/ 4/)
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\) \.attention-nav\s*\{[^}]*grid-column:\s*4/)
    expect(phone).toMatch(/\.workspace-header:has\(\.stage-buttons\) \.workspace-header-actions\s*\{[^}]*grid-column:\s*4/)
  })

  it('hides a closed panel only on a wide screen, as the exact complement of the drawer query', () => {
    const wideBlock = meshCss.match(/@media not all and \(max-width: 1180px\)\s*\{[^@]*?\n\}/)?.[0] ?? ''
    expect(wideBlock).toMatch(/\.stage-people:not\(\.open\)\s*\{[^}]*display:\s*none/)
    expect(rule(meshCss, '.stage-people')).not.toMatch(/display:\s*none|visibility:\s*hidden/)
    // The drawer hides itself by sliding out, never with display: none.
    const narrow = meshCss.slice(meshCss.indexOf('@media (max-width: 1180px)'), meshCss.indexOf('@media (prefers-reduced-motion', meshCss.indexOf('@media (max-width: 1180px)')))
    expect(narrow).not.toMatch(/\.stage-people:not\(\.open\)/)
  })

  it('shows the opening drawer at once so it can take the focus, and delays hiding it until it has slid out', () => {
    const narrow = meshCss.slice(meshCss.indexOf('@media (max-width: 1180px)'))
    // A visibility transition keeps `hidden` for its whole first frame, and focus() on a hidden element is a no-op.
    expect(rule(narrow, '.stage-people.open')).toMatch(/transition:[^;]*visibility\s+0s/)
    expect(rule(narrow, '.stage-people')).toMatch(/transition:[^;]*visibility\s+var\(--motion-fast, 120ms\)/)
  })

  it('has no drawer slide under reduced motion, open or closed', () => {
    const reduced = meshCss.slice(meshCss.indexOf('@media (prefers-reduced-motion', meshCss.indexOf('@media (max-width: 1180px)')))
    expect(reduced).toMatch(/^[^{]*\{\s*\.stage-people,\s*\.stage-people\.open\s*\{\s*transition:\s*none/)
  })

  it('tints the people toggle with the theme accent while the panel is shown, also on hover', () => {
    expect(rule(layoutCss, ".workspace-header .stage-people-toggle[aria-expanded='true'],\n.workspace-header .stage-people-toggle[aria-expanded='true']:hover"))
      .toMatch(/color:\s*var\(--accent-text\)/)
  })

  it('uses a preferred desktop width but keeps the existing 1180px drawer fixed and hides its separator', () => {
    expect(rule(meshCss, '.stage-people')).toMatch(/(?:width|flex-basis|flex):[^;]*var\(--people-width\)/)
    const narrow = meshCss.slice(meshCss.indexOf('@media (max-width: 1180px)'))
    expect(narrow).toMatch(/\.stage-people \{[^}]*width:\s*min\(320px, 88vw\)/)
    expect(narrow).toMatch(/\.stage-people-resizer\s*\{[^}]*display:\s*none/)
  })
})

describe('skins CSS contract — People and Run team separators', () => {
  it('uses the measured desktop roster width and keeps the conversation shrinkable', () => {
    expect(rule(meshCss, '.team-roster')).toMatch(/(?:width|flex-basis|flex):[^;]*var\(--team-roster-width\)/)
    expect(rule(meshCss, '.team-view')).toMatch(/grid-template-columns:\s*var\(--team-roster-width\) minmax\(0, 1fr\)/)
  })

  it('gives both separators the Stages hit area and centered grip with keyboard focus feedback', () => {
    // The shared rules may live in layout.css alongside the Stages divider or in mesh.css.
    const css = `${layoutCss}\n${meshCss}`
    expect(rule(css, '.panel-resizer')).toMatch(/position:\s*absolute/)
    expect(rule(css, '.panel-resizer')).toMatch(/width:\s*11px/)
    expect(rule(css, '.panel-resizer')).toMatch(/cursor:\s*col-resize/)
    expect(rule(css, '.panel-resizer-grip')).toMatch(/width:\s*4px/)
    expect(rule(css, '.panel-resizer-grip')).toMatch(/height:\s*40px/)
    expect(rule(css, '.panel-resizer:focus-visible')).toMatch(/outline:\s*none/)
    expect(css).toMatch(/\.panel-resizer:focus-visible \.panel-resizer-grip\s*\{[^}]*box-shadow:[^;]*var\(--focus-ring\)/)
    expect(css).toMatch(/\.panel-resizer:hover \.panel-resizer-grip,[\s\S]*?\.panel-resizer:focus-visible \.panel-resizer-grip\s*\{[^}]*height:\s*60px/)
    expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)\s*\{[^}]*\.panel-resizer-grip\s*\{[^}]*transition:\s*none/)
  })
})

describe('skins CSS contract — global header', () => {
  it('a long flow description shrinks (ellipsis) instead of squeezing the run metrics into each other', () => {
    const r = rule(headerCss, '.global-header')
    expect(r).toMatch(/grid-template-columns:\s*minmax\(0, max-content\) minmax\(max-content, 1fr\) auto/)
  })
  it('on narrow screens the metrics get their own full-width row instead of overlapping the brand and actions', () => {
    const narrow = headerCss.slice(headerCss.indexOf('@media (max-width: 900px)'))
    expect(narrow).toMatch(/\.global-header \{[^}]*grid-template-columns:\s*minmax\(0, 1fr\) auto/)
    expect(narrow).toMatch(/\.gh-center \{[^}]*grid-column:\s*1 \/ -1;[^}]*grid-row:\s*2/)
  })
  it('on a phone the header compacts: wordmark dropped, tight padding, 44px icon buttons', () => {
    const phone = headerCss.slice(headerCss.indexOf('@media (max-width: 600px)'))
    expect(phone).toMatch(/\.global-header \{[^}]*min-height:\s*0;[^}]*padding:\s*4px 12px/)
    expect(phone).toMatch(/\.gh-wordmark \{ display: none; \}/)
    expect(phone).toMatch(/\.icon-btn\.gh-files-btn,[^{]*\{[^}]*width:\s*44px;[^}]*height:\s*44px/)
  })
  it('on a phone Assistant is icon-only too, and out-specifies the later side-agent.css width rule', () => {
    const phone = headerCss.slice(headerCss.indexOf('@media (max-width: 600px)'))
    expect(phone).toMatch(/\.gh-side-agent-label \{ display: none; \}/)
    expect(phone).toMatch(/\.global-header \.header-actions \.icon-btn\.gh-side-agent-btn \{[^}]*width:\s*44px;[^}]*height:\s*44px;[^}]*padding:\s*0/)
  })
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

describe('skins CSS contract — dense shell and stage rail', () => {
  it('lets the workspace column shrink so a wide child never adds a horizontal scrollbar', () => {
    expect(rule(layoutCss, '.dashboard-body')).toMatch(/grid-template-columns:\s*var\(--rail-width, 300px\) minmax\(0, 1fr\)/)
    const narrow = layoutCss.slice(layoutCss.indexOf('@media (max-width: 900px)'))
    expect(narrow).toMatch(/\.dashboard-body \{ grid-template-columns:\s*minmax\(0, 1fr\)/)
  })

  it('shows keyboard focus on the Stages divider as a ring on its short handle, not a full-height outline', () => {
    expect(layoutCss).toMatch(/\.rail-resizer:focus-visible \{ outline: none; \}/)
    expect(layoutCss).toMatch(/\.rail-resizer:focus-visible \.rail-resizer-grip\s*\{[^}]*box-shadow:[^;]*var\(--focus-ring\)/)
    expect(rule(layoutCss, '.rail-resizer-grip')).toMatch(/height:\s*40px/)
  })

  it('has no tab rule left for the removed Full feed tab', () => {
    expect(layoutCss).not.toContain('full-feed')
  })

  it('sizes a stage row around a 26px node with 15px padding above and below', () => {
    expect(rule(stagesListCss, '.status-dot')).toMatch(/width:\s*26px;\s*height:\s*26px/)
    expect(rule(stagesListCss, '.stage-row')).toMatch(/padding:\s*15px 4px 15px 10px/)
    expect(rule(stagesListCss, '.stage-label')).toMatch(/min-height:\s*26px/)
  })

  it('draws the dialog answer field border from --control-border in every skin', () => {
    expect(rule(dialogChannelCss, '.dialog-pending .dialog-custom')).toMatch(/border:\s*1px solid var\(--control-border\)/)
    // Любое правило (в т.ч. список селекторов или псевдокласс) с .dialog-custom и border* перебивает токен.
    const dialogCustomRules = (css: string) =>
      Array.from(css.matchAll(/([^{}]*\.dialog-custom[^{}]*)\{([^}]*)\}/g), (m) => ({ selector: m[1] ?? '', body: m[2] ?? '' }))
        .filter((r) => /\bborder(?:-[a-z-]+)?\s*:/.test(r.body))
    for (const skinCss of [graphiteIndexCss, gogaIndexCss, novacorpsIndexCss, coffeeIndexCss]) {
      expect(dialogCustomRules(skinCss)).toEqual([])
    }
    // Состояние :focus в базе вправе менять рамку; остальные правила рамки обязаны брать токен.
    const baseRules = dialogCustomRules(dialogChannelCss).filter((r) => !r.selector.includes(':focus'))
    expect(baseRules.length).toBeGreaterThan(0)
    expect(baseRules.every((r) => r.body.includes('var(--control-border)'))).toBe(true)
  })

  it('fades only the connection dot while offline, so the Offline text keeps its contrast', () => {
    expect(headerCss).toMatch(/\.gh-connection\.disconnected \.gh-connection-dot \{ animation: ghConnReconnect/)
    expect(headerCss).not.toMatch(/\.gh-connection\.disconnected \{ animation/)
  })
})

it.each([
  ['Feed', feedWorkspaceCss, '.feed-workspace'],
  ['Team and People', meshCss, '.team-view'],
  ['Composer', chatComposerCss, '.chat-send'],
])('%s disables motion inside a reduced-motion media block', (_name, css, selector) => {
  const blocks = css!.match(/@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{[\s\S]*?\n\}/g) ?? []
  const block = blocks.find((entry) => entry.includes(selector!)) ?? ''
  expect(block).toContain(selector)
  expect(block).toMatch(/transition:\s*none/)
  if (selector !== '.chat-send') {
    expect(block).toMatch(/animation:\s*none/)
    expect(block).toMatch(/scroll-behavior:\s*auto/)
  }
  if (selector === '.team-view') expect(block).toContain('.stage-people')
})

describe('skins CSS contract — global typography', () => {
  it('sets the page font to the UI sans token and lets form controls inherit it', () => {
    const page = rule(resetCss, 'html, body')
    expect(page).toMatch(/font-family:\s*var\(--font-ui\)/)
    expect(page).toMatch(/font-size:\s*14px/)
    expect(resetCss).toMatch(/button, input, textarea, select \{ font: inherit; \}/)
  })

  // Mono is reserved for ids, numbers and code; every other selector in these files reads as UI text.
  const MONO_ALLOWED = new Set([
    '.rail-foot',
    '.workspace-tab-count',
    '.quota-badge',
    '.stage-cost',
    '.stage-time',
    '.dot-num',
    '.metric-value',
    '.cost-table .cost-col-tokens,\n.cost-table .cost-col-cost',
    '.cost-figure dd',
    '.cost-time-summary dd',
    '.cost-table .cost-col-time',
    '.cost-time-title > span',
    '.cost-time-legend dd',
    '.cost-time-waiting dd',
    '.cost-usage-row dd',
  ])
  const uiFiles: Array<[string, string]> = [
    ['reset', resetCss],
    ['header', headerCss],
    ['layout', layoutCss],
    ['stages-list', stagesListCss],
    ['run-metrics', runMetricsCss],
    ['review-banner', reviewBannerCss],
    ['review-notes-modal', reviewNotesModalCss],
    ['cost-panel', costPanelCss],
    ['comments', commentsCss],
    ['markdown', markdownCss],
    ['pasteable-textarea', pasteableTextareaCss],
  ]

  it.each(uiFiles)('%s hard-codes no monospace family and uses mono only on allowed selectors', (_name, css) => {
    expect(css).not.toMatch(/font-family:[^;]*(monospace|JetBrains|Menlo|Consolas)/)
    const monoSelectors = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
      .filter((m) => /var\(--font-mono\)/.test(m[2] ?? ''))
      .map((m) => (m[1] ?? '').replace(/\/\*[\s\S]*?\*\//g, '').trim())
    for (const selector of monoSelectors) expect(MONO_ALLOWED.has(selector), selector).toBe(true)
  })
})

describe('skins CSS contract — shell parity (rail, header, tabs, stage header, banner)', () => {
  it('draws the rail progress as one segment per stage, green when done and amber while active', () => {
    expect(rule(stagesListCss, '.rail-progress-seg')).toMatch(/flex:\s*1;\s*height:\s*3px/)
    expect(rule(stagesListCss, '.rail-progress-seg[data-state="done"]')).toMatch(/var\(--success\)/)
    expect(rule(stagesListCss, '.rail-progress-seg[data-state="active"]')).toMatch(/var\(--attention\)/)
  })

  it('fills only the done stage node; other stages stay numbered rings', () => {
    expect(stagesListCss).toMatch(/\.status-dot\[data-status="done"\] \{ background: currentColor; \}/)
    expect(rule(stagesListCss, '.status-dot')).toMatch(/background:\s*transparent/)
    expect(stagesListCss).not.toContain('.stage-connector')
  })

  it('keeps Assistant and Files quiet (no outline) in the header', () => {
    expect(headerCss).toMatch(/\.header-actions \.icon-btn\.gh-files-btn,\s*\n\.header-actions \.icon-btn\.gh-side-agent-btn \{\s*border-color:\s*transparent/)
    expect(rule(headerCss, '.global-header')).toMatch(/min-height:\s*64px/)
  })

  it('shows every metric as a label over its value, without icons', () => {
    expect(rule(runMetricsCss, '.metric')).toMatch(/flex-direction:\s*column/)
    expect(runMetricsCss).not.toContain('.metric-icon')
  })

  // The hidden 260px tooltip, centred on the tile, is scrollable overflow even at opacity 0: it widened the page
  // at 390px and again at 601-640px. Narrow widths anchor it to the tile's right edge instead of hiding it,
  // so hover and keyboard focus still explain the tile.
  describe('cost tooltip never widens the page', () => {
    // Bodies of every `@media (max-width: Npx)` block, found by brace matching (blocks contain nested rules).
    const maxWidthBlocks = (css: string) => {
      const blocks: { px: number; body: string }[] = []
      for (const m of css.matchAll(/@media \(max-width:\s*(\d+)px\)\s*\{/g)) {
        let depth = 1
        let i = m.index + m[0].length
        const from = i
        while (depth > 0 && i < css.length) {
          depth += css[i] === '{' ? 1 : css[i] === '}' ? -1 : 0
          i++
        }
        blocks.push({ px: Number(m[1]), body: css.slice(from, i - 1) })
      }
      return blocks
    }
    const tooltip = '.metric-cost[data-tooltip]::after'
    const anchored = maxWidthBlocks(runMetricsCss).filter((b) => rule(b.body, tooltip) !== '')

    it('right-anchors the tooltip in a block that covers the 601-640px band', () => {
      expect(anchored.length).toBeGreaterThan(0)
      for (const { px, body } of anchored) {
        expect(px).toBeGreaterThanOrEqual(640)
        const decls = rule(body, tooltip)
        expect(decls).toMatch(/left:\s*auto/)
        expect(decls).toMatch(/right:\s*0/)
        expect(decls).toMatch(/transform:\s*none/)
      }
    })

    it('bounds the tooltip width by the viewport', () => {
      for (const { body } of anchored) expect(rule(body, tooltip)).toMatch(/max-width:[^;]*100vw/)
    })

    it('never hides the tooltip pseudo-element at any width', () => {
      for (const { body } of maxWidthBlocks(runMetricsCss)) expect(rule(body, tooltip)).not.toMatch(/display:\s*none/)
    })

    it('still reveals the tooltip on hover and keyboard focus', () => {
      expect(runMetricsCss).toMatch(/\.metric-cost\[data-tooltip\]:hover::after,\s*\.metric-cost\[data-tooltip\]:focus-visible::after \{\s*opacity:\s*1;\s*visibility:\s*visible;/)
    })
  })

  it('draws workspace tabs as plain underline tabs with a square count badge and no glow', () => {
    expect(rule(layoutCss, '.workspace-tab')).toMatch(/padding:\s*10px 0/)
    expect(rule(layoutCss, '.workspace-tab.active::after')).toMatch(/height:\s*2px/)
    expect(rule(layoutCss, '.workspace-tab-count')).toMatch(/border-radius:\s*4px/)
    expect(layoutCss).not.toMatch(/attn-glyph|tabGlyphGlow|tabHaloGlow|bannerIconGlow/)
  })

  it('draws the stage status as a sentence-case chip with a dot, not an outlined uppercase badge', () => {
    const chip = rule(layoutCss, '.status-badge')
    expect(chip).toMatch(/border-radius:\s*5px/)
    expect(chip).not.toMatch(/text-transform|letter-spacing|border:/)
    expect(rule(layoutCss, '.status-badge::before')).toMatch(/border-radius:\s*50%/)
  })

  it('keeps the attention banner a quiet card with a thin tone border', () => {
    expect(rule(layoutCss, '.attention-banner')).toMatch(/border:\s*1px solid rgba\(var\(--attention-rgb\), 0\.32\)/)
    expect(rule(layoutCss, '.attention-banner-icon')).not.toMatch(/box-shadow|animation/)
  })

  it('signals attention with colour and the "!" icon only: no pulsing halo or full-width frame line', () => {
    const all = [layoutCss, stagesListCss, graphiteIndexCss, gogaIndexCss, coffeeIndexCss, novacorpsIndexCss].join('\n')
    expect(all).not.toMatch(/attentionPulse/)
    expect(layoutCss).not.toMatch(/\.panel-frame\.attention/)
    expect(layoutCss).not.toMatch(/\.stages-list \[data-attention/)
  })
})

describe('skins CSS contract — small tone text on tints uses the AA text tokens', () => {
  it('derives each text token from its tone through the one --tone-text-mix share', () => {
    expect(tokensCss).toMatch(/--tone-text-mix:\s*\d+%/)
    for (const tone of ['accent', 'attention', 'success', 'danger']) {
      const source = tone === 'accent' ? 'accent-primary' : tone
      expect(tokensCss).toContain(`--${tone}-text:`)
      expect(tokensCss).toMatch(new RegExp(`--${tone}-text:\\s*color-mix\\(in srgb, var\\(--${source}\\) var\\(--tone-text-mix\\), var\\(--text-primary\\)\\)`))
    }
  })

  // Neutral text is not covered by the pure-tone scan, so the two rules that keep it on AA are pinned here.
  it('selected and unread rows read their muted text as the stronger secondary token', () => {
    expect(meshCss).toMatch(
      /\.team-person-row\.active,\s*\.team-thread-item:is\(\.active, \.unread\)\s*\{\s*--text-muted:\s*var\(--text-secondary\);/,
    )
  })

  it('a reply quote inside your own team message uses the primary text colour', () => {
    expect(rule(meshCss, '.team-conversation-message.from-you .team-conversation-reply-quote-text')).toContain(
      'color: var(--text-primary);',
    )
  })

  it('a new agent message is flagged by border and pulse, not by a background wash', () => {
    const block = rule(meshCss, '.team-conversation-message.is-new-agent-message .team-conversation-message-body')
    expect(block).not.toMatch(/background/)
    expect(block).toMatch(/animation:\s*newAgentMessage/)
  })

  it('the stage number inside a status dot is the dot tone pulled toward the text colour', () => {
    expect(rule(stagesListCss, '.dot-num')).toMatch(
      /color:\s*color-mix\(in srgb, currentColor var\(--tone-text-mix\), var\(--text-primary\)\)/,
    )
  })

  it('the avatar picks its text colour per participant kind', () => {
    expect(rule(meshCss, '.mesh-avatar[data-kind="run_agent"]')).toContain('--agent-text: var(--success-text)')
    expect(rule(meshCss, '.mesh-avatar[data-kind="helper"]')).toContain('--agent-text: var(--attention-text)')
  })

  it('the status chip text is its tone pulled toward the text colour, and the dot keeps the pure tone', () => {
    expect(rule(layoutCss, '.status-badge')).toMatch(
      /\bcolor:\s*color-mix\(in srgb, var\(--status-tone\) var\(--tone-text-mix\), var\(--text-primary\)\)/,
    )
    expect(rule(layoutCss, '.status-badge::before')).toContain('background: var(--status-tone)')
    expect(layoutCss).not.toMatch(/\.status-badge\[[^\]]*\](\[[^\]]*\])?\s*\{\s*color:/)
  })

  it('the warning cost badge mixes --cost-warning toward the text colour', () => {
    expect(rule(costPanelCss, '.cost-badge-warn')).toMatch(
      /color:\s*color-mix\(in srgb, var\(--cost-warning\) var\(--tone-text-mix\), var\(--text-primary\)\)/,
    )
  })
})

// --- Structural contrast guards over every shipped stylesheet ---------------------------------------------

type Declaration = { file: string; line: number; selector: string; property: string; value: string }

// A pure tone (accent, status, palette colour) has too little headroom for small text on any tint of itself.
// Small text must use a --*-text token or the --tone-text-mix recipe; the pure tone stays for fills and icons.
const SHIPPED_CSS = import.meta.glob<string>(
  ['../../skins/base/*.css', '../../skins/*/index.css', '../../chat-composer.css', '../../side-agent.css'],
  { query: '?raw', import: 'default', eager: true },
)

function splitSelectors(selector: string): string[] {
  const parts: string[] = []
  let depth = 0
  let from = 0
  for (let i = 0; i < selector.length; i++) {
    if (selector[i] === '(') depth++
    else if (selector[i] === ')') depth--
    else if (selector[i] === ',' && depth === 0) {
      parts.push(selector.slice(from, i).trim())
      from = i + 1
    }
  }
  parts.push(selector.slice(from).trim())
  return parts
}

// scanCss lists every `property: value` with its file, line and innermost rule prelude. `problem` is set when the
// stylesheet ends inside a string, a parenthesis or a rule: a stray `*/` in a comment looks exactly like that, and
// would silently hide the rest of the file from every guard below.
function scanCss(file: string, css: string): { declarations: Declaration[]; problem: string | null } {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, ' '))
  const found: Declaration[] = []
  const preludes: string[] = []
  let chunk = ''
  let chunkLine = 1
  let line = 1
  let parens = 0
  let quote = ''
  const flush = (): void => {
    const colon = chunk.indexOf(':')
    if (colon > 0 && preludes.length > 0) {
      found.push({
        file,
        line: chunkLine,
        selector: preludes[preludes.length - 1]!,
        property: chunk.slice(0, colon).trim(),
        value: chunk.slice(colon + 1).trim().replace(/\s+/g, ' '),
      })
    }
    chunk = ''
  }
  for (const ch of text) {
    if (ch === '\n') line++
    if (quote !== '') {
      if (ch === quote) quote = ''
    } else if (ch === '"' || ch === "'") quote = ch
    else if (ch === '(') parens++
    else if (ch === ')') parens--
    else if (parens === 0 && ch === '{') {
      preludes.push(chunk.trim().replace(/\s+/g, ' '))
      chunk = ''
      continue
    } else if (parens === 0 && ch === ';') {
      flush()
      continue
    } else if (parens === 0 && ch === '}') {
      flush()
      preludes.pop()
      continue
    }
    if (chunk.trim() === '' && ch.trim() !== '') chunkLine = line
    chunk += ch
  }
  const open = [quote !== '' && 'string', parens !== 0 && `parentheses (${parens})`, preludes.length > 0 && 'rule'].filter(Boolean)
  const problem = open.length > 0 ? `${file} ends inside an open ${open.join(', ')}` : found.length === 0 ? `${file} has no declarations` : null
  return { declarations: found, problem }
}

const SCANNED = Object.entries(SHIPPED_CSS).map(([path, css]) => scanCss(path.replace(/^(\.\.\/)+/, ''), css))
const SHIPPED_DECLARATIONS = SCANNED.flatMap((scanned) => scanned.declarations)

const where = (d: Declaration): string => `${d.file}:${d.line} ${d.selector} { ${d.property}: ${d.value} }`
const onlyAllowed = (d: Declaration, allowed: string[]): boolean =>
  splitSelectors(d.selector).every((part) => allowed.includes(part.replace(/\[[^\]]*\]/g, '')))

describe('skins CSS contract — scanner', () => {
  it('reads every stylesheet and resolves declarations inside nested at-rules', () => {
    const { declarations, problem } = scanCss(
      'x.css',
      '/* c */\n@media (max-width: 1px) {\n  .a, .b:is(.c, .d) {\n    color: red;\n    background: url("data:x;y");\n  }\n}\n',
    )
    expect(problem).toBeNull()
    expect(declarations.map((d) => [d.line, d.selector, d.property, d.value])).toEqual([
      [4, '.a, .b:is(.c, .d)', 'color', 'red'],
      [5, '.a, .b:is(.c, .d)', 'background', 'url("data:x;y")'],
    ])
  })

  it('reports a comment that ends early instead of silently losing the rest of the file', () => {
    expect(scanCss('x.css', '/* tokens (--surface-*/--text-*/ and more) */\n.a { color: red; }\n').problem).toMatch(/open parentheses/)
    expect(scanCss('x.css', '.a { color: red;\n').problem).toMatch(/open rule/)
    expect(scanCss('x.css', '/* only a comment */\n').problem).toMatch(/no declarations/)
  })

  it('scans all 26 stylesheets, each balanced and with declarations', () => {
    expect(Object.keys(SHIPPED_CSS)).toHaveLength(26)
    expect(SCANNED).toHaveLength(26)
    expect(SCANNED.flatMap(({ problem }) => (problem === null ? [] : [problem]))).toEqual([])
  })
})

const TONE_VARS = 'accent-primary|attention|success|danger|cost-warning|amber|mint|coral|violet|c-[\\w-]+|status-tone|agent-color'

// Glyphs and icons are non-text content: WCAG asks 3:1, which the pure tone meets. The status dot only carries
// currentColor to its ring; its number uses the text recipe. Links and syntax highlighting are text, so they
// use the --*-text tokens.
const PURE_GLYPHS = [
  '.pause-banner-icon',
  '.attention-banner-icon',
  '.attention-banner .attention-banner-icon',
  '.gh-mark',
  '.gh-wordmark::before',
  '.assistant-branch-aggregate::before',
  '.assistant-task-view-error-icon',
  '.status-dot',
]

describe('skins CSS contract — no pure tone as text colour', () => {
  const PURE_TONE = new RegExp(`^var\\(--(${TONE_VARS})\\s*[,)]`)
  it('no `color:` declaration uses a pure tone outside the glyph allow-list', () => {
    const offenders = SHIPPED_DECLARATIONS.filter(
      (d) => d.property === 'color' && PURE_TONE.test(d.value) && !onlyAllowed(d, PURE_GLYPHS),
    )
    expect(offenders.map(where)).toEqual([])
  })
})

// A tone wash is a translucent tone background: rgba(var(--tone-rgb), a), color-mix(tone N%, transparent) or a
// literal skin rgba(). Grey and black layers (r = g = b) are shadows and scrims, not tones.
const TINT_BUDGET = 0.18
const TONE_RGB_TINT = /rgba\(\s*var\(--(?:accent-primary|attention|success|danger)-rgb\)\s*,\s*([\d.]+)\s*\)/g
const TONE_MIX_TINT = new RegExp(
  `color-mix\\(\\s*in srgb\\s*,\\s*var\\(--(?:${TONE_VARS})\\)\\s*([\\d.]+)%\\s*,(?!\\s*var\\(--text-primary\\))`,
  'g',
)
const LITERAL_RGBA = /rgba\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*([\d.]+)\s*\)/g

const tintAlphas = (value: string): number[] => [
  ...[...value.matchAll(TONE_RGB_TINT)].map((m) => +m[1]!),
  ...[...value.matchAll(TONE_MIX_TINT)].map((m) => +m[1]! / 100),
]
const literalToneAlphas = (value: string): number[] =>
  [...value.matchAll(LITERAL_RGBA)].filter((m) => !(m[1] === m[2] && m[2] === m[3])).map((m) => +m[4]!)
const washAlphas = (d: Declaration): number[] =>
  /^(background|--)/.test(d.property) ? [...tintAlphas(d.value), ...(d.property.startsWith('background') ? literalToneAlphas(d.value) : [])] : []

// Layers that carry no text: the selection highlight, the progress bar gradient, the drag handle, the hairline under
// the selected stage and the novacorps glow behind the page.
const NON_TEXT_LAYERS = ['::selection', '.progress-fill', '.resize-handle', '.stage-item.active::after', '.ray::before']
// The coffee glow fades out within the first 55% of the main pane and peaks above its top edge, behind the header.
const PAGE_GLOW = [':root #main']

describe('skins CSS contract — tone tints behind text stay within the alpha budget', () => {
  // The --*-text tokens keep AA on a tint up to this alpha (palette-contrast.test.ts measures the budget); a
  // stronger wash needs its own measured text colour.
  it('no translucent tone background is stronger than the budget outside the non-text allow-list', () => {
    const offenders = SHIPPED_DECLARATIONS.filter((d) => washAlphas(d).some((a) => a > TINT_BUDGET) && !onlyAllowed(d, NON_TEXT_LAYERS))
    expect(offenders.map(where)).toEqual([])
  })
})

// opacity < 1 fades the text of an element together with its fill, so the label can no longer reach 4.5:1 and no
// colour token can repair it. A pending or dimmed state is drawn with a contrast-safe colour or a border instead.
const isFade = (value: string): boolean => /^0?\.\d+$/.test(value) && +value > 0

describe('skins CSS contract — filled button hover', () => {
  const FILLED_TEXT_BUTTON = /\.btn-approve|\.btn-send|#btn-approve|#btn-revise|workspace-action-retry/

  it('tokens.css derives the hover fills from the fills, moving toward the text colour', () => {
    expect(tokensCss).toMatch(/--accent-primary-hover:\s*color-mix\(in srgb, var\(--accent-primary\) \d+%, var\(--text-primary\)\)/)
    expect(tokensCss).toMatch(/--success-hover:\s*color-mix\(in srgb, var\(--success\) \d+%, var\(--text-primary\)\)/)
  })

  it('no filled text button lightens its label with a brightness filter on hover', () => {
    const offenders = SHIPPED_DECLARATIONS.filter((d) => d.property === 'filter' && FILLED_TEXT_BUTTON.test(d.selector))
    expect(offenders.map(where)).toEqual([])
  })

  it('every filled text button hover takes its fill from a hover token', () => {
    const hovers = SHIPPED_DECLARATIONS.filter(
      (d) => /^background(-color)?$/.test(d.property) && /:hover/.test(d.selector) && FILLED_TEXT_BUTTON.test(d.selector),
    )
    expect(hovers.length).toBeGreaterThanOrEqual(5)
    expect(hovers.filter((d) => !/^var\(--(accent-primary|success)-hover\)$/.test(d.value)).map(where)).toEqual([])
  })

  it('no filled text button fades its label on hover', () => {
    const faded = SHIPPED_DECLARATIONS.filter(
      (d) => d.property === 'opacity' && /:hover/.test(d.selector) && FILLED_TEXT_BUTTON.test(d.selector) && isFade(d.value),
    )
    expect(faded.map(where)).toEqual([])
  })
})

describe('skins CSS contract — no whole-element fade on text', () => {
  const KEYFRAME_STEP = /^(from|to|\d+(\.\d+)?%)$/
  // Fades that carry no text, or sit on a control that is disabled (disabled text is exempt from the minimum).
  const FADE_OK: Array<[RegExp, string]> = [
    [KEYFRAME_STEP, 'a keyframe step: a one-shot entrance that ends at 1'],
    [/::-webkit-scrollbar-thumb$/, 'scrollbar thumb: no text'],
    [/:disabled|\[aria-disabled/, 'disabled control'],
    [/^\.(rail|panel)-resizer-grip$/, 'drag handle: a hairline, no text'],
    [/^\.ornament$/, 'decorative ornament'],
    [/^\.cost-tokenmix-empty$/, 'empty bar: no text'],
    [/-spinner$/, 'spinner ring: no text'],
    [/^\.pasteable-attachment\.(uploading|failed) img$/, 'thumbnail image: no text'],
    [/^\.assistant-mention-item\.is-unavailable$/, 'its button is disabled'],
    [/^\.line-commented .*::after$/, 'plan comment icon: a mask with no text, never a control'],
  ]
  const unexplained = (d: Declaration): boolean =>
    !splitSelectors(d.selector).every((part) => FADE_OK.some(([pattern]) => pattern.test(part)))

  it('no rule fades an element below full opacity unless it is on the allow-list', () => {
    const offenders = SHIPPED_DECLARATIONS.filter((d) => d.property === 'opacity' && isFade(d.value) && unexplained(d))
    expect(offenders.map(where)).toEqual([])
  })

  it('the allow-list has no entry that matches nothing', () => {
    const faded = SHIPPED_DECLARATIONS.filter((d) => d.property === 'opacity' && isFade(d.value)).flatMap((d) => splitSelectors(d.selector))
    expect(FADE_OK.filter(([pattern]) => !faded.some((part) => pattern.test(part))).map(([, reason]) => reason)).toEqual([])
  })
})

describe('skins CSS contract — neutral text inside a tone wash', () => {
  // Muted text keeps AA on plain and hovered surfaces, not on a tone wash, so a rule that paints a wash swaps muted
  // for secondary text inside it (palette-contrast.test.ts measures secondary on the washes).
  const WASH_FLOOR = 0.05
  // Chips, badges, counters and hover fills of buttons: their text is a --*-text token or the primary colour, never muted.
  const TONE_TEXT_ONLY = [
    '.chat-composer-card .chat-composer-recipient',
    '.chat-composer-card .chat-composer-recipient.has-problem',
    '.modal-attention-shortcut',
    '.modal-attention-shortcut:hover',
    '.file-browser-attention-shortcut:hover',
    '.cost-badge-ok',
    '.cost-badge-warn',
    '.feed-stage-badge',
    '.status-badge',
    '.workspace-tab-attention .workspace-tab-count',
    '.workspace-tab-attention[data-kind="failed"] .workspace-tab-count',
    '.workspace-tab-attention[data-kind="hook_failed"] .workspace-tab-count',
    '.message-mention',
    '.feed-composer-mode.active',
    '.pause-banner-resume:hover',
    '.jump-latest:hover',
    '.comment-remove:hover',
    '.btn-revise:hover:not(:disabled)',
    '.btn-retry:hover:not(:disabled)',
    '.btn-cancel:hover:not(:disabled)',
    '.btn-cancel-dialog:hover:not(:disabled)',
    '.dialog-pending .btn-cancel-dialog:hover',
    '.mesh-avatar',
    '.progress-bar',
  ]
  // The Team row rule in mesh.css swaps muted for secondary on these (asserted by the Team row test above).
  const TEAM_ROWS = ['.team-person-row.active', '.team-thread-item.active', '.team-thread-item.unread']
  const swapsMutedForSecondary = (d: Declaration): boolean =>
    SHIPPED_DECLARATIONS.some(
      (o) => o.file === d.file && o.selector === d.selector && o.property === '--text-muted' && o.value === 'var(--text-secondary)',
    )

  it('every rule that paints a tone wash swaps --text-muted for --text-secondary or is on the allow-list', () => {
    const offenders = SHIPPED_DECLARATIONS.filter(
      (d) =>
        d.property.startsWith('background') &&
        washAlphas(d).some((a) => a >= WASH_FLOOR && a <= TINT_BUDGET) &&
        !onlyAllowed(d, [...TONE_TEXT_ONLY, ...TEAM_ROWS, ...PAGE_GLOW, ...NON_TEXT_LAYERS]) &&
        !swapsMutedForSecondary(d),
    )
    expect(offenders.map(where)).toEqual([])
  })
})

describe('skins CSS contract — plan line numbers share one style', () => {
  const planDeclarations = SHIPPED_DECLARATIONS.filter((d) => d.file.endsWith('skins/base/plan-panel.css'))
  const selectorsOf = (d: Declaration): string[] => splitSelectors(d.selector)
  // Any generated box under a plan row is the number (::before) or the comment icon (::after), whatever classes its
  // selector uses: a second style for one row type can only come back as another rule of this kind.
  const underPlanRows = (part: string): boolean => /^\.line-commented\b(?!-)/.test(part)
  const touchesNumber = (d: Declaration): boolean => selectorsOf(d).some((part) => underPlanRows(part) && /::before$/.test(part))
  const touchesIcon = (d: Declaration): boolean => selectorsOf(d).some((part) => underPlanRows(part) && /::after$/.test(part))
  const numbers = planDeclarations.filter(touchesNumber)
  const icons = planDeclarations.filter(touchesIcon)
  const preludesSetting = (declarations: Declaration[], property: string): string[] =>
    Array.from(new Set(declarations.filter((d) => d.property === property).map((d) => d.selector)))

  // State rules recolour a number; only the first-line offset (margin-top) may differ per row type.
  const NUMBER_GEOMETRY = ['position', 'left', 'width', 'min-width', 'padding', 'font', 'font-size', 'font-family', 'line-height', 'text-align', 'opacity']

  it('finds the shared number rule and no second mechanism', () => {
    expect(preludesSetting(numbers, 'position')).toHaveLength(1)
    expect(planPanelCss).not.toContain('md-line-no')
  })

  it.each(NUMBER_GEOMETRY)('%s of a number is declared once, for every row type', (property) => {
    expect(preludesSetting(numbers, property).length).toBeLessThanOrEqual(1)
  })

  // `font` and `font-size` set the same size: a small override must not hide behind the other property name.
  it('the number type size comes from one rule whichever property sets it', () => {
    const typography = new Set(['font', 'font-size', 'line-height'])
    const setters = Array.from(new Set(numbers.filter((d) => typography.has(d.property)).map((d) => d.selector)))
    expect(setters).toHaveLength(1)
  })

  it('the shared number is 10px monospace in a 30px column at the left edge, on the muted token', () => {
    const shared = numbers.filter((d) => d.selector === preludesSetting(numbers, 'position')[0])
    const value = (property: string): string | undefined => shared.find((d) => d.property === property)?.value
    expect(value('font')).toBe('10px/16px var(--font-mono)')
    expect(value('left')).toBe('4px')
    expect(value('min-width')).toBe('30px')
    expect(value('text-align')).toBe('center')
    expect(value('color')).toBe('var(--text-muted)')
  })

  it('every row type centres the number on its first line through --row-h alone', () => {
    const offsets = numbers.filter((d) => d.property === 'margin-top')
    expect(offsets.map((d) => d.value)).toEqual(['calc((var(--row-h) - 16px) / 2)'])
  })

  it('the comment icon has one size and column for every row type', () => {
    for (const property of ['width', 'height', 'right']) expect(preludesSetting(icons, property).length, property).toBeLessThanOrEqual(1)
    const resting = icons.filter((d) => d.property === 'opacity').map((d) => d.value)
    expect(Array.from(new Set(resting)).sort()).toEqual(['0.55', '1'])
  })
})

describe('skins CSS contract — every line-commented document reserves the number gutter', () => {
  // The number is absolute at left: 4px of the nearest positioned ancestor, so the document body itself must be
  // that ancestor and must own the left padding the number sits in. Keyed on a plan container instead, the
  // Question dialog (the same component) draws its numbers over the first letters of the text.
  const own = (d: Declaration): boolean => splitSelectors(d.selector).some((part) => /(^|\s)\.line-commented$/.test(part))
  const shippedOnDocument = SHIPPED_DECLARATIONS.filter(own)
  const paddingLeft = (d: Declaration): string | undefined => {
    if (d.property === 'padding-left') return d.value
    if (d.property === 'padding-inline' || d.property === 'padding') return d.value.split(/\s+/).at(d.property === 'padding' ? 3 : 0)
    return undefined
  }

  it('the document body is the positioned ancestor of its numbers', () => {
    const positioned = shippedOnDocument.filter((d) => d.property === 'position')
    expect(positioned.map((d) => d.value)).toEqual(['relative'])
  })

  it('a rule on .line-commented itself reserves the 44px number column on the left', () => {
    const columns = shippedOnDocument.map(paddingLeft).filter((value) => value !== undefined)
    expect(columns).toContain('44px')
  })

  it('the Question dialog does not override the gutter', () => {
    const overrides = SHIPPED_DECLARATIONS.filter(
      (d) =>
        d.file.endsWith('skins/base/dialog-channel.css') &&
        d.property.startsWith('padding') &&
        splitSelectors(d.selector).some((part) => /\.dialog-question(?![\w-])/.test(part)),
    )
    expect(overrides.map(where)).toEqual([])
  })
})

describe('skins CSS contract — plan and question use the full work area', () => {
  // The Approval and Question views fill the work area like the Feed and Team: a width cap or auto margins on
  // their containers bring back the centred reading column.
  const containers = /#plan-section|#plan-content|#plan-empty|#dialog-section|#dialog-scroll|\.dialog-pending|\.dialog-question(?![\w-])|\.plan-hint-row|\.attention-banner|\.stale-comments/
  const onContainers = SHIPPED_DECLARATIONS.filter(
    (d) =>
      /skins\/base\/(plan-panel|dialog-channel)\.css$/.test(d.file) &&
      splitSelectors(d.selector).some((part) => /#(plan|dialog)-/.test(part) && containers.test(part)),
  )

  it('finds the document containers of both views', () => {
    expect(new Set(onContainers.map((d) => d.file.split('/').pop()))).toEqual(new Set(['plan-panel.css', 'dialog-channel.css']))
  })

  it('no container caps its width', () => {
    const capped = onContainers.filter((d) => d.property === 'max-width' || (d.property === 'width' && /\b(min|clamp)\(/.test(d.value)))
    expect(capped.map(where)).toEqual([])
  })

  it('no container centres itself with auto margins', () => {
    const centred = onContainers.filter((d) => d.property.startsWith('margin') && /\bauto\b/.test(d.value))
    expect(centred.map(where)).toEqual([])
  })

  it('both views take their side padding from the one --doc-gutter', () => {
    const gutters = SHIPPED_DECLARATIONS.filter((d) => d.property === 'margin-inline' && d.value === 'var(--doc-gutter)')
    expect(new Set(gutters.map((d) => d.file.split('/').pop()))).toEqual(new Set(['plan-panel.css', 'dialog-channel.css']))
    expect(SHIPPED_DECLARATIONS.filter((d) => d.property === '--doc-gutter').map((d) => d.value)).toEqual(['8px'])
  })
})

describe('skins CSS contract — text colours that bypass the muted swap', () => {
  // These tokens equal --text-muted but are resolved once at :root, so the `--text-muted: var(--text-secondary)` swap of
  // a tone wash never reaches them. Text inside a wash must read --text-muted itself.
  const MUTED_ALIASES = ['--text-placeholder', '--ink-dim', '--feed-command', '--c-pending', '--cost-rail', '--cost-rail-strong', '--qa-answer']
  const ALIAS_TEXT = new RegExp(`^var\\((${MUTED_ALIASES.join('|')})\\s*[,)]`)
  const ALIAS_TEXT_OK = [
    '.status-dot', // carries the pending tone as currentColor to its ring; the stage number uses the text recipe
    '.stage-cost', // rail cost figure: skin literal measured in palette-contrast.test.ts, strong token on hover/selected
    '.stage-item:hover .stage-cost',
    '.stage-item.active .stage-cost',
    '.dialog-history .qa .a', // dialog answer: skin literal measured on the dialog surfaces in palette-contrast.test.ts
  ]

  it('no `color:` reads a muted alias outside a placeholder or the measured allow-list', () => {
    const offenders = SHIPPED_DECLARATIONS.filter(
      (d) =>
        d.property === 'color' &&
        ALIAS_TEXT.test(d.value) &&
        !splitSelectors(d.selector).every((part) => part.endsWith('::placeholder') || ALIAS_TEXT_OK.includes(part.replace(/\[[^\]]*\]/g, ''))),
    )
    expect(offenders.map(where)).toEqual([])
  })

  it('every skin-declared token used as text colour is measured in palette-contrast.test.ts', () => {
    const declaredBySkins = new Set(
      SHIPPED_DECLARATIONS.filter((d) => /skins\/[^/]+\/index\.css$/.test(d.file) && d.property.startsWith('--')).map((d) => d.property),
    )
    const unmeasured = SHIPPED_DECLARATIONS.flatMap((d) => {
      const token = d.property === 'color' ? /^var\((--[\w-]+)\s*[,)]/.exec(d.value)?.[1] : undefined
      const bypassesSwap = token !== undefined && MUTED_ALIASES.includes(token)
      const measured = token === undefined || MEASURED_TEXT_TOKENS.includes(token) || bypassesSwap || onlyAllowed(d, PURE_GLYPHS)
      return token !== undefined && declaredBySkins.has(token) && !measured ? [where(d)] : []
    })
    expect(unmeasured).toEqual([])
  })
})

describe('skins CSS contract — tints follow the palette', () => {
  // A literal rgba() tint is painted with one set of numbers in both modes, so it stops matching the tone it stands for
  // (coffee light tinted with the dark amber). Tints use the -rgb tokens or color-mix() of the tone.
  it('no background paints a literal tone rgba() outside the non-text layers', () => {
    const offenders = SHIPPED_DECLARATIONS.filter(
      (d) => d.property.startsWith('background') && literalToneAlphas(d.value).length > 0 && !onlyAllowed(d, [...NON_TEXT_LAYERS, ...PAGE_GLOW]),
    )
    expect(offenders.map(where)).toEqual([])
  })
})

describe('skins CSS contract — a tinted line inside a quote sets its own text colour', () => {
  // A line anchor inherits `color` already computed on the quote, before its own muted-to-secondary swap, so the
  // anchor has to resolve --text-muted itself.
  it('the plan quote sets --text-muted on its anchored descendants, not only on the quote', () => {
    const own = SHIPPED_DECLARATIONS.filter(
      (d) =>
        d.file.endsWith('skins/base/plan-panel.css') &&
        d.property === 'color' &&
        d.value === 'var(--text-muted)' &&
        splitSelectors(d.selector).some((part) => /blockquote [^,]*\[data-line\]$/.test(part)),
    )
    expect(own.map(where)).not.toEqual([])
  })
})

describe('skins CSS contract — the Question answer field is in the field family', () => {
  // The answer field reads like the Feed composer card: a rounded raised surface, the control border, a plain
  // placeholder and the shared focus ring. Colours come from tokens only, so every palette stays readable.
  const answerField = SHIPPED_DECLARATIONS.filter((d) => splitSelectors(d.selector).some((part) => /\.dialog-custom(?![\w-])/.test(part)))
  const valuesOf = (property: string, pseudo = ''): string[] =>
    answerField
      .filter((d) => d.property === property && splitSelectors(d.selector).some((part) => part.endsWith(`.dialog-custom${pseudo}`)))
      .map((d) => d.value)

  it('finds the answer field rules', () => {
    expect(valuesOf('border')).not.toEqual([])
  })

  it('rounds its corners with a radius token', () => {
    const radii = valuesOf('border-radius')
    expect(radii).not.toEqual([])
    expect(radii.filter((value) => !/^var\(--radius-(sm|md|lg)\)$/.test(value))).toEqual([])
  })

  it('sits on the raised surface', () => {
    expect(valuesOf('background')).toEqual(['var(--surface-raised)'])
  })

  it('keeps the placeholder plain: no letter-spacing, full opacity, the placeholder token', () => {
    expect(answerField.filter((d) => d.property === 'letter-spacing').map(where)).toEqual([])
    expect(valuesOf('color', '::placeholder')).toEqual(['var(--text-placeholder)'])
    expect(valuesOf('opacity', '::placeholder')).toEqual(['1'])
  })

  it('shows the shared focus ring on focus', () => {
    expect(valuesOf('box-shadow', ':focus').join(' ')).toContain('var(--focus-ring)')
  })

  it('paints no literal colour', () => {
    expect(answerField.filter((d) => /#[0-9a-f]{3,8}\b|\brgba?\(/i.test(d.value)).map(where)).toEqual([])
  })
})

describe('skins CSS contract — wide tables and images stay inside a line-commented document', () => {
  // The plan and the Question body must never scroll sideways: a wide table scrolls inside itself, an image
  // shrinks to the text width. The numbers and comment icons hang off .line-commented, so they stay put.
  const rulesFor = (tag: string) =>
    SHIPPED_DECLARATIONS.filter((d) =>
      splitSelectors(d.selector).some((part) => /^\.line-commented\b/.test(part) && new RegExp(`(^|[\\s>])${tag}$`).test(part)),
    )
  const valuesOf = (tag: string, property: string): string[] =>
    rulesFor(tag)
      .filter((d) => d.property === property)
      .map((d) => d.value)

  it('finds the table and image rules', () => {
    expect(rulesFor('table')).not.toEqual([])
    expect(rulesFor('img')).not.toEqual([])
  })

  it('a table is a block bounded to the text width with its own horizontal scroll', () => {
    expect(valuesOf('table', 'display')).toEqual(['block'])
    expect(valuesOf('table', 'max-width')).toEqual(['100%'])
    expect(valuesOf('table', 'overflow-x')).toEqual(['auto'])
  })

  it('an image shrinks to the text width and keeps its ratio', () => {
    expect(valuesOf('img', 'max-width')).toEqual(['100%'])
    expect(valuesOf('img', 'height')).toEqual(['auto'])
  })

  it('the table does not become the containing block of the hanging numbers and icons', () => {
    expect(rulesFor('table').filter((d) => d.property === 'position').map(where)).toEqual([])
  })
})

describe('skins CSS contract — stage time colours', () => {
  const skinFiles = {
    graphite: graphiteIndexCss,
    goga: gogaIndexCss,
    coffee: coffeeIndexCss,
    novacorps: novacorpsIndexCss,
  }
  const modeBlock = (css: string, mode: 'dark' | 'light') =>
    css.match(new RegExp(`:root\\[data-theme="${mode}"\\]\\s*\\{[\\s\\S]*?\\n\\}`))?.[0] ?? ''
  // The value of a token in one block, following var(--other) aliases defined in the same block.
  const resolve = (block: string, name: string): string => {
    const raw = block.match(new RegExp(`${name}:\\s*([^;]+);`))?.[1]?.trim() ?? ''
    const aliasName = raw.match(/^var\((--[\w-]+)\)$/)?.[1]
    return aliasName === undefined ? raw.toLowerCase() : resolve(block, aliasName)
  }

  // CIE76 distance in Lab (sRGB, D65): enough to tell two share-bar segments apart.
  const deltaE76 = (hexA: string, hexB: string): number => {
    const lab = (hex: string): number[] => {
      const linear = [1, 3, 5].map((i) => {
        const c = parseInt(hex.slice(i, i + 2), 16) / 255
        return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
      })
      const [r, g, b] = linear as [number, number, number]
      const xyz = [
        (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047,
        0.2126 * r + 0.7152 * g + 0.0722 * b,
        (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883,
      ].map((t) => (t > 216 / 24389 ? Math.cbrt(t) : (24389 / 27 * t + 16) / 116)) as [number, number, number]
      return [116 * xyz[1] - 16, 500 * (xyz[0] - xyz[1]), 200 * (xyz[1] - xyz[2])]
    }
    const [a, b] = [lab(hexA), lab(hexB)] as [number[], number[]]
    return Math.hypot(a[0]! - b[0]!, a[1]! - b[1]!, a[2]! - b[2]!)
  }

  it('the share bar and legend read the dedicated work, waiting and paused tokens', () => {
    expect(costPanelCss).toMatch(/\.cost-time-segment\.work, \.cost-time-swatch\.work \{ background: var\(--cost-time-work\); \}/)
    expect(costPanelCss).toMatch(/\.cost-time-segment\.waiting, \.cost-time-swatch\.waiting \{ background: var\(--cost-time-waiting\); \}/)
    expect(costPanelCss).toMatch(/\.cost-time-segment\.paused, \.cost-time-swatch\.paused \{ background: var\(--cost-time-paused\); \}/)
  })

  for (const [skin, css] of Object.entries(skinFiles)) {
    for (const mode of ['dark', 'light'] as const) {
      it(`${skin} ${mode}: work, waiting and paused are defined and pairwise distinguishable`, () => {
        const block = modeBlock(css, mode)
        const work = resolve(block, '--cost-time-work')
        const waiting = resolve(block, '--cost-time-waiting')
        const paused = resolve(block, '--cost-time-paused')
        for (const colour of [work, waiting, paused]) expect(colour).toMatch(/^#[0-9a-f]{6}$/)
        expect(deltaE76(work, waiting)).toBeGreaterThanOrEqual(30)
        expect(deltaE76(work, paused)).toBeGreaterThanOrEqual(30)
        expect(deltaE76(waiting, paused)).toBeGreaterThanOrEqual(30)
      })
    }
  }
})

describe('skins CSS contract — rail time column', () => {
  it('.stage-time hangs on the right edge of the row, so every row ends its time at the same x', () => {
    const time = rule(stagesListCss, '.stage-time')
    expect(time).toMatch(/position:\s*absolute/)
    expect(time).toMatch(/right:\s*8px/)
    expect(rule(stagesListCss, '.stage-item')).toMatch(/position:\s*relative/)
    expect(rule(stagesListCss, '.stage-actions')).not.toMatch(/min-width/)
    // The containing block must be the row, not the button or its status line.
    for (const selector of ['.stage-row', '.stage-label', '.stage-status-line']) {
      expect(rule(stagesListCss, selector)).not.toMatch(/position:/)
    }
  })
})
