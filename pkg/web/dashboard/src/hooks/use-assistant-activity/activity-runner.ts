import { FlowApiError, getAssistantActivity } from '../../api/run-client'
import type { AssistantActivityPage } from '../../types/assistant-activity'
import {
  EMPTY_WINDOW,
  MAX_ACTIVITY_ENTRIES,
  PAGE_SIZE,
  growForward,
  growOlder,
  olderRoom,
  rememberOrder,
  tailWindow,
  windowFrom,
  type ActivityWindow,
  type OrderMemory,
} from './activity-window'

// loading — first page not back yet; unavailable — the server has no activity for this task (404);
// error — the first page failed and there is nothing to show.
export type ActivityStatus = 'loading' | 'ready' | 'unavailable' | 'error'

// Everything the runner publishes; `key` ties it to one (run, task) so a render in between never shows the
// previous selection's rows.
export type ActivityData = {
  key: string
  window: ActivityWindow
  status: ActivityStatus
  // The last refresh failed but the rows already loaded are kept.
  refreshFailing: boolean
  loadingOlder: boolean
}

export const initialData = (key: string): ActivityData => ({ key, window: EMPTY_WINDOW, status: 'loading', refreshFailing: false, loadingOlder: false })

// poll is the periodic refresh (silent after a 404); sync is an explicit catch-up that always tries.
export type ActivityControls = { poll: () => void; sync: () => void; loadOlder: () => void }

type Operation = 'tail' | 'older' | 'forward'

// A catch-up that is still behind after this many full pages starts over from the tail: the pages in between
// would be trimmed away anyway.
const MAX_FORWARD_PAGES = MAX_ACTIVITY_ENTRIES / PAGE_SIZE

// startActivityRunner owns the history of ONE (run, task): triggers only record what is wanted, and the pump runs
// one request at a time (tail, older, forward), applying each response to the window as it is then. stop drops
// every response still in flight — the stale-response guard for a task or run switch.
export function startActivityRunner(
  taskId: string,
  key: string,
  publish: (data: ActivityData) => void,
  liveIds: () => string[],
  memory?: OrderMemory,
): { controls: ActivityControls; stop: () => OrderMemory } {
  let cancelled = false
  let data: ActivityData = { ...initialData(key), window: windowFrom(memory, liveIds()) }
  let running: Operation | null = null
  // 404: the periodic poll stays quiet until an explicit sync.
  let unavailable = false
  let forwardStreak = 0
  const wanted: Record<Operation, boolean> = { tail: true, older: false, forward: false }

  const update = (patch: Partial<ActivityData>) => {
    if (cancelled) return
    data = { ...data, ...patch, loadingOlder: wanted.older || running === 'older' }
    publish(data)
  }

  const canLoadOlder = () => data.window.hasOlder && olderRoom(data.window) > 0

  const nextOperation = (): Operation | null => {
    if (wanted.tail) {
      wanted.tail = false
      wanted.forward = false // the tail read covers it
      return 'tail'
    }
    if (wanted.older) {
      wanted.older = false
      if (canLoadOlder()) return 'older'
      update({})
    }
    if (wanted.forward) {
      wanted.forward = false
      // No cursor to continue from (the server rejected it): start over from the tail.
      return data.window.after === null ? 'tail' : 'forward'
    }
    return null
  }

  const fetchPage = (operation: Operation): Promise<AssistantActivityPage> => {
    const { window } = data
    switch (operation) {
      case 'tail':
        return getAssistantActivity(taskId, { limit: PAGE_SIZE })
      case 'older':
        return getAssistantActivity(taskId, { limit: olderRoom(window), before: window.before })
      case 'forward':
        return getAssistantActivity(taskId, { limit: PAGE_SIZE, after: window.after ?? undefined })
    }
  }

  const apply = (operation: Operation, page: AssistantActivityPage, ringAtRequest: string[]) => {
    const { window } = data
    switch (operation) {
      case 'tail':
        forwardStreak = 0
        unavailable = false
        update({ window: tailWindow(window, page, ringAtRequest), status: 'ready', refreshFailing: false })
        return
      case 'older':
        update({ window: growOlder(window, page) })
        return
      case 'forward':
        forwardStreak = page.hasMore ? forwardStreak + 1 : 0
        if (page.hasMore) wanted[forwardStreak >= MAX_FORWARD_PAGES ? 'tail' : 'forward'] = true
        update({ window: growForward(window, page, liveIds(), ringAtRequest), refreshFailing: false })
        return
    }
  }

  // A failed read keeps the rows on screen: only 404 says "nothing here", and only 400 says the cursor is stale.
  const fail = (operation: Operation, error: unknown) => {
    if (error instanceof FlowApiError && error.status === 404) {
      unavailable = true
      update({ status: 'unavailable', refreshFailing: false })
      return
    }
    if (error instanceof FlowApiError && error.status === 400 && operation !== 'tail') {
      wanted.tail = true
      update({ window: { ...data.window, after: null } })
      return
    }
    update(data.status === 'ready' ? { refreshFailing: true } : { status: 'error' })
  }

  const run = async (operation: Operation) => {
    const ringAtRequest = operation === 'older' ? [] : liveIds()
    running = operation
    update({})
    try {
      const page = await fetchPage(operation)
      if (cancelled) return
      running = null
      apply(operation, page, ringAtRequest)
    } catch (error) {
      if (cancelled) return
      running = null
      fail(operation, error)
    }
  }

  let pumping = false
  const pump = async () => {
    if (pumping) return
    pumping = true
    try {
      for (let operation = nextOperation(); operation !== null && !cancelled; operation = nextOperation()) {
        await run(operation)
      }
    } finally {
      pumping = false
    }
  }

  const want = (operation: Operation) => {
    wanted[operation] = true
    update({})
    void pump()
  }
  const refresh = () => want('forward')

  void pump()
  return {
    controls: {
      poll: () => {
        if (!unavailable) refresh()
      },
      sync: () => {
        unavailable = false
        refresh()
      },
      loadOlder: () => {
        if (canLoadOlder()) want('older')
      },
    },
    stop: () => {
      cancelled = true
      return rememberOrder(data.window)
    },
  }
}
