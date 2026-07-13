import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { ConsumptionPanel } from './ConsumptionPanel'

describe('ConsumptionPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('hides the Cost metric when the cost probe returns empty', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => [],
    } as Response)

    render(<ConsumptionPanel stages={[]} />)

    // Cost скрыт, пока пробный запрос /api/usage?metric=cost пуст; обёртка waitFor
    // дожидается асинхронного резолва хуков внутри act() — без warning.
    await waitFor(() => {
      expect(screen.getByText('Cost')).toHaveClass('hidden')
      expect(screen.getByText('Tokens')).not.toHaveClass('hidden')
    })
  })

  test('shows the Cost metric when cost data is available', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => [{ timeBucket: '2026-07-10T10:00:00Z', value: 0.5 }],
    } as Response)

    render(<ConsumptionPanel stages={[]} />)

    await waitFor(() => {
      expect(screen.getByText('Cost')).not.toHaveClass('hidden')
    })
  })
})
