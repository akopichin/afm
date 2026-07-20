import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { SupervisorDecision } from './SupervisorDecision'

// Мокаем эндпоинт супервизора: null → 404 (решения нет), иначе {decision, reason}.
function mockFetch(decision: string | null, reason = ''): void {
  vi.spyOn(globalThis, 'fetch').mockImplementation(
    async () =>
      (decision == null
        ? { ok: false, json: async () => ({}) }
        : { ok: true, json: async () => ({ decision, reason }) }) as Response,
  )
}

describe('SupervisorDecision', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('ничего не рендерит, когда решения нет', async () => {
    mockFetch(null)
    const { container } = render(<SupervisorDecision stageId="s1" />)
    await waitFor(() => expect(container.querySelector('.supervisor-dot')).toBeNull())
  })

  test('рендерит точку с классом трека, когда решение есть', async () => {
    mockFetch('autonomous', 'reason text')
    const { container } = render(<SupervisorDecision stageId="s1" />)
    const dot = await waitFor(() => {
      const el = container.querySelector('.supervisor-dot')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    expect(dot).toHaveClass('autonomous')
  })

  test('клик открывает поповер с заголовком решения и причиной', async () => {
    mockFetch('autonomous', 'because prompt requires it')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByText('supervisor: autonomous')).toBeInTheDocument()
    expect(screen.getByText('because prompt requires it')).toBeInTheDocument()
  })

  test('поповер закрывается повторным кликом и по Escape', async () => {
    mockFetch('standard', 'reason')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(dot)
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  test('поповер закрывается по клику вне', async () => {
    mockFetch('standard', 'reason')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  test('при пустой причине поповер показывает только заголовок', async () => {
    mockFetch('autonomous', '')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByText('supervisor: autonomous')).toBeInTheDocument()
    expect(screen.getByRole('dialog').querySelector('.supervisor-popover-reason')).toBeNull()
  })
})
