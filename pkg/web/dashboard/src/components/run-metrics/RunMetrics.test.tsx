import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { RunMetrics, costReason, summarizeCoverageGroups } from './RunMetrics'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'

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

const unsupported: AccountingState = { supported: false }
// showMoney:true у денежных фикстур — тесты Est. cost написаны для случая, когда
// деньги показываются. showMoney:false отдельно проверяется ниже.
const okHealthy: AccountingState = { supported: true, health: 'ok', hasData: true, showMoney: true }
const okHealthyNoMoney: AccountingState = { supported: true, health: 'ok', hasData: true, showMoney: false }
const unavailableWithData: AccountingState = { supported: true, health: 'unavailable', hasData: true, showMoney: true }
const unavailableNoData: AccountingState = { supported: true, health: 'unavailable', hasData: false, showMoney: true }

function makeIssue(overrides: Partial<CoverageIssue> = {}): CoverageIssue {
  return {
    kind: 'unpriced',
    attribution: { kind: 'unknown' },
    phase: 'implementation',
    channel: 'claude',
    model: 'claude-x',
    reason: 'unknown model',
    count: 1,
    ...overrides,
  }
}

function makeCostSummary(displayCost: string): CostSummary {
  return {
    displayCost,
    coverage: 'full',
    estimatedCostUsd: 0,
    metered: 0,
    pricedInvocations: 0,
    unpriced: 0,
    unmetered: 0,
    models: [],
    uncachedInput: 0,
    cacheRead: 0,
    cacheWrite5m: 0,
    cacheWrite1h: 0,
    cacheWriteOther: 0,
    output: 0,
    reasoningOutput: 0,
    totalTokens: 0,
    cacheWriteTotal: 0,
    cacheHitRatio: null,
    phases: {},
  }
}

// Пропсы, общие для всех тестов, не завязанных на конкретную комбинацию
// coverageIssues/accounting — не увеличивать копипасту базовых времянок.
const baseTimeProps = {
  startedAt: '2026-07-29T10:00:00.000Z',
  elapsedMs: 65_000,
  idleMs: 5_000,
  backoffMs: 0,
}
const baseCostProps = {
  coverageIssues: [] as CoverageIssue[],
  accounting: unsupported,
  onOpenCost: () => {},
}

