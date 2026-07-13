import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import type { Stage } from '../../types'
import { Footer } from './Footer'

describe('Footer', () => {
  test('renders done/total progress and formatted elapsed', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '' },
      { id: 's2', name: '', status: 'running', updatedAt: '' },
    ]

    render(<Footer stages={stages} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} />)

    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('01:05')).toBeInTheDocument()
  })

  test('shows placeholder elapsed when startedAt is empty', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} />)

    // При пустом startedAt оба поля (started-at и elapsed) показывают плейсхолдер '--' —
    // проверяем именно elapsed по его id.
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
  })
})
