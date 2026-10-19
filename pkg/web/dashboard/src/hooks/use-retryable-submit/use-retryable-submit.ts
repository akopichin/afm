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
  // submit — sends `payload` with an idempotency key. A retry of the SAME payload under the same fence after a
  // failure reuses the failed attempt's key (its response may have been lost after the server committed it); a
  // changed payload is a new intent and gets a new key. Ignored while an attempt under the same fence is in flight.
  submit: <R>(payload: P, send: (payload: Readonly<P>, requestId: string) => Promise<R>, handlers?: SubmitHandlers<R>) => void
}

// Unsettled — the request id of the latest attempt for one fence+payload that has not succeeded yet.
type Unsettled = { attempt: number; fence: string; requestId: string }
type Running = { attempt: number; generation: number }

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
  // unsettledRef — per fence+payload, the request id that is in flight or failed. Keeping one entry per intent
  // means a late failure of one selection's attempt never replaces the retry id of another selection's. A
  // success under a fence retires that fence's older intents: the user moved on, so sending an old text again
  // later is a new message, not a retry the server would answer with the old receipt.
  const unsettledRef = useRef(new Map<string, Unsettled>())
  const latestAttemptRef = useRef(0)
  // runningRef mirrors `running` for the double-submit check, which must not wait for a re-render.
  const runningRef = useRef<Running | null>(null)
  // selection — the fence plus a generation bumped on EVERY fence change, so a return to an earlier
  // selection (A → B → A) is a new selection: an attempt started under the first A is stale for the second.
  const [selection, setSelection] = useState({ fence, generation: 0 })
  if (selection.fence !== fence) setSelection({ fence, generation: selection.generation + 1 })
  const fenceRef = useRef(fence)
  const generationRef = useRef(selection.generation)
  const mountedRef = useRef(true)
  const [running, setRunningState] = useState<Running | null>(null)
  const [failure, setFailure] = useState<{ generation: number; message: string } | null>(null)

  useLayoutEffect(() => {
    fenceRef.current = selection.fence
    generationRef.current = selection.generation
  }, [selection])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const submit = useCallback(
    <R,>(payload: P, send: (payload: Readonly<P>, requestId: string) => Promise<R>, handlers: SubmitHandlers<R> = {}): void => {
      const startedFence = fenceRef.current
      const startedGeneration = generationRef.current
      if (runningRef.current !== null && runningRef.current.generation === startedGeneration) return
      const snapshot: Readonly<P> = Object.freeze({ ...payload })
      const key = JSON.stringify([startedFence, snapshot])
      const requestId = unsettledRef.current.get(key)?.requestId ?? genMeshRequestId(requestIdPrefix)
      const attempt = ++latestAttemptRef.current
      unsettledRef.current.set(key, { attempt, fence: startedFence, requestId })
      const isCurrent = (): boolean =>
        mountedRef.current && generationRef.current === startedGeneration && latestAttemptRef.current === attempt
      const setRunning = (next: Running | null): void => {
        runningRef.current = next
        setRunningState(next)
      }
      // settle — this attempt is over: stop showing it as pending, unless a newer attempt already took over.
      const settle = (): void => {
        if (mountedRef.current && runningRef.current?.attempt === attempt) setRunning(null)
      }

      setRunning({ attempt, generation: startedGeneration })
      setFailure(null)
      send(snapshot, requestId).then(
        (result) => {
          // Only the latest attempt for this intent forgets its id: an older one finishing must not drop the
          // id a newer, still-unsettled retry of the same payload relies on. For the same reason only older
          // intents of this fence are retired, never a newer one that may still be in flight.
          if (unsettledRef.current.get(key)?.attempt === attempt) unsettledRef.current.delete(key)
          for (const [other, entry] of unsettledRef.current) {
            if (entry.fence === startedFence && entry.attempt < attempt) unsettledRef.current.delete(other)
          }
          settle()
          if (isCurrent()) handlers.onSuccess?.(result)
        },
        (err: unknown) => {
          settle()
          if (!isCurrent()) return
          const message = errorMessage(err)
          setFailure({ generation: startedGeneration, message })
          handlers.onFailure?.(message)
        },
      )
    },
    [requestIdPrefix],
  )

  return {
    pending: running !== null && running.generation === selection.generation,
    error: failure !== null && failure.generation === selection.generation ? failure.message : null,
    submit,
  }
}
