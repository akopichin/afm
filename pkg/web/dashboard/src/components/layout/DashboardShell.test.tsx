import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'
import { DashboardShell } from './DashboardShell'

// Переводит matchMedia в «мобильный» режим (matches=true) для теста шторки.
function mockMobile(matches: boolean): void {
  window.matchMedia = ((query: string) => ({
    matches,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

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

describe('DashboardShell rail resizer', () => {
  afterEach(() => localStorage.clear())

  test('dragging the resizer changes the rail width (--rail-width) and persists it', () => {
    const body = renderShell()
    const resizer = screen.getByRole('separator', { name: /resize stages panel/i })

    // Мокаем левый край тела в 0, чтобы clientX == ширина.
    body.getBoundingClientRect = () => ({ left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => {} })

    fireEvent.mouseDown(resizer, { clientX: 240 })
    fireEvent.mouseMove(document, { clientX: 320 })
    fireEvent.mouseUp(document)

    expect(body.style.getPropertyValue('--rail-width')).toBe('320px')
    expect(localStorage.getItem('afm.railWidth')).toBe('320')
  })

  test('width is clamped and Arrow keys resize; double-click resets to default', () => {
    const body = renderShell()
    const resizer = screen.getByRole('separator', { name: /resize stages panel/i })
    body.getBoundingClientRect = () => ({ left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0, toJSON: () => {} })

    // Тянем далеко за максимум — клампится до 560.
    fireEvent.mouseDown(resizer, { clientX: 240 })
    fireEvent.mouseMove(document, { clientX: 9999 })
    fireEvent.mouseUp(document)
    expect(body.style.getPropertyValue('--rail-width')).toBe('560px')

    // Стрелка влево сужает на шаг.
    fireEvent.keyDown(resizer, { key: 'ArrowLeft' })
    expect(body.style.getPropertyValue('--rail-width')).toBe('544px')

    // Двойной клик сбрасывает к дефолту (240).
    fireEvent.doubleClick(resizer)
    expect(body.style.getPropertyValue('--rail-width')).toBe('240px')
  })
})

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

describe('DashboardShell rail drawer — mobile a11y (R2 #5)', () => {
  afterEach(() => {
    // Возвращаем desktop-заглушку из setup.ts.
    mockMobile(false)
  })

  function renderMobileShell(): { rail: HTMLElement; toggle: HTMLButtonElement } {
    const { container } = render(
      <DashboardShell
        rail={
          <div>
            <button type="button" className="stage-row" data-stage-id="s1">Stage one</button>
          </div>
        }
        tabs={<div>tabs</div>}
        workspace={<div>workspace</div>}
      />,
    )
    return {
      rail: container.querySelector('#rail-slot') as HTMLElement,
      toggle: screen.getByLabelText('Toggle stages') as HTMLButtonElement,
    }
  }

  test('closed drawer is inert on mobile; opening clears inert and focuses the first stage; closing returns focus to the toggle', () => {
    mockMobile(true)
    const { rail, toggle } = renderMobileShell()

    // Закрытая шторка на мобиле — inert (её кнопки вне tab-order).
    expect(rail.inert).toBe(true)

    // Открытие: inert снят, фокус на первой строке стадии.
    fireEvent.click(toggle)
    expect(rail.inert).toBe(false)
    expect(document.activeElement).toBe(rail.querySelector('.stage-row'))

    // Закрытие: inert снова, фокус вернулся на тумблер.
    fireEvent.click(toggle)
    expect(rail.inert).toBe(true)
    expect(document.activeElement).toBe(toggle)
  })

  test('on desktop the rail is never inert', () => {
    mockMobile(false)
    const { rail } = renderMobileShell()
    expect(rail.inert).toBe(false)
  })
})
