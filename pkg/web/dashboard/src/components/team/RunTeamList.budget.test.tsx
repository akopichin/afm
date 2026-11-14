import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { MeshParticipant } from '../../api/mesh-client'
import type { BudgetView } from '../../types/budget'
import { BudgetUiContext, NO_BUDGET_UI } from '../budget-dialog/budget-context'
import { RunTeamList } from './RunTeamList'

const participants: MeshParticipant[] = [
  { id: 'user', kind: 'user', scope: 'run', reachable: true },
  { id: 'run:t1', kind: 'run_agent', scope: 'run', display_name: 'QA reviewer', reachable: true },
  { id: 'helper:t2', kind: 'helper', scope: 'stage', stage_id: 'implement', display_name: 'Correctness helper', reachable: true },
]

const view = (scope: string, over: Partial<BudgetView> = {}): BudgetView => ({
  scope, kind: 'helper', revision: 1, base: null, baseSource: 'none', override: { kind: 'none' }, effective: { tokens: 100, stopPolicy: 'hard' },
  spent: { tokens: 85, moneyNanoUsd: 0, unmetered: 0, unpriced: 0, notExplicit: 0 }, evaluation: { tokens: 'warning', money: '' },
  parentLimits: [], hold: null, draining: false, actions: [], ...over,
})

function renderWith(views: BudgetView[]) {
  render(
    <BudgetUiContext.Provider value={{ ...NO_BUDGET_UI, enabled: true, views: new Map(views.map((v) => [v.scope, v])) }}>
      <RunTeamList participants={participants} stages={[{ id: 'implement', status: 'running' }]} listView="people" onAddRunAgent={() => {}} />
    </BudgetUiContext.Provider>,
  )
}

describe('RunTeamList budget status', () => {
  test('helper and run agent rows show only a text status, with no budget button', () => {
    renderWith([view('task:t1', { kind: 'run_agent', hold: { reason: 'limit_reached', scope: 'task:t1' } }), view('task:t2')])
    expect(screen.getByText('Stopped by budget')).toBeInTheDocument()
    expect(screen.getByText('Budget 85%')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Edit budget/i })).toBeNull()
  })

  test('a scope under its limit adds no text', () => {
    renderWith([view('task:t2', { evaluation: { tokens: 'ok', money: '' }, spent: { tokens: 10, moneyNanoUsd: 0, unmetered: 0, unpriced: 0, notExplicit: 0 } })])
    expect(screen.queryByText(/Budget \d+%/)).toBeNull()
  })
})
