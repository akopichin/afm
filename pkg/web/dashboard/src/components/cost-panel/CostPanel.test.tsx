import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { Stage } from '../../types'
import type { AccountingState, CostSummary, CoverageIssue } from '../../types/cost'
import { CostPanel } from './CostPanel'

// Минимальный валидный CostSummary для тестов — та же форма, что и в
// StagesList.test.tsx, чтобы не плодить разные соглашения по фикстурам.
function makeCost(overrides: Partial<CostSummary> = {}): CostSummary {
  return {
    displayCost: '$1.23',
    coverage: 'full',
    estimatedCostUsd: 1.23,
    metered: 1,
    pricedInvocations: 1,
    unpriced: 0,
    unmetered: 0,
    models: ['claude'],
    uncachedInput: 100,
    cacheRead: 200,
    cacheWrite5m: 30,
    cacheWrite1h: 20,
    cacheWriteOther: 0,
    output: 400,
    reasoningOutput: 0,
    totalTokens: 750, // 100 (uncached) + 200 (cache read) + 50 (cache write total) + 400 (output)
    cacheWriteTotal: 50,
    cacheHitRatio: 0.4,
    phases: { planning: 2, implementation: 1 },
    ...overrides,
  }
}

function makeStage(id: string, cost?: CostSummary, name = ''): Stage {
  return {
    id,
    name,
    status: 'done',
    updatedAt: '',
    interactive: false,
    autonomous: false,
    autoApprove: false,
    hasDialog: false,
    showPlan: false,
    showDialog: false,
    isScript: false,
    pausedFrom: '',
    preNote: '',
    buttons: [],
    cost,
  }
}

function makeIssue(overrides: Partial<CoverageIssue> = {}): CoverageIssue {
  return {
    kind: 'unpriced',
    attribution: { kind: 'stage', stageId: 'a stage' },
    phase: 'implementation',
    channel: 'claude',
    model: 'glm-5',
    reason: 'no rate configured',
    count: 3,
    ...overrides,
  }
}

const ACCOUNTING_UNSUPPORTED: AccountingState = { supported: false }
const ACCOUNTING_OK_NO_DATA: AccountingState = { supported: true, health: 'ok', hasData: false }
const ACCOUNTING_OK_DATA: AccountingState = { supported: true, health: 'ok', hasData: true }
const ACCOUNTING_UNAVAILABLE_NO_DATA: AccountingState = { supported: true, health: 'unavailable', hasData: false }
const ACCOUNTING_UNAVAILABLE_DATA: AccountingState = { supported: true, health: 'unavailable', hasData: true }

describe('CostPanel — empty-state order', () => {
  test('unavailable && !hasData → "Cost unavailable this run" (highest priority)', () => {
    render(
      <CostPanel
        stages={[]}
        coverageIssues={[]}
        accounting={ACCOUNTING_UNAVAILABLE_NO_DATA}
      />,
    )
    expect(screen.getByText('Cost unavailable this run')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  test('unsupported → "No usage data"', () => {
    render(<CostPanel stages={[]} coverageIssues={[]} accounting={ACCOUNTING_UNSUPPORTED} />)
    expect(screen.getByText('No usage data')).toBeInTheDocument()
  })

  test('supported+ok+!hasData → "No usage data"', () => {
    render(<CostPanel stages={[]} coverageIssues={[]} accounting={ACCOUNTING_OK_NO_DATA} />)
    expect(screen.getByText('No usage data')).toBeInTheDocument()
  })

  test('data present → renders the table, plus an incomplete banner when unavailable', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    render(
      <CostPanel
        stages={stages}
        runCost={makeCost({ displayCost: '$9.99' })}
        coverageIssues={[]}
        accounting={ACCOUNTING_UNAVAILABLE_DATA}
      />,
    )
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByText(/incomplete|unavailable/i)).toBeInTheDocument()
  })

  test('data present + health ok → no incomplete banner', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    render(
      <CostPanel
        stages={stages}
        runCost={makeCost()}
        coverageIssues={[]}
        accounting={ACCOUNTING_OK_DATA}
      />,
    )
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.queryByText(/incomplete/i)).not.toBeInTheDocument()
  })
})

