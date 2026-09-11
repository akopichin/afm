import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, it, expect } from 'vitest'
import { RunMetrics } from './RunMetrics'

// Управляемая заглушка matchMedia: запоминает listener'ы, чтобы тест мог
// сымитировать переход через breakpoint (resize) вручную.
function installControllableMatchMedia(initialMatches: boolean): {
  set: (matches: boolean) => void
  restore: () => void
} {
  const prev = window.matchMedia
  let matches = initialMatches
  const listeners = new Set<() => void>()
  window.matchMedia = ((query: string) => ({
    get matches() { return matches },
    media: query,
    onchange: null,
    addEventListener: (_: string, cb: () => void) => listeners.add(cb),
    removeEventListener: (_: string, cb: () => void) => listeners.delete(cb),
    addListener: (cb: () => void) => listeners.add(cb),
    removeListener: (cb: () => void) => listeners.delete(cb),
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
  return {
    set: (m: boolean) => { matches = m; listeners.forEach((cb) => cb()) },
    restore: () => { window.matchMedia = prev },
  }
}

describe('RunMetrics', () => {
  it('formats started clock and mm:ss durations', () => {
    render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={65_000} idleMs={5_000} backoffMs={0} />)
    // Started — локальные часы; проверяем формат HH:MM:SS без привязки к TZ.
    expect(document.getElementById('started-at')?.textContent).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    expect(document.getElementById('elapsed')).toHaveTextContent('01:05')
    expect(document.getElementById('idle')).toHaveTextContent('00:05')
    expect(document.getElementById('backoff')).toHaveTextContent('00:00')
  })

  it('uses h:mm:ss once past an hour', () => {
    render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={3_661_000} idleMs={0} backoffMs={0} />)
    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:01')
  })

  it('shows placeholders when the run has not started', () => {
    render(<RunMetrics startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)
    expect(document.getElementById('started-at')).toHaveTextContent('--')
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
    expect(document.getElementById('idle')).toHaveTextContent('--')
    expect(document.getElementById('backoff')).toHaveTextContent('--')
  })

  describe('R4 #3: the "…" popover closes when the viewport crosses to desktop', () => {
    let mm: ReturnType<typeof installControllableMatchMedia>
    afterEach(() => mm?.restore())

    it('opening the popover on a narrow header, then widening to desktop, closes it', () => {
      // Стартуем НЕ на desktop (matches=false для min-width:1280).
      mm = installControllableMatchMedia(false)
      render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={0} idleMs={0} backoffMs={0} />)

      // Открываем поповер «⋯».
      fireEvent.click(screen.getByRole('button', { name: /show all run metrics/i }))
      expect(screen.getByRole('group', { name: /all run metrics/i })).toBeInTheDocument()

      // Расширяем окно до desktop (>=1280) — поповер должен закрыться сам.
      act(() => mm.set(true))
      expect(screen.queryByRole('group', { name: /all run metrics/i })).toBeNull()
    })
  })
})
