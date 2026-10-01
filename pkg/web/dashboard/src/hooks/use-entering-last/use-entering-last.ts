import { useCallback, useState } from 'react'

// useEnteringLast — the feed entrance is deliberately minimal (owner decision):
// animate ONLY the last feed message, and ONLY when it differs from the
// previously-COMMITTED last message. `signature` is a STABLE CONTENT signature of
// the last row (stageId|kind|actor|text) — never the timestamp-bearing feed key —
// so a reconnect re-sync or history backfill that re-keys rows but leaves the
// newest message's content unchanged produces the SAME signature and animates
// nothing. A new live message changes the signature → animate. A byte-identical
// repeat of the current last does not differ → no animation. A resetToken (feed
// scope) change, and the first render, re-baseline silently. A null → non-null
// transition (the async initial /api/events load into an empty feed, or the first
// message of a fresh feed) is also a silent baseline, NOT an animation.
//
// CONCURRENT-SAFE: the baseline (last committed signature/token) and the animate
// flag live in React STATE, adjusted via the official "store info from previous
// renders" pattern — a conditional setState DURING render (NOT ref mutation). React
// guarantees this is safe under the concurrent renderer: if a speculative render is
// discarded/interrupted, its setState is discarded with it, so the committed
// baseline can never be corrupted by an aborted render (the failure mode a
// render-time ref mutation would have). The setState is conditional (only when the
// signature/token actually changed vs committed state), so there is no render loop;
// React re-runs this component with the new state before committing, no extra paint.
//
// The last row calls onEntered() from its animationend handler to clear the flag.
// If a newer message arrives first, the signature changes again and the flag
// re-arms for the new last; the old last loses the entrance class by position
// (FeedWorkspace only applies it to the current last row), so nothing replays.
type Baseline = { token: string | null | undefined; sig: string | null; animate: boolean }

export function useEnteringLast(
  signature: string | null,
  resetToken: string | null,
): { animate: boolean; onEntered: () => void } {
  // token starts `undefined` (distinct from any real token incl. null) so the first
  // render is always a silent baseline.
  const [base, setBase] = useState<Baseline>({ token: undefined, sig: null, animate: false })

  if (base.token !== resetToken) {
    // scope switch or first render → adopt current signature, animate nothing
    setBase({ token: resetToken, sig: signature, animate: false })
  } else if (signature !== base.sig) {
    const hadPrevious = base.sig !== null
    // Animate ONLY when an existing newest message is replaced by a different one
    // (a genuine live append). null -> non-null is the ASYNC initial history load
    // (useEventFeed starts events=[], then applies /api/events) or the very first
    // message in a fresh feed — adopt it silently, never animate.
    setBase({ token: resetToken, sig: signature, animate: signature !== null && hadPrevious })
  }

  // Functional update: clearing on animationend is independent of the latest render.
  const onEntered = useCallback(() => setBase((b) => (b.animate ? { ...b, animate: false } : b)), [])

  return { animate: base.animate, onEntered }
}