describe('CostPanel — table + Total + expand', () => {
  test('renders only stages with cost, plus run overhead row and Total', () => {
    const stages = [
      makeStage('s1', makeCost(), 'Build'),
      makeStage('s2', undefined, 'No cost stage'),
    ]
    render(
      <CostPanel
        stages={stages}
        runCost={makeCost({ displayCost: '$5.00' })}
        runOverheadCost={makeCost({ displayCost: '$0.50' })}
        coverageIssues={[]}
        accounting={ACCOUNTING_OK_DATA}
      />,
    )
    expect(screen.getByText('Build')).toBeInTheDocument()
    expect(screen.queryByText('No cost stage')).not.toBeInTheDocument()
    expect(screen.getByText(/run overhead/i)).toBeInTheDocument()
    expect(screen.getByText('$0.50')).toBeInTheDocument()
    expect(screen.getByText('Total')).toBeInTheDocument()
    expect(screen.getByText('$5.00')).toBeInTheDocument()
  })

  test('Total falls back to — when runCost is absent', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    const totalRow = screen.getByText('Total').closest('tr')
    expect(totalRow).not.toBeNull()
    expect(totalRow).toHaveTextContent('—')
  })

  test('expanding a stage row and the overhead row are independent (ids __overhead__ and "a stage")', () => {
    const stages = [makeStage('__overhead__', makeCost(), 'Overhead-named stage'), makeStage('a stage', makeCost(), 'A stage')]
    render(
      <CostPanel
        stages={stages}
        runOverheadCost={makeCost({ displayCost: '$0.10' })}
        coverageIssues={[]}
        accounting={ACCOUNTING_OK_DATA}
      />,
    )

    const buttons = screen.getAllByRole('button')
    // Три expandable-строки: стадия "__overhead__", стадия "a stage", и сам run overhead.
    expect(buttons.length).toBe(3)

    for (const btn of buttons) {
      expect(btn).toHaveAttribute('aria-expanded', 'false')
    }

    // Раскрываем все три — каждая должна получить СВОЙ уникальный, резолвящийся
    // ровно в один элемент aria-controls (никакой коллизии между стадией с id
    // '__overhead__' и настоящей строкой run-overhead).
    const seenRegionIds = new Set<string>()
    for (const btn of buttons) {
      fireEvent.click(btn)
      expect(btn).toHaveAttribute('aria-expanded', 'true')
      const regionId = btn.getAttribute('aria-controls')
      expect(regionId).toBeTruthy()
      expect(seenRegionIds.has(regionId as string)).toBe(false)
      seenRegionIds.add(regionId as string)
      const region = document.getElementById(regionId as string)
      expect(region).not.toBeNull()
      expect(region).toHaveAttribute('role', 'region')
      // Каждый aria-controls резолвится РОВНО в один элемент.
      expect(document.querySelectorAll(`#${CSS.escape(regionId as string)}`).length).toBe(1)
    }

    // Сворачиваем стадию "a stage" обратно — overhead и __overhead__-стадия
    // остаются раскрытыми (независимость состояния).
    const aStageBtn = screen.getByRole('button', { name: /A stage/ })
    fireEvent.click(aStageBtn)
    expect(aStageBtn).toHaveAttribute('aria-expanded', 'false')
    const overheadBtn = screen.getByRole('button', { name: /run overhead/i })
    expect(overheadBtn).toHaveAttribute('aria-expanded', 'true')
  })

  test('detail region lives inside a td with colSpan=5', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Build/ }))
    const region = screen.getByRole('region')
    const td = region.closest('td')
    expect(td).not.toBeNull()
    expect(td?.getAttribute('colspan')).toBe('5')
    expect(region.tagName).not.toBe('TR')
  })
})