describe('RunMetrics', () => {
  it('formats started clock and mm:ss durations', () => {
    render(<RunMetrics {...baseTimeProps} {...baseCostProps} />)
    // Started — локальные часы; проверяем формат HH:MM:SS без привязки к TZ.
    expect(document.getElementById('started-at')?.textContent).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    expect(document.getElementById('elapsed')).toHaveTextContent('01:05')
    expect(document.getElementById('idle')).toHaveTextContent('00:05')
    expect(document.getElementById('backoff')).toHaveTextContent('00:00')
  })

  it('uses h:mm:ss once past an hour', () => {
    render(<RunMetrics startedAt="2026-07-29T10:00:00.000Z" elapsedMs={3_661_000} idleMs={0} backoffMs={0} {...baseCostProps} />)
    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:01')
  })

  it('shows placeholders when the run has not started', () => {
    render(<RunMetrics startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} {...baseCostProps} />)
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
      render(<RunMetrics {...baseTimeProps} elapsedMs={0} idleMs={0} {...baseCostProps} />)

      // Открываем поповер «⋯».
      fireEvent.click(screen.getByRole('button', { name: /show all run metrics/i }))
      expect(screen.getByRole('group', { name: /all run metrics/i })).toBeInTheDocument()

      // Расширяем окно до desktop (>=1280) — поповер должен закрыться сам.
      act(() => mm.set(true))
      expect(screen.queryByRole('group', { name: /all run metrics/i })).toBeNull()
    })
  })

  describe('Est. cost tile', () => {
    it('is a button that calls onOpenCost when clicked', () => {
      const onOpenCost = vi.fn()
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={onOpenCost}
        />,
      )
      const btn = screen.getByRole('button', { name: /est\. cost/i })
      expect(btn.tagName).toBe('BUTTON')
      expect(btn).toHaveTextContent('$1.23')
      fireEvent.click(btn)
      expect(onOpenCost).toHaveBeenCalledTimes(1)
    })

    it('shows an amber marker and a non-empty accessible reason for ok health with coverage issues', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[makeIssue()]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={() => {}}
        />,
      )
      const btn = screen.getByRole('button', { name: /est\. cost/i })
      expect(btn.querySelector('.metric-marker')).not.toBeNull()
      const describedBy = btn.getAttribute('aria-describedby')
      expect(describedBy).toBeTruthy()
      const reasonNode = document.getElementById(describedBy as string)
      expect(reasonNode?.textContent).toBeTruthy()
      expect(reasonNode?.textContent).toMatch(/coverage/i)
      expect(reasonNode?.textContent).not.toMatch(/storage|unavailable/i)
    })

    it.each([
      ['hasData=true', unavailableWithData],
      ['hasData=false', unavailableNoData],
    ])('shows a marker with a storage reason when health is unavailable and there are no issues (%s)', (_label, accounting) => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={accounting}
          onOpenCost={() => {}}
        />,
      )
      const btn = screen.getByRole('button', { name: /est\. cost/i })
      expect(btn.querySelector('.metric-marker')).not.toBeNull()
      const describedBy = btn.getAttribute('aria-describedby')
      const reasonNode = document.getElementById(describedBy as string)
      expect(reasonNode?.textContent).toBeTruthy()
      expect(reasonNode?.textContent).toMatch(/unavailable|storage|incomplete/i)
      expect(reasonNode?.textContent).not.toMatch(/coverage gaps/i)
    })

    it('composes both sentences when health is unavailable and coverage issues exist', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[makeIssue()]}
          accounting={unavailableWithData}
          onOpenCost={() => {}}
        />,
      )
      const btn = screen.getByRole('button', { name: /est\. cost/i })
      const describedBy = btn.getAttribute('aria-describedby')
      const reasonText = document.getElementById(describedBy as string)?.textContent ?? ''
      expect(reasonText).toMatch(/unavailable|storage|incomplete/i)
      expect(reasonText).toMatch(/coverage/i)
    })

    it('hides the Est. cost tile entirely when accounting is unsupported', () => {
      // accounting.enabled: false / AFM_ACCOUNTING=0 → /api/status omits the
      // accounting object → supported:false → no tile at all (not a dash tile):
      // the whole point of the display switch is that cost chrome disappears.
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={unsupported}
          onOpenCost={() => {}}
        />,
      )
      expect(screen.queryByRole('button', { name: /est\. cost/i })).toBeNull()
      expect(document.querySelector('.metric-cost')).toBeNull()
      // The other four metrics still render.
      expect(document.getElementById('elapsed')).not.toBeNull()
    })

    it('does not render the Est. cost tile inside the popover when unsupported', () => {
      installControllableMatchMedia(false) // narrow header → popover available
      render(
        <RunMetrics
          {...baseTimeProps}
          elapsedMs={0}
          idleMs={0}
          coverageIssues={[]}
          accounting={unsupported}
          onOpenCost={() => {}}
        />,
      )
      fireEvent.click(screen.getByRole('button', { name: /show all run metrics/i }))
      expect(screen.getByRole('group', { name: /all run metrics/i })).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /est\. cost/i })).toBeNull()
    })

    it('keeps all DOM ids unique across the inline and popover copies', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[makeIssue()]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={() => {}}
        />,
      )
      fireEvent.click(screen.getByRole('button', { name: /show all run metrics/i }))
      expect(screen.getByRole('group', { name: /all run metrics/i })).toBeInTheDocument()

      const ids = Array.from(document.querySelectorAll('[id]')).map((el) => el.id)
      expect(new Set(ids).size).toBe(ids.length)
    })

    it('activating Cost from the popover closes it, calls onOpenCost, and moves focus off the removed node', () => {
      const onOpenCost = vi.fn()
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[makeIssue()]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={onOpenCost}
        />,
      )
      const moreBtn = screen.getByRole('button', { name: /show all run metrics/i })
      fireEvent.click(moreBtn)
      const popover = screen.getByRole('group', { name: /all run metrics/i })
      const popoverCostBtn = within(popover).getByRole('button', { name: /est\. cost/i })

      fireEvent.click(popoverCostBtn)

      expect(onOpenCost).toHaveBeenCalledTimes(1)
      expect(screen.queryByRole('group', { name: /all run metrics/i })).toBeNull()
      expect(document.body.contains(popoverCostBtn)).toBe(false)
      expect(document.activeElement).toBe(moreBtn)
    })

    it('renders both Est. tokens and Est. cost when accounting is supported and show_money is on', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={() => {}}
        />,
      )
      expect(screen.getByRole('button', { name: /est\. tokens/i })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /est\. cost/i })).toBeInTheDocument()
    })

    it('shows only Est. tokens (not Est. cost) when show_money is off', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={okHealthyNoMoney}
          runCost={{ ...makeCostSummary('$1.23'), totalTokens: 1_234_567 }}
          onOpenCost={() => {}}
        />,
      )
      const tokensBtn = screen.getByRole('button', { name: /est\. tokens/i })
      expect(tokensBtn).toHaveTextContent('1.2M')
      expect(screen.queryByRole('button', { name: /est\. cost/i })).toBeNull()
    })

    it('Est. tokens shows a flat dash when there is no runCost', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={okHealthyNoMoney}
          onOpenCost={() => {}}
        />,
      )
      expect(screen.getByRole('button', { name: /est\. tokens/i })).toHaveTextContent('—')
    })

    it('hides Est. tokens too when accounting is unsupported', () => {
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={unsupported}
          onOpenCost={() => {}}
        />,
      )
      expect(screen.queryByRole('button', { name: /est\. tokens/i })).toBeNull()
    })

    it('inline activation does not touch popover state or focus', () => {
      const onOpenCost = vi.fn()
      render(
        <RunMetrics
          {...baseTimeProps}
          coverageIssues={[]}
          accounting={okHealthy}
          runCost={makeCostSummary('$1.23')}
          onOpenCost={onOpenCost}
        />,
      )
      fireEvent.click(screen.getByRole('button', { name: /est\. cost/i }))
      expect(onOpenCost).toHaveBeenCalledTimes(1)
      expect(screen.queryByRole('group', { name: /all run metrics/i })).toBeNull()
    })
  })
})

