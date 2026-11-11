import { useMemo, useState } from 'react'

// Состояние строк, которое должно пережить их размонтаж виртуализатором. Ключи — ID данных (ключ вызова, id
// сообщения, id проверки), а не позиции строк: смена состава не переносит состояние на чужую строку.

const MAX_OPEN_KEYS = 500
const MAX_IMAGE_URLS = 200

export type FeedRowState = {
  // scope — стадия, к которой относится состояние; другая стадия начинает с пустого.
  scope: string
  // openTools — ключи вызовов раскрытых серий: серия открыта, если открыт любой её вызов.
  openTools: ReadonlySet<string>
  // openReports — `${stageId}|${verificationId}` раскрытых отчётов verify.
  openReports: ReadonlySet<string>
  // openRaw — id сообщений с раскрытым Raw.
  openRaw: ReadonlySet<string>
  // imageAttempts — сколько повторов загрузки картинки уже сделано, по её URL; хранит не больше MAX_IMAGE_URLS.
  imageAttempts: ReadonlyMap<string, number>
}

// imageUrl — адрес опубликованной стадией картинки: по нему же ведётся счётчик повторов загрузки.
export const imageUrl = (stageId: string, name: string): string => `/api/stages/${encodeURIComponent(stageId)}/artifacts/${encodeURIComponent(name)}`

export const reportKey = (stageId: string, verificationId: string): string => `${stageId}|${verificationId}`

export function emptyRowState(scope: string): FeedRowState {
  return { scope, openTools: new Set(), openReports: new Set(), openRaw: new Set(), imageAttempts: new Map() }
}

// withKeys добавляет ключи в порядке вставки; сверх лимита первыми уходят самые старые.
function withKeys(set: ReadonlySet<string>, keys: readonly string[]): ReadonlySet<string> {
  const next = new Set(set)
  for (const key of keys) {
    next.delete(key)
    next.add(key)
  }
  for (const oldest of next) {
    if (next.size <= MAX_OPEN_KEYS) break
    next.delete(oldest)
  }
  return next
}

function withoutKeys(set: ReadonlySet<string>, keys: readonly string[]): ReadonlySet<string> {
  return new Set([...set].filter((key) => !keys.includes(key)))
}

function toggled(set: ReadonlySet<string>, key: string): ReadonlySet<string> {
  return set.has(key) ? withoutKeys(set, [key]) : withKeys(set, [key])
}

export function isSeriesOpen(state: FeedRowState, memberKeys: readonly string[]): boolean {
  return memberKeys.some((key) => state.openTools.has(key))
}

// toggleSeries открывает серию по её первому и последнему вызовам или закрывает по всем: вытеснение первого
// вызова не закрывает серию.
export function toggleSeries(state: FeedRowState, memberKeys: readonly string[]): FeedRowState {
  const first = memberKeys[0]
  const last = memberKeys.at(-1)
  if (first === undefined || last === undefined) return state
  return { ...state, openTools: isSeriesOpen(state, memberKeys) ? withoutKeys(state.openTools, memberKeys) : withKeys(state.openTools, [first, last]) }
}

export const toggleReport = (state: FeedRowState, key: string): FeedRowState => ({ ...state, openReports: toggled(state.openReports, key) })
export const toggleRaw = (state: FeedRowState, id: string): FeedRowState => ({ ...state, openRaw: toggled(state.openRaw, id) })

// bumpImageAttempt считает ещё один повтор; предел повторов держит FeedImage, а сверх MAX_IMAGE_URLS первыми
// забываются самые старые адреса.
export function bumpImageAttempt(state: FeedRowState, url: string): FeedRowState {
  const attempts = new Map(state.imageAttempts)
  const count = (attempts.get(url) ?? 0) + 1
  attempts.delete(url)
  attempts.set(url, count)
  for (const oldest of attempts.keys()) {
    if (attempts.size <= MAX_IMAGE_URLS) break
    attempts.delete(oldest)
  }
  return { ...state, imageAttempts: attempts }
}

export const isRowStateEmpty = (state: FeedRowState): boolean =>
  state.openTools.size === 0 && state.openReports.size === 0 && state.openRaw.size === 0 && state.imageAttempts.size === 0

// LiveKeys — что есть в неотфильтрованных данных ленты: состояние пропавшего элемента не должно копиться.
export type LiveKeys = {
  itemKeys: ReadonlySet<string>
  reportKeys: ReadonlySet<string>
  messageIds: ReadonlySet<string>
  imageUrls: ReadonlySet<string>
  // seriesTails — последний вызов каждой раскрытой серии: когда вытеснены все вызовы, открытые пользователем,
  // серия остаётся открытой за счёт хвоста.
  seriesTails: readonly string[]
}

const keep = (set: ReadonlySet<string>, live: ReadonlySet<string>): ReadonlySet<string> => {
  const kept = [...set].filter((key) => live.has(key))
  return kept.length === set.size ? set : new Set(kept)
}

// syncRowState выбрасывает ключи, которых больше нет в данных, и запоминает хвосты раскрытых серий. Если
// менять нечего — возвращает то же состояние, чтобы не было лишнего рендера.
export function syncRowState(state: FeedRowState, live: LiveKeys): FeedRowState {
  const kept = keep(state.openTools, live.itemKeys)
  const tails = live.seriesTails.filter((key) => !kept.has(key))
  const openTools = tails.length === 0 ? kept : withKeys(kept, tails)
  const openReports = keep(state.openReports, live.reportKeys)
  const openRaw = keep(state.openRaw, live.messageIds)
  const imageAttempts = [...state.imageAttempts.keys()].every((url) => live.imageUrls.has(url))
    ? state.imageAttempts
    : new Map([...state.imageAttempts].filter(([url]) => live.imageUrls.has(url)))
  if (openTools === state.openTools && openReports === state.openReports && openRaw === state.openRaw && imageAttempts === state.imageAttempts) return state
  return { ...state, openTools, openReports, openRaw, imageAttempts }
}

export type FeedRowActions = {
  toggleSeries: (memberKeys: readonly string[]) => void
  toggleReport: (key: string) => void
  toggleRaw: (id: string) => void
  retryImage: (url: string) => void
  sync: (live: LiveKeys) => void
}

// useFeedRowState — состояние строк стадии `scope`; при смене стадии оно начинается заново прямо в рендере.
export function useFeedRowState(scope: string): { state: FeedRowState; actions: FeedRowActions } {
  const [stored, setStored] = useState(() => emptyRowState(scope))
  const state = stored.scope === scope ? stored : emptyRowState(scope)
  if (state !== stored) setStored(state)
  const actions = useMemo<FeedRowActions>(() => {
    const update = (change: (prev: FeedRowState) => FeedRowState): void => setStored((prev) => change(prev.scope === scope ? prev : emptyRowState(scope)))
    return {
      toggleSeries: (memberKeys) => update((prev) => toggleSeries(prev, memberKeys)),
      toggleReport: (key) => update((prev) => toggleReport(prev, key)),
      toggleRaw: (id) => update((prev) => toggleRaw(prev, id)),
      retryImage: (url) => update((prev) => bumpImageAttempt(prev, url)),
      sync: (live) => update((prev) => syncRowState(prev, live)),
    }
  }, [scope])
  return { state, actions }
}