describe('CostPanel — token-mix bar', () => {
  test('segment widths sum to ~100% of totalTokens', () => {
    const cost = makeCost() // totalTokens = 750 = 100 + 200 + 50 + 400
    const stages = [makeStage('s1', cost, 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Build/ }))

    const bar = document.querySelector('.cost-tokenmix')
    expect(bar).not.toBeNull()
    const segments = bar?.querySelectorAll('.cost-tokenmix-seg') ?? []
    expect(segments.length).toBeGreaterThan(0)
    let sum = 0
    segments.forEach((seg) => {
      const width = (seg as HTMLElement).style.width
      sum += parseFloat(width)
    })
    expect(sum).toBeGreaterThan(99.9)
    expect(sum).toBeLessThan(100.1)
  })

  test('totalTokens===0 → neutral empty track, no NaN, no percentages', () => {
    const cost = makeCost({ totalTokens: 0, uncachedInput: 0, cacheRead: 0, cacheWrite5m: 0, cacheWrite1h: 0, cacheWriteOther: 0, cacheWriteTotal: 0, output: 0 })
    const stages = [makeStage('s1', cost, 'Empty')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Empty/ }))

    const bar = document.querySelector('.cost-tokenmix')
    expect(bar).not.toBeNull()
    expect(bar).toHaveClass('cost-tokenmix-empty')
    const segments = bar?.querySelectorAll('.cost-tokenmix-seg') ?? []
    expect(segments.length).toBe(0)
    expect(bar?.innerHTML).not.toMatch(/NaN/)
  })
})

describe('CostPanel — cache-hit ratio + figures', () => {
  test('cacheHitRatio null renders — (not 0%)', () => {
    const cost = makeCost({ cacheHitRatio: null })
    const stages = [makeStage('s1', cost, 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Build/ }))
    const region = screen.getByRole('region')
    expect(region).toHaveTextContent('—')
    expect(region).not.toHaveTextContent('0%')
  })

  test('cacheHitRatio number renders as a percentage', () => {
    const cost = makeCost({ cacheHitRatio: 0.4 })
    const stages = [makeStage('s1', cost, 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Build/ }))
    const region = screen.getByRole('region')
    expect(region).toHaveTextContent('40%')
  })

  test('phases are shown as ×count figures', () => {
    const cost = makeCost({ phases: { planning: 2, implementation: 5 } })
    const stages = [makeStage('s1', cost, 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    fireEvent.click(screen.getByRole('button', { name: /Build/ }))
    const region = screen.getByRole('region')
    expect(region).toHaveTextContent('×2')
    expect(region).toHaveTextContent('×5')
  })
})

describe('CostPanel — coverage line', () => {
  test('uses the shared bounded summary and shows ×count', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    const issues = [makeIssue({ count: 4 })]
    render(<CostPanel stages={stages} coverageIssues={issues} accounting={ACCOUNTING_OK_DATA} />)
    expect(screen.getByText(/×4/)).toBeInTheDocument()
  })

  test('no coverage line when there are no issues', () => {
    const stages = [makeStage('s1', makeCost(), 'Build')]
    render(<CostPanel stages={stages} coverageIssues={[]} accounting={ACCOUNTING_OK_DATA} />)
    expect(screen.queryByText(/coverage gaps/i)).not.toBeInTheDocument()
  })
})

describe('CostPanel — vertical scroll container', () => {
  test('a long stage list with several expanded rows still keeps Total in the DOM', () => {
    const stages = Array.from({ length: 30 }, (_, i) => makeStage(`s${i}`, makeCost(), `Stage ${i}`))
    render(
      <CostPanel
        stages={stages}
        runCost={makeCost({ displayCost: '$100.00' })}
        coverageIssues={[]}
        accounting={ACCOUNTING_OK_DATA}
      />,
    )
    // Раскрываем несколько строк.
    const buttons = screen.getAllByRole('button')
    fireEvent.click(buttons[0] as HTMLElement)
    fireEvent.click(buttons[5] as HTMLElement)
    fireEvent.click(buttons[10] as HTMLElement)

    expect(screen.getByText('Total')).toBeInTheDocument()
    expect(screen.getByText('$100.00')).toBeInTheDocument()
    expect(document.querySelector('.cost-panel-scroll')).not.toBeNull()
  })
})
