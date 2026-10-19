// serial-poll — the one polling primitive for views that re-read server state on an interval (mesh thread,
// board, roster, stage discussion). Loads never overlap: a tick or a reload that arrives while a load is
// running is coalesced into ONE load right after it. Responses are therefore applied in request order, so an
// older response never replaces a newer one, and a cursor-based reader never reads the same page twice.

export type SerialPoll = {
  // now — load right away (e.g. after posting), or once more after the running load; resolves when done.
  now: () => Promise<void>
  // stop — no further loads; a running one sees isStopped() === true when its response arrives.
  stop: () => void
}

// startSerialPoll starts loading immediately and then every intervalMs. `load` handles its own errors and
// checks isStopped() before applying a response; a rejection is swallowed so the next tick still runs.
export function startSerialPoll(load: (isStopped: () => boolean) => Promise<void>, intervalMs: number): SerialPoll {
  let stopped = false
  let running: Promise<void> | null = null
  let queued: Promise<void> | null = null
  const isStopped = (): boolean => stopped

  function runOnce(): Promise<void> {
    const current = load(isStopped)
      .catch(() => {})
      .finally(() => {
        running = null
      })
    running = current
    return current
  }

  function now(): Promise<void> {
    if (stopped) return Promise.resolve()
    if (running === null) return runOnce()
    if (queued === null) {
      queued = running.then(() => {
        queued = null
        return stopped ? undefined : runOnce()
      })
    }
    return queued
  }

  void now()
  const timer = setInterval(() => void now(), intervalMs)

  return {
    now,
    stop: () => {
      stopped = true
      clearInterval(timer)
    },
  }
}
