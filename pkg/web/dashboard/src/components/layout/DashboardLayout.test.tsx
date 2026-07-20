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
      log={<div>LOG</div>}
      feed={<div>FEED</div>}
    />,
  )
}

describe('DashboardLayout', () => {
  test('показывает все панели по умолчанию', () => {
    const { container } = renderLayout({})
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    expect(screen.getByText('LOG')).toBeInTheDocument()
    // 3 строки (plan+dialog+log) → 2 разделителя между ними.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(2)
  })

  test('скрывает plan, когда plan=null', () => {
    const { container } = renderLayout({ plan: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    expect(screen.getByText('LOG')).toBeInTheDocument()
    // 2 строки (dialog+log) → 1 разделитель. На старой безусловной реализации
    // (plan монтировался бы всегда) здесь было бы 2 — этот ассерт ловит регрессию.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(1)
  })

  test('скрывает dialog, когда dialog=null', () => {
    const { container } = renderLayout({ dialog: null })
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.queryByText('DIALOG')).toBeNull()
    expect(screen.getByText('LOG')).toBeInTheDocument()
    // 2 строки (plan+log) → 1 разделитель.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(1)
  })

  test('скрывает обе, когда plan=null и dialog=null', () => {
    const { container } = renderLayout({ plan: null, dialog: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.queryByText('DIALOG')).toBeNull()
    expect(screen.getByText('LOG')).toBeInTheDocument()
    // 1 строка (только log) → 0 разделителей. На старой реализации (обе панели
    // монтированы всегда) здесь было бы 2 — этот ассерт ловит регрессию.
    expect(container.querySelectorAll('.resize-handle-h')).toHaveLength(0)
  })
})
