import { describe, it, expect } from 'vitest'
import layoutCss from '../../../skins/base/layout.css?raw'

// T7.6: the Team tab (and every other workspace tab) must stay a visible text button at every
// viewport width — unlike the header's Files/Side agent buttons, there is no icon-only fallback
// for tabs, so no breakpoint may ever hide .workspace-tab-label. Narrow viewports instead scroll
// the tab strip horizontally (.workspace-tabs has overflow-x: auto), which needs no CSS change
// here; this is a permanent regression guard, not a fix.
describe('layout.css workspace-tab-label narrow-viewport contract (T7.6)', () => {
  it('never collapses a tab label behind display:none at any breakpoint', () => {
    expect(layoutCss).not.toMatch(/\.workspace-tab-label\s*\{[^}]*display:\s*none/)
  })

  it('lets the tab strip scroll horizontally instead of shrinking labels away', () => {
    expect(layoutCss).toMatch(/\.workspace-topbar \.workspace-tabs\s*\{[^}]*overflow-x:\s*auto/)
  })
})
