import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { ActivityPanel } from './ActivityPanel'
import type { AfmEvent } from '../../types'
import { BudgetUiContext, NO_BUDGET_UI } from '../budget-dialog/budget-context'

const events: AfmEvent[] = [
  { type: 'mesh_participant', payload: { participant_id: 'run:qa' }, stageId: '', timestamp: '2026-10-18T00:00:00Z' },
  { type: 'stage_status_changed', payload: { stage_id: 'implement', status: 'running' }, stageId: 'implement', timestamp: '2026-10-18T00:00:01Z' },
]

describe('ActivityPanel', () => {
  test('filters to mesh_* events only', () => {
    render(<ActivityPanel events={events} />)
    expect(screen.getByText(/joined the mesh/i)).toBeInTheDocument()
  })

  test('empty hint when there is no mesh activity', () => {
    render(<ActivityPanel events={[events[1]!]} />)
    expect(screen.getByText('No mesh activity yet')).toBeInTheDocument()
  })

  test('shows budget notices of run agents but not of the run or a stage', () => {
    const notice = (scope: string, stageId: string): AfmEvent => ({
      type: 'budget_limit_reached', stageId, timestamp: '2026-10-18T00:00:02Z',
      payload: { kind: 'limit_reached', scope, metric: 'tokens', policy: 'hard', limit: { tokens: 1000, stop_policy: 'hard' }, spent: { tokens: 1000, money_nano_usd: 0 }, revision: 1 },
    })
    render(
      <BudgetUiContext.Provider value={{ ...NO_BUDGET_UI, enabled: true, names: new Map([['task:t1', 'Run agent QA']]) }}>
        <ActivityPanel events={[notice('task:t1', ''), notice('run', ''), notice('stage:implement', 'implement')]} />
      </BudgetUiContext.Provider>,
    )
    expect(screen.getAllByText(/Budget limit reached/)).toHaveLength(1)
    expect(screen.getByText(/Run agent QA/)).toBeInTheDocument()
  })
})
