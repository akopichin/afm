// Loop drafts take a separate operation route so slash text can never become an ordinary message.

import type { MeshParticipant } from '../../api/mesh-client'
import { mentionHandles } from '../../lib/mesh-roster'
import { recipientProblem, resolveTyped } from './composer-recipients'
import { isLoopTarget } from './mesh-recipients'

// LOOP_SYNTAX — the muted guidance shown while a loop command is typed.
export const LOOP_SYNTAX = '/loop <interval> [--until-stage-end] @agent <prompt> · /loop list · /loop stop <id>'

// LOOP_INTERVAL_EXAMPLES — the selectable intervals offered while the interval is typed.
export const LOOP_INTERVAL_EXAMPLES = ['5m', '15m', '1h']

const UNTIL_STAGE_END_FLAG = '--until-stage-end'

export type LoopCommand =
  | { kind: 'add'; every: string; untilStageEnd: boolean; target: string; prompt: string }
  | { kind: 'list' }
  | { kind: 'stop'; loopId: string }

// LoopParse — a failed parse carries the already-resolved `@target` too, when parsing got that far: the
// composer keeps showing the target row instead of hiding it while the rest (e.g. the prompt) is still typed.
export type LoopParse = { ok: true; command: LoopCommand } | { ok: false; error: string; target?: string }

// isLoopCommandDraft accepts leading whitespace so pasted commands cannot fall through to ordinary sends.
// A longer token such as /loops stays ordinary text.
export function isLoopCommandDraft(draft: string): boolean {
  return /^\s*\/loop(\s|$)/.test(draft)
}

// loopSubcommand keeps partial list and stop drafts from being labelled Start loop.
export function loopSubcommand(draft: string): 'add' | 'list' | 'stop' | null {
  const m = /^\s*\/loop([\s\S]*)$/.exec(draft)
  if (m === null) return null
  const first = nextToken(m[1] ?? '').token
  if (first === '') return null
  return first === 'list' || first === 'stop' ? first : 'add'
}

// MIN_INTERVAL_MS — the server accepts Go durations of at least one second.
const MIN_INTERVAL_MS = 1000
const MS_PER_UNIT: Record<string, number> = { ns: 1e-6, us: 1e-3, 'µs': 1e-3, 'μs': 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 }
const DURATION = /^(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+$/
const DURATION_PART = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g

// intervalError — why token is no loop interval, or null.
function intervalError(token: string): string | null {
  if (!DURATION.test(token)) return `"${token}" is not a duration — use 5m, 90s or 1h30m`
  let ms = 0
  for (const part of token.matchAll(DURATION_PART)) ms += Number(part[1]) * (MS_PER_UNIT[part[2] ?? ''] ?? 0)
  return ms < MIN_INTERVAL_MS ? 'The interval must be at least 1s' : null
}

// nextToken — the first whitespace-delimited word of text (leading blanks skipped) and what follows it.
function nextToken(text: string): { token: string; rest: string } {
  const m = /^\s*(\S*)([\s\S]*)$/.exec(text)
  return { token: m?.[1] ?? '', rest: m?.[2] ?? '' }
}

// parseLoopCommand resolves one target before preserving all later mentions as prompt text.
export function parseLoopCommand(draft: string): LoopParse {
  const fail = (error: string, target?: string): LoopParse => (target === undefined ? { ok: false, error } : { ok: false, error, target })
  const args = /^\s*\/loop([\s\S]*)$/.exec(draft)?.[1] ?? ''
  const first = nextToken(args)
  if (first.token === '') return fail('Give an interval first, such as 5m or 90s')

  if (first.token === 'list') return first.rest.trim() === '' ? { ok: true, command: { kind: 'list' } } : fail('/loop list takes no arguments')

  if (first.token === 'stop') {
    const id = nextToken(first.rest)
    if (id.token === '') return fail('Say which loop to stop, for example /loop stop L1')
    if (!/^l[1-9]\d*$/i.test(id.token)) return fail(`"${id.token}" is not a loop id — ids look like L1`)
    if (id.rest.trim() !== '') return fail('/loop stop takes one id')
    return { ok: true, command: { kind: 'stop', loopId: id.token.toUpperCase() } }
  }

  const bad = intervalError(first.token)
  if (bad !== null) return fail(bad)

  let after = nextToken(first.rest)
  const untilStageEnd = after.token === UNTIL_STAGE_END_FLAG
  if (untilStageEnd) after = nextToken(after.rest)
  if (after.token.startsWith('--')) return fail(`Unknown option ${after.token} — the only option is ${UNTIL_STAGE_END_FLAG}`)
  if (!after.token.startsWith('@') || after.token === '@') return fail('Name one target with @agent before the prompt')

  const prompt = after.rest.trim()
  if (prompt === '') return fail('Add the prompt to repeat after @agent', after.token.slice(1))
  return { ok: true, command: { kind: 'add', every: first.token, untilStageEnd, target: after.token.slice(1), prompt } }
}

// IntervalSlot — where an interval example is written: the [start, end) range of the interval typed so far, and
// the space to put before it when `/loop` has no blank after it yet.
export type IntervalSlot = { start: number; end: number; lead: ' ' | '' }

// loopIntervalSlot — the interval slot while the interval is being typed (nothing yet, or a word that starts
// like a duration with no blank after it); null once it is finished or another subcommand/target is typed.
export function loopIntervalSlot(draft: string): IntervalSlot | null {
  const m = /^(\s*)\/loop(\s*)(\S*)$/.exec(draft)
  if (m === null) return null
  const leadingWs = m[1] ?? ''
  const blanks = m[2] ?? ''
  const typed = m[3] ?? ''
  if (typed !== '' && (blanks === '' || !/^[\d.]/.test(typed))) return null // `/loops`, `/loop5m`, `list`, `@qa`
  const start = leadingWs.length + '/loop'.length + blanks.length
  return { start, end: draft.length, lead: blanks === '' ? ' ' : '' }
}

export type LoopTarget = { participant: MeshParticipant } | { problem: string }

// resolveLoopTarget rejects ineligible or ambiguous handles instead of silently substituting a recipient.
export function resolveLoopTarget(handle: string, participants: MeshParticipant[], stageId: string): LoopTarget {
  const p = resolveTyped(handle, participants, mentionHandles(participants))
  if (p === 'ambiguous') return { problem: `@${handle} matches several people — pick one from the list` }
  if (p === null) return { problem: `No one is called @${handle} in this run` }
  if (!isLoopTarget(p, stageId)) return { problem: `@${handle} can't be looped — pick a run agent or a helper of this stage` }
  const problem = recipientProblem(p, handle, [])
  return problem !== null ? { problem } : { participant: p }
}
