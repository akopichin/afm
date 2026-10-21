import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { BoardLoadState } from './BoardLoadState'
import type { MeshBoard } from '../../hooks/use-mesh'

function board(status: MeshBoard['status'], reload: MeshBoard['reload'] = async () => {}): MeshBoard {
  return { entries: [], status, reload }
}

describe('BoardLoadState', () => {
  test('ready — shows the board content and no notice', () => {
    render(<BoardLoadState board={board('ready')}>content</BoardLoadState>)
    expect(screen.getByText('content')).toBeInTheDocument()
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  test('loading — a status notice instead of the content', () => {
    render(<BoardLoadState board={board('loading')}>content</BoardLoadState>)
    expect(screen.getByRole('status')).toHaveTextContent('Loading board')
    expect(screen.queryByText('content')).toBeNull()
  })

  test('error — an alert with Retry instead of the content', () => {
    const reload = vi.fn(async () => {})
    render(<BoardLoadState board={board('error', reload)}>content</BoardLoadState>)
    expect(screen.getByRole('alert')).toHaveTextContent('Board unavailable')
    expect(screen.queryByText('content')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('stale — the content stays, with a warning and Retry above it', () => {
    const reload = vi.fn(async () => {})
    render(<BoardLoadState board={board('stale', reload)}>content</BoardLoadState>)
    expect(screen.getByRole('alert')).toHaveTextContent('out of date')
    expect(screen.getByText('content')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })
})
