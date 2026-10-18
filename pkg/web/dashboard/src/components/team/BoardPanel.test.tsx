import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { BoardPanel } from './BoardPanel'
import * as meshClient from '../../api/mesh-client'

describe('BoardPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('empty state when nothing is published', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({ entries: [] })
    render(<BoardPanel />)
    await waitFor(() => expect(screen.getByText('Nothing published to the board yet')).toBeInTheDocument())
  })

  test('renders every entry with its key, status, author and version', async () => {
    vi.spyOn(meshClient, 'getMeshBoard').mockResolvedValue({
      entries: [{ author_id: 'run:qa', key: 'qa:report', version: 2, status: 'ready', seq: 5, ts: '2026-10-18T00:00:00Z' }],
    })
    render(<BoardPanel />)
    await waitFor(() => expect(screen.getByText('qa:report')).toBeInTheDocument())
    expect(screen.getByText('ready')).toBeInTheDocument()
    expect(screen.getByText('run:qa')).toBeInTheDocument()
    expect(screen.getByText('v2')).toBeInTheDocument()
  })
})
