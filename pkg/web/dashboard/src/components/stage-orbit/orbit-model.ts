import type { DeclaredAssistant, Stage } from '../../types/stage'
import { helperTriggerLabel } from '../../lib/helper-trigger'

// До 4 помощников — все на дуге; больше — по 3 на страницу и четвёртый слот «+N».
const MAX_ARC_SLOTS = 4
const PAGE_SIZE = 3

// Хаб показывается, пока стадия ни разу не стартовала. Нет timing (старый сервер) — факт старта неизвестен.
export function isOrbitVisible(stage: Stage, collaborationEnabled: boolean): boolean {
  return collaborationEnabled && stage.timing?.started === false && (stage.declaredAssistants?.length ?? 0) > 0
}

export type OrbitPage = {
  helpers: DeclaredAssistant[]
  // Индекс первого помощника страницы в общем списке.
  start: number
  // Сколько помощников не поместилось на странице; > 0 — на дуге есть слот «+N».
  remaining: number
}

export function orbitPages(list: DeclaredAssistant[]): OrbitPage[] {
  if (list.length <= MAX_ARC_SLOTS) return [{ helpers: list, start: 0, remaining: 0 }]
  const pages: OrbitPage[] = []
  for (let start = 0; start < list.length; start += PAGE_SIZE) {
    const helpers = list.slice(start, start + PAGE_SIZE)
    pages.push({ helpers, start, remaining: list.length - helpers.length })
  }
  return pages
}

// Углы слотов в градусах от направления «влево от хаба»; отрицательные — выше.
const ARC_ANGLES: Record<number, number[]> = {
  1: [0],
  2: [-22, 22],
  3: [-42, 0, 42],
  4: [-63, -21, 21, 63],
}

export const ORBIT_RADIUS = { fine: 52, coarse: 70 }
// Половина стороны области нажатия хаба (24px padding box + inset ::after): 28 и 44px.
export const ORBIT_HUB_HIT_HALF = { fine: 14, coarse: 22 }
export const ORBIT_SATELLITE_SIZE = { fine: 30, coarse: 44 }

export type OrbitPoint = { x: number; y: number }

// Смещение центров слотов относительно центра хаба (x < 0 — левее).
export function orbitSlotOffsets(slotCount: number, coarse: boolean): OrbitPoint[] {
  const radius = coarse ? ORBIT_RADIUS.coarse : ORBIT_RADIUS.fine
  return (ARC_ANGLES[slotCount] ?? []).map((deg) => {
    const rad = (deg * Math.PI) / 180
    return { x: -radius * Math.cos(rad), y: radius * Math.sin(rad) }
  })
}

function letters(text: string): string[] {
  return text.split(/[^\p{L}\p{N}]+/u).filter((word) => word !== '')
}

function baseInitials(source: string): string {
  const words = letters(source)
  if (words.length === 0) return ''
  const firstChars = words.slice(0, 2).map((word) => Array.from(word)[0])
  const two = words.length > 1 ? firstChars.join('') : Array.from(words[0] ?? '').slice(0, 2).join('')
  return two.toLocaleUpperCase()
}

// helperInitials — две буквы из имени (иначе из profile ID); при совпадении с уже занятыми берёт другие
// буквы имени, затем цифру.
export function helperInitials(name: string, profile: string, taken: ReadonlySet<string>): string {
  const initials = baseInitials(name) || baseInitials(profile) || '?'
  if (!taken.has(initials)) return initials
  const first = Array.from(initials)[0] ?? '?'
  const pool = Array.from(letters(name).join('') || letters(profile).join('')).slice(1)
  for (const ch of pool) {
    const candidate = (first + ch).toLocaleUpperCase()
    if (!taken.has(candidate)) return candidate
  }
  for (let n = 2; n < 10; n++) {
    if (!taken.has(first + n)) return first + n
  }
  return initials
}

// orbitInitials — инициалы всего списка в порядке объявления, без дублей.
export function orbitInitials(list: DeclaredAssistant[]): string[] {
  const taken = new Set<string>()
  return list.map((helper) => {
    const initials = helperInitials(helper.name, helper.profile, taken)
    taken.add(initials)
    return initials
  })
}

// Tooltip и описание: «Имя · Trigger», для loop — «· every 5m», для required — суффикс «· Required».
export function orbitTooltip(helper: DeclaredAssistant): string {
  const parts = [helper.name, helperTriggerLabel(helper.trigger, helper.loopEvery)]
  if (helper.mode === 'required') parts.push('Required')
  return parts.join(' · ')
}

export function orbitHubLabel(count: number, stageId: string): string {
  return `${count} ${count === 1 ? 'helper' : 'helpers'} declared for ${stageId}`
}

function rangeText(page: OrbitPage, separator: string): string {
  const from = page.start + 1
  const to = page.start + page.helpers.length
  return from === to ? `${from}` : `${from}${separator}${to}`
}

// Текст слота «+N» — число помощников, скрытых на текущей странице; диапазон следующей страницы — в accessible label.
export function orbitMoreText(page: OrbitPage): string {
  return `+${page.remaining}`
}

export function orbitMoreLabel(next: OrbitPage, total: number): string {
  return `Show helpers ${rangeText(next, '–')} of ${total}`
}

export function orbitPageAnnouncement(page: OrbitPage, total: number): string {
  return `Showing helpers ${rangeText(page, ' to ')} of ${total}`
}
