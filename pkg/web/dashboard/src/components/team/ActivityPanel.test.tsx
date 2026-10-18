import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { ActivityPanel } from './ActivityPanel'
import type { AfmEvent } from '../../types'

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
})
