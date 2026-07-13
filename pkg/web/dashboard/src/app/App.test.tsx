import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { App } from './App'

// Минимальный stub WebSocket — App открывает соединение при монтировании.
class StubWebSocket {
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null

  close() {
    /* no-op */
  }
}

describe('App', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', StubWebSocket)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  test('renders the flow name and auto-selects an active stage', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url

      if (url.includes('/api/status')) {
        return {
          ok: true,
          json: async () => ({
            flow_name: 'demo',
            stage_order: ['s1', 's2'],
            stage_names: { s1: 'Propose', s2: 'Plan' },
            stages: {
              s1: { status: 'running', updated_at: '' },
              s2: { status: 'pending', updated_at: '' },
            },
          }),
        } as Response
      }

      if (url.includes('/log')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/plan')) return { ok: true, text: async () => '' } as Response
      if (url.includes('/dialog')) return { ok: true, json: async () => [] } as Response

      return { ok: true, json: async () => [] } as Response
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('demo')).toBeInTheDocument()
    })

    // Автовыбор: s1 (running) выбран автоматически → заголовок панели деталей показывает 'Propose'.
    // 'Propose' также есть в списке стадий, поэтому проверяем именно заголовок деталей по id.
    await waitFor(() => {
      expect(document.getElementById('detail-title')).toHaveTextContent('Propose')
    })

    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
  })
})
