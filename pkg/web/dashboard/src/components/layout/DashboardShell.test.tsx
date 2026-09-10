import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { DashboardShell } from './DashboardShell'

// Рейл-шторка (<900px) — чистое клиентское состояние DashboardShell: тумблер ☰
// открывает/закрывает, скрим/Escape закрывают, клик по строке стадии закрывает
// (выбор сделан), клик по кебабу — НЕ закрывает (меню действий должно остаться).
// CSS-брейкпоинт (когда рейл реально становится слайд-овером) тестами не
// покрывается — это верстка; здесь проверяется только логика класса rail-open.

function renderShell(): HTMLElement {
  const { container } = render(
    <DashboardShell
      rail={
        <div>
          <div data-stage-id="s1" className="stage-item">
            Stage one
            <span className="stage-kebab-wrap">
              <button type="button" className="stage-kebab">More actions</button>
            </span>
          </div>
        </div>
      }
      tabs={<div>tabs</div>}
      workspace={<div>workspace</div>}
    />,
  )
  return container.querySelector('.dashboard-body') as HTMLElement
}

describe('DashboardShell rail drawer', () => {
  test('toggle button opens and closes the drawer', () => {
    const body = renderShell()
    const toggle = screen.getByLabelText('Toggle stages')

    expect(body.classList.contains('rail-open')).toBe(false)
    fireEvent.click(toggle)
    expect(body.classList.contains('rail-open')).toBe(true)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(toggle)
    expect(body.classList.contains('rail-open')).toBe(false)
  })

  test('scrim click closes the drawer', () => {
    const body = renderShell()
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(body.classList.contains('rail-open')).toBe(true)

    fireEvent.click(screen.getByLabelText('Close stages'))
    expect(body.classList.contains('rail-open')).toBe(false)
  })

  test('Escape closes the drawer', () => {
    const body = renderShell()
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(body.classList.contains('rail-open')).toBe(true)

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(body.classList.contains('rail-open')).toBe(false)
  })

  test('picking a stage closes the drawer, but the kebab does not', () => {
    const body = renderShell()
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(body.classList.contains('rail-open')).toBe(true)

    // Клик по кебабу внутри строки стадии НЕ закрывает шторку.
    fireEvent.click(screen.getByText('More actions'))
    expect(body.classList.contains('rail-open')).toBe(true)

    // Клик по самой строке стадии — выбор сделан → шторка закрывается.
    fireEvent.click(screen.getByText('Stage one'))
    expect(body.classList.contains('rail-open')).toBe(false)
  })
})
