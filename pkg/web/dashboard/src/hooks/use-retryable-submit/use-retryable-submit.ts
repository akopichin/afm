import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { genMeshRequestId } from '../../api/mesh-client'

// SubmitHandlers — what happens once a request settles. Both run only while the attempt is still CURRENT:
// the component is mounted, the fence (selection) it started under has not changed and no newer attempt was
// started since. A late response therefore never re-selects an old conversation or clears another draft.
export type SubmitHandlers<R> = {
  onSuccess?: (result: R) => void
  onFailure?: (message: string) => void
}

export type RetryableSubmit<P> = {
  // pending — an attempt started under the current fence is in flight.
  pending: boolean
  // error — why the last attempt under the current fence failed; null after a success or a fence change.
  error: string | null
  // submit — sends `payload` with an idempotency key. A retry of the SAME payload after a failure reuses the
  // failed attempt's key (its response may have been lost after the server committed it); a changed payload
  // is a new intent and gets a new key. Ignored while an attempt under the same fence is in flight.
  submit: <R>(payload: P, send: (payload: Readonly<P>, requestId: string) => Promise<R>, handlers?: SubmitHandlers<R>) => void
}

type Attempt = { key: string; requestId: string }
type Running = { attempt: number; fence: string }

// errorMessage — the text a form shows for a failed request.
export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'request failed'
}

// removeSubmittedText — the draft after a successful send of `submitted`: the sent text goes, anything typed
// after it while the request was in flight stays. A draft edited elsewhere is left as it is.
export function removeSubmittedText(current: string, submitted: string): string {
  if (current === submitted) return ''
  if (submitted !== '' && current.startsWith(submitted)) return current.slice(submitted.length).replace(/^\s+/, '')
  return current
}

// useRetryableSubmit — the one mutation primitive for send-like forms (composers, Post task, Create
// conversation, Add run agent): idempotent retries, fenced completions and per-fence pending/error state.
// P — the request's intent as plain JSON data (strings, arrays): two payloads are the same intent when they
// serialize equally. fence — the selection the form currently works on (e.g. the addressed participant);
// changing it makes in-flight attempts stale. Forms that remount per selection can omit it.
export function useRetryableSubmit<P extends object>(requestIdPrefix: string, fence = ''): RetryableSubmit<P> {
  // failedRef — the last attempt that failed; its key is reused only when the same payload is retried.
  const failedRef = useRef<Attempt | null>(null)
  const latestAttemptRef = useRef(0)
  // runningRef mirrors `running` for the double-submit check, which must not wait for a re-render.
  const runningRef = useRef<Running | null>(null)
  const fenceRef = useRef(fence)
  const mountedRef = useRef(true)
  const [running, setRunningState] = useState<Running | null>(null)
  const [failure, setFailure] = useState<{ fence: string; message: string } | null>(null)

  useLayoutEffect(() => {
    fenceRef.current = fence
  }, [fence])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const submit = useCallback(
    <R,>(payload: P, send: (payload: Readonly<P>, requestId: string) => Promise<R>, handlers: SubmitHandlers<R> = {}): void => {
      const startedFence = fenceRef.current
      if (runningRef.current !== null && runningRef.current.fence === startedFence) return
      const snapshot: Readonly<P> = Object.freeze({ ...payload })
      const key = JSON.stringify(snapshot)
      const failed = failedRef.current
      const requestId = failed !== null && failed.key === key ? failed.requestId : genMeshRequestId(requestIdPrefix)
      const attempt = ++latestAttemptRef.current
      const isCurrent = (): boolean => mountedRef.current && fenceRef.current === startedFence && latestAttemptRef.current === attempt
      const setRunning = (next: Running | null): void => {
        runningRef.current = next
        setRunningState(next)
      }
      // settle — this attempt is over: stop showing it as pending, unless a newer attempt already took over.
      const settle = (): void => {
        if (mountedRef.current && runningRef.current?.attempt === attempt) setRunning(null)
      }

      setRunning({ attempt, fence: startedFence })
      setFailure(null)
      send(snapshot, requestId).then(
        (result) => {
          if (failedRef.current?.requestId === requestId) failedRef.current = null
          settle()
          if (isCurrent()) handlers.onSuccess?.(result)
        },
        (err: unknown) => {
          failedRef.current = { key, requestId }
          settle()
          if (!isCurrent()) return
          const message = errorMessage(err)
          setFailure({ fence: startedFence, message })
          handlers.onFailure?.(message)
        },
      )
    },
    [requestIdPrefix],
  )

  return {
    pending: running !== null && running.fence === fence,
    error: failure !== null && failure.fence === fence ? failure.message : null,
    submit,
  }
}