describe('costReason', () => {
  it('is empty when there is nothing to warn about', () => {
    expect(costReason([], unsupported)).toBe('')
    expect(costReason([], okHealthy)).toBe('')
  })

  it('includes a storage sentence when health is unavailable with no issues', () => {
    expect(costReason([], unavailableWithData)).toMatch(/unavailable|storage|incomplete/i)
  })

  it('includes a coverage sentence when there are issues', () => {
    const text = costReason([makeIssue()], okHealthy)
    expect(text).toMatch(/coverage/i)
    expect(text).not.toMatch(/storage|unavailable/i)
  })

  it('includes both sentences when both axes fire', () => {
    const text = costReason([makeIssue()], unavailableWithData)
    expect(text).toMatch(/unavailable|storage|incomplete/i)
    expect(text).toMatch(/coverage/i)
  })
})

describe('summarizeCoverageGroups', () => {
  it('shows all groups as-is when there are 3 or fewer', () => {
    const issues = [makeIssue({ model: 'model-a' }), makeIssue({ model: 'model-b' }), makeIssue({ model: 'model-c' })]
    const text = summarizeCoverageGroups(issues)
    const segments = text.split('; ')
    expect(segments).toHaveLength(3)
    expect(text).not.toMatch(/more groups/)
  })

  it('caps at 3 groups and folds the rest into "and K more groups (M invocations)"', () => {
    const issues = [
      makeIssue({ model: 'model-a', count: 1 }),
      makeIssue({ model: 'model-b', count: 2 }),
      makeIssue({ model: 'model-c', count: 3 }),
      makeIssue({ model: 'model-d', count: 4 }),
      makeIssue({ model: 'model-e', count: 5 }),
    ]
    const text = summarizeCoverageGroups(issues)
    const segments = text.split('; ')
    expect(segments).toHaveLength(4)
    expect(segments[3]).toBe('and 2 more groups (9 invocations)')
    expect(text).toContain('model-a')
    expect(text).toContain('model-b')
    expect(text).toContain('model-c')
    expect(text).not.toContain('model-d')
    expect(text).not.toContain('model-e')
  })
})
