import { useRef } from 'react'
import { MeshApiError } from '../../api/mesh-client'

// isRejected — the server answered and refused the change (stale_revision among others): it was not applied,
// so there is nothing left to retry. A network error or a 5xx leaves the outcome unknown.
export function isRejected(err: unknown): boolean {
  return err instanceof MeshApiError && err.status >= 400 && err.status < 500
}

export type UnsettledChange<P> = {
  // freeze — the payload to send: the remembered unsettled change when it is the same operator action as `fresh`
  // (so it keeps its request id), otherwise `fresh`, which is then remembered.
  freeze: (fresh: P) => P
  // track — runs the request: a success or a refusal settles the remembered change, a network error / 5xx keeps it.
  track: <R>(request: () => Promise<R>) => Promise<R>
}

// useUnsettledChange — remembers the change sent last whose outcome is unknown (e.g. its response was lost), so
// the next attempt resends it unchanged with the same request id and the server answers a committed one with its
// receipt. Rebuilding it from what the screen shows now would make it a new change against whatever the world
// became meanwhile. `same` tells whether two payloads are the same operator action.
export function useUnsettledChange<P>(same: (a: P, b: P) => boolean): UnsettledChange<P> {
  const unsettledRef = useRef<P | null>(null)
  return {
    freeze(fresh) {
      const unsettled = unsettledRef.current
      const payload = unsettled !== null && same(unsettled, fresh) ? unsettled : fresh
      unsettledRef.current = payload
      return payload
    },
    async track(request) {
      try {
        const result = await request()
        unsettledRef.current = null
        return result
      } catch (err) {
        if (isRejected(err)) unsettledRef.current = null
        throw err
      }
    },
  }
}
