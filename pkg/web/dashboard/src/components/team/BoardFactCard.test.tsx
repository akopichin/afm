import { render, screen, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { MeshBoardEntry, MeshParticipant } from '../../api/mesh-client'
import { BoardFactCard } from './BoardFactCard'

const participants: MeshParticipant[] = [
  { id: 'run:t-1', kind: 'run_agent', scope: 'run', alias: 'qa', display_name: 'QA', reachable: true },
]

function entry(key: string, status: string, overrides: Partial<MeshBoardEntry> = {}): MeshBoardEntry {
  return { author_id: 'run:t-1', key, version: 1, status, seq: 1, ts: '2026-10-18T00:00:00Z', ...overrides }
}

describe('BoardFactCard', () => {
  test('shows the key, version, status, author and artifacts of a fact', () => {
    const fact = entry('qa:report', 'ready', { version: 3, artifact_refs: ['qa/out.txt'] })
    render(<BoardFactCard fact={fact} entries={[fact]} participants={participants} />)
    const card = screen.getByRole('article', { name: 'qa:report' })
    expect(within(card).getByText('v3')).toBeInTheDocument()
    expect(within(card).getByText('ready')).toBeInTheDocument()
    expect(within(card).getByText('QA')).toBeInTheDocument()
    expect(within(card).getByText('qa/out.txt')).toBeInTheDocument()
    expect(within(card).queryByRole('list', { name: 'Reports' })).toBeNull()
  })

  test('lists only the reports under its own key', () => {
    const fact = entry('qa:report', 'ready')
    const entries = [fact, entry('qa:report/qa', 'checked'), entry('other/qa', 'unrelated')]
    render(<BoardFactCard fact={fact} entries={entries} participants={participants} />)
    const reports = screen.getByRole('list', { name: 'Reports' })
    expect(within(reports).getAllByRole('listitem')).toHaveLength(1)
    expect(within(reports).getByText('checked')).toBeInTheDocument()
  })
})
