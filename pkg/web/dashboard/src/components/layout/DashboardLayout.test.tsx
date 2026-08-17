import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { DashboardLayout } from './DashboardLayout'

function renderLayout(overrides: Partial<Record<'plan' | 'dialog', null>>) {
  return render(
    <DashboardLayout
      stages={<div>STAGES</div>}
      stageHeader={<div>HEADER</div>}
      plan={overrides.plan === null ? null : <div>PLAN</div>}
      dialog={overrides.dialog === null ? null : <div>DIALOG</div>}
      feed={<div>FEED</div>}
    />,
  )
}

describe('DashboardLayout', () => {
  test('показывает plan и dialog по умолчанию', () => {
    const { container } = renderLayout({})
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    // 2 строки (plan+dialog) → 1 разделитель между ними.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(1)
  })

  test('скрывает plan, когда plan=null', () => {
    const { container } = renderLayout({ plan: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    // 1 строка (только dialog) → 0 разделителей.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(0)
  })

  test('скрывает dialog, когда dialog=null', () => {
    const { container } = renderLayout({ dialog: null })
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.queryByText('DIALOG')).toBeNull()
    // 1 строка (только plan) → 0 разделителей.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(0)
  })

  test('показывает empty-state, когда plan=null и dialog=null', () => {
    const { container } = renderLayout({ plan: null, dialog: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.queryByText('DIALOG')).toBeNull()
    // Нет строк вообще → нет resize-группы и разделителей (регрессия на старой
    // реализации: log-row монтировался всегда, разделителей было бы 0, но
    // сама Group с одной log-панелью существовала бы — здесь Group не рендерится).
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(0)
    expect(container.querySelector('.detail-rows')).toBeNull()
    expect(screen.getByText('Nothing to show for this stage')).toBeInTheDocument()
  })
})
