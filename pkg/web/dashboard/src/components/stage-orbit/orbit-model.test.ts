import { describe, expect, it } from 'vitest'
import type { DeclaredAssistant, Stage } from '../../types/stage'
import {
  helperInitials, isOrbitVisible, ORBIT_SATELLITE_SIZE, orbitInitials, orbitPageAnnouncement, orbitPages,
  orbitSlotOffsets, orbitTooltip,
} from './orbit-model'

function helper(name: string, extra: Partial<DeclaredAssistant> = {}): DeclaredAssistant {
  return { profile: name.toLowerCase(), name, trigger: 'one_shot', mode: 'advisory', ...extra }
}

function stage(extra: Partial<Stage>): Stage {
  return { id: 's', name: '', status: 'pending', ...extra } as Stage
}

describe('isOrbitVisible', () => {
  const declared = [helper('Reviewer')]
  it('needs capability, a never-started stage and declarations', () => {
    expect(isOrbitVisible(stage({ timing: { started: false, complete: true }, declaredAssistants: declared }), true)).toBe(true)
    expect(isOrbitVisible(stage({ timing: { started: false, complete: true }, declaredAssistants: declared }), false)).toBe(false)
    expect(isOrbitVisible(stage({ timing: { started: true, complete: false }, declaredAssistants: declared }), true)).toBe(false)
    expect(isOrbitVisible(stage({ declaredAssistants: declared }), true)).toBe(false)
    expect(isOrbitVisible(stage({ timing: { started: false, complete: true } }), true)).toBe(false)
    expect(isOrbitVisible(stage({ timing: { started: false, complete: true }, declaredAssistants: [] }), true)).toBe(false)
  })
})

describe('helperInitials', () => {
  it('takes the first letters of the first two words', () => {
    expect(helperInitials('Code reviewer', 'p', new Set())).toBe('CR')
    expect(helperInitials('Ревьюер кода', 'p', new Set())).toBe('РК')
  })
  it('takes two letters of a single word and falls back to the profile', () => {
    expect(helperInitials('Scribe', 'p', new Set())).toBe('SC')
    expect(helperInitials('', 'qa-bot', new Set())).toBe('QB')
    expect(helperInitials('---', '', new Set())).toBe('?')
  })
  it('tells duplicates apart', () => {
    const list = [helper('Code reviewer'), helper('Code runner'), helper('Cool robot')]
    const initials = orbitInitials(list)
    expect(new Set(initials).size).toBe(3)
    expect(initials[0]).toBe('CR')
  })
})

describe('orbitPages', () => {
  const many = (n: number): DeclaredAssistant[] => Array.from({ length: n }, (_, i) => helper(`H${i}`))
  it('puts up to four helpers on one page without +N', () => {
    for (const n of [1, 2, 3, 4]) expect(orbitPages(many(n))).toEqual([{ helpers: many(n), start: 0, remaining: 0 }])
  })
  it('pages by three with a +N slot from five helpers', () => {
    const pages = orbitPages(many(7))
    expect(pages.map((p) => [p.start, p.helpers.length, p.remaining])).toEqual([[0, 3, 4], [3, 3, 4], [6, 1, 6]])
  })
  it('announces the visible range', () => {
    const pages = orbitPages(many(7))
    expect(orbitPageAnnouncement(pages[1]!, 7)).toBe('Showing helpers 4 to 6 of 7')
    expect(orbitPageAnnouncement(pages[2]!, 7)).toBe('Showing helpers 7 of 7')
  })
})

describe('orbitSlotOffsets', () => {
  it('keeps satellites left of the hub without overlap', () => {
    for (const coarse of [false, true]) {
      const size = coarse ? ORBIT_SATELLITE_SIZE.coarse : ORBIT_SATELLITE_SIZE.fine
      for (const count of [1, 2, 3, 4]) {
        const points = orbitSlotOffsets(count, coarse)
        expect(points).toHaveLength(count)
        for (const p of points) expect(p.x).toBeLessThan(0)
        for (let i = 0; i < points.length; i++) {
          for (let j = i + 1; j < points.length; j++) {
            expect(Math.hypot(points[i]!.x - points[j]!.x, points[i]!.y - points[j]!.y)).toBeGreaterThan(size)
          }
        }
      }
    }
  })
})

describe('orbitTooltip', () => {
  it('joins name, trigger, loop and required', () => {
    expect(orbitTooltip(helper('Reviewer', { trigger: 'checkpoints' }))).toBe('Reviewer · At checkpoints')
    expect(orbitTooltip(helper('Reviewer', { trigger: 'checkpoints', loopEvery: '5m', mode: 'required' })))
      .toBe('Reviewer · At checkpoints · every 5m · Required')
  })
})
