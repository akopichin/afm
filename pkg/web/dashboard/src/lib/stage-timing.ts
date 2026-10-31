import type { Stage } from '../types'
import { STAGE_TIME_KINDS, type StageTimeKind } from '../types/stage'

// Секунды стадии по видам времени; waiting и time считаются из тех же целых, поэтому сумма в UI всегда сходится.
export type StageTimeView = {
  workSec: number
  approvalSec: number
  helpersSec: number
  backoffSec: number
  schedulingSec: number
  pausedSec: number
  // waitingSec — approval + helpers + backoff + scheduling.
  waitingSec: number
  // timeSec — work + waiting + paused.
  timeSec: number
}

export type StageTimes = {
  // null — времени нет: стадия не стартовала или время неизвестно (см. incompleteIds).
  byStage: Map<string, StageTimeView | null>
  // Время неизвестно: неполные данные или нет timing (старый сервер); остальные null-стадии не стартовали.
  incompleteIds: ReadonlySet<string>
  // true, если итоги исключили хотя бы одну неполную стадию.
  partial: boolean
  // Сумма тех же целых секунд по стадиям с известным временем.
  totals: StageTimeView
  // false — известного времени нет ни у одной стадии, а неизвестное есть: итоги «—», а не нули.
  totalsKnown: boolean
}

const LEAF_FIELDS: Record<StageTimeKind, 'workSec' | 'approvalSec' | 'helpersSec' | 'backoffSec' | 'schedulingSec' | 'pausedSec'> = {
  work: 'workSec',
  approval: 'approvalSec',
  helpers: 'helpersSec',
  backoff: 'backoffSec',
  scheduling: 'schedulingSec',
  paused: 'pausedSec',
}

// hasOpenTiming — есть ли стадия с интервалом, который ещё идёт; без неё часы стадий тикать не должны.
export function hasOpenTiming(stages: readonly Stage[]): boolean {
  return stages.some((stage) => stage.timing?.open !== undefined)
}

// stageTimes считает время стадий на момент nowMs. Живая часть открытого интервала добавляется только к его
// виду и только при live (иначе — сервер или стоп-кадр после сбоя), потом каждый вид округляется вниз до секунд.
export function stageTimes(stages: readonly Stage[], nowMs: number, live: boolean): StageTimes {
  const byStage = new Map<string, StageTimeView | null>()
  const incompleteIds = new Set<string>()
  const totals = withSums(emptyLeaves())
  let knownCount = 0

  for (const stage of stages) {
    const timing = stage.timing
    // Новый сервер присылает timing у каждой стадии; без него (старый сервер, битый ответ) факт старта не узнать — время неизвестно.
    if (timing !== undefined && !timing.started) {
      byStage.set(stage.id, null)
      continue
    }
    if (timing === undefined || !timing.complete || timing.ms === undefined) {
      incompleteIds.add(stage.id)
      byStage.set(stage.id, null)
      continue
    }

    const leaves = emptyLeaves()
    for (const kind of STAGE_TIME_KINDS) {
      const liveMs = live && timing.open?.kind === kind ? Math.max(0, nowMs - timing.open.sinceMs) : 0
      leaves[LEAF_FIELDS[kind]] = Math.floor((timing.ms[kind] + liveMs) / 1000)
    }
    const view = withSums(leaves)
    byStage.set(stage.id, view)
    knownCount++
    for (const field of Object.values(LEAF_FIELDS)) totals[field] += view[field]
  }
  totals.waitingSec = totals.approvalSec + totals.helpersSec + totals.backoffSec + totals.schedulingSec
  totals.timeSec = totals.workSec + totals.waitingSec + totals.pausedSec

  return { byStage, incompleteIds, partial: incompleteIds.size > 0, totals, totalsKnown: knownCount > 0 || incompleteIds.size === 0 }
}

type Leaves = Pick<StageTimeView, (typeof LEAF_FIELDS)[StageTimeKind]>

function emptyLeaves(): Leaves {
  return { workSec: 0, approvalSec: 0, helpersSec: 0, backoffSec: 0, schedulingSec: 0, pausedSec: 0 }
}

function withSums(leaves: Leaves): StageTimeView {
  const waitingSec = leaves.approvalSec + leaves.helpersSec + leaves.backoffSec + leaves.schedulingSec
  return { ...leaves, waitingSec, timeSec: leaves.workSec + waitingSec + leaves.pausedSec }
}

// formatStageDuration: `0s`, `0m 45s`, `3m 05s`, `1h 02m 03s`; часы без верхнего предела. null — `—`:
// времени нет (не стартовала / неполные данные), в отличие от честных `0s`.
export function formatStageDuration(seconds: number | null): string {
  if (seconds === null) return '—'
  const total = Math.max(0, Math.floor(seconds))
  if (total === 0) return '0s'
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = String(total % 60).padStart(2, '0')
  if (hours === 0) return `${minutes}m ${secs}s`
  return `${hours}h ${String(minutes).padStart(2, '0')}m ${secs}s`
}
