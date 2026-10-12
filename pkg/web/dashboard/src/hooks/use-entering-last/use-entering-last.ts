import { useCallback, useState } from 'react'

// useEnteringLast animates only a last message that differs from the previous committed last message.
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
    // Animate ONLY when an existing newest message is replaced by a different one.
    setBase({ token: resetToken, sig: signature, animate: signature !== null && hadPrevious })
  }

  // Functional update: clearing on animationend is independent of the latest render.
  const onEntered = useCallback(() => setBase((b) => (b.animate ? { ...b, animate: false } : b)), [])

  return { animate: base.animate, onEntered }
}
