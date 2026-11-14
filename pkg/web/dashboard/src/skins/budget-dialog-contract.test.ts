import { describe, expect, it } from 'vitest'
import budgetCss from '../../skins/base/budget-dialog.css?raw'
import publicBudgetCss from '../../public/skins/base/budget-dialog.css?raw'
import { declared, media } from './css-blocks'

describe('budget dialog stylesheet', () => {
  it('keeps the dialog inside the viewport and scrolls it', () => {
    expect(declared(budgetCss, '.budget-dialog', 'max-height')).toBe('calc(100dvh - 32px)')
    expect(declared(budgetCss, '.budget-dialog', 'overflow-y')).toBe('auto')
  })

  it('stacks the fields and lets actions wrap on a narrow screen', () => {
    const narrow = media(budgetCss, '(max-width: 600px)')
    expect(declared(narrow, '.budget-custom-fields', 'flex-direction')).toBe('column')
    expect(declared(budgetCss, '.budget-actions', 'flex-wrap')).toBe('wrap')
  })

  it('uses semantic tokens only — no literal colours', () => {
    expect(budgetCss).not.toMatch(/#[0-9a-fA-F]{3,8}\b|rgba?\(\s*\d/)
  })

  it('is mirrored into public/skins and imported by every skin', () => {
    expect(publicBudgetCss).toBe(budgetCss)
    const skins = import.meta.glob('../../skins/*/index.css', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
    for (const css of Object.values(skins)) expect(css).toContain('@import url("../base/budget-dialog.css");')
  })
})
