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

// Рейл-шторка — чистое клиентское состояние DashboardShell: тумблер ☰ открывает/закрывает, скрим/Escape
// закрывают, клик по строке стадии закрывает.

function renderShell(): HTMLElement {
  const { container } = render(
    <DashboardShell
      rail={
        <div>
          <div data-stage-id="s1" className="stage-item">
            Stage one
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

  test('dragging the resizer moves the rail width (--rail-width) by the pointer distance and persists it', () => {
    const body = renderShell()
    const resizer = screen.getByRole('separator', { name: /resize stages panel/i })

    fireEvent.mouseDown(resizer, { clientX: 240 })
    fireEvent.mouseMove(document, { clientX: 320 })
    fireEvent.mouseUp(document)

    // 300px default + 80px of drag.
    expect(body.style.getPropertyValue('--rail-width')).toBe('380px')
    expect(localStorage.getItem('afm.railWidth')).toBe('380')
    expect(resizer).toHaveAttribute('aria-valuenow', '380')
  })

  test('width is clamped and Arrow keys resize; double-click resets to default', () => {
    const body = renderShell()
    const resizer = screen.getByRole('separator', { name: /resize stages panel/i })

    // Тянем далеко за максимум — клампится до 560.
    fireEvent.mouseDown(resizer, { clientX: 240 })
    fireEvent.mouseMove(document, { clientX: 9999 })
    fireEvent.mouseUp(document)
    expect(body.style.getPropertyValue('--rail-width')).toBe('560px')

    // Стрелка влево сужает на шаг.
    fireEvent.keyDown(resizer, { key: 'ArrowLeft' })
    expect(body.style.getPropertyValue('--rail-width')).toBe('544px')

    // Двойной клик сбрасывает к дефолту (300) и сохраняет его.
    fireEvent.doubleClick(resizer)
    expect(body.style.getPropertyValue('--rail-width')).toBe('300px')
    expect(localStorage.getItem('afm.railWidth')).toBe('300')
  })

  test('the saved width survives a reload, and an out-of-range value falls back to the default', () => {
    localStorage.setItem('afm.railWidth', '420')
    const { unmount } = render(<DashboardShell rail={<div />} tabs={<div />} workspace={<div />} />)
    expect(document.querySelector<HTMLElement>('.dashboard-body')!.style.getPropertyValue('--rail-width')).toBe('420px')
    unmount()

    localStorage.setItem('afm.railWidth', '9999')
    render(<DashboardShell rail={<div />} tabs={<div />} workspace={<div />} />)
    expect(document.querySelector<HTMLElement>('.dashboard-body')!.style.getPropertyValue('--rail-width')).toBe('300px')
  })

  test('the rail width does not touch the People or Team widths', () => {
    renderShell()
    fireEvent.keyDown(screen.getByRole('separator', { name: /resize stages panel/i }), { key: 'ArrowRight' })
    expect(localStorage.getItem('afm.railWidth')).toBe('316')
    expect(localStorage.getItem('afm.peopleWidth')).toBeNull()
    expect(localStorage.getItem('afm.teamRosterWidth')).toBeNull()
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

  test('picking a stage closes the drawer', () => {
    const body = renderShell()
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(body.classList.contains('rail-open')).toBe(true)

    // Клик по самой строке стадии — выбор сделан → шторка закрывается.
    fireEvent.click(screen.getByText('Stage one'))
    expect(body.classList.contains('rail-open')).toBe(false)
  })

  test('TestRail_DrawerDoesNotCloseOnChildToggle: the helper-task branch toggle is carved out, a child row click still closes it', () => {
    const { container } = render(
      <DashboardShell
        rail={
          <div>
            <div data-stage-id="s1" className="stage-item">
              Stage one
              <span className="assistant-branch-toggle-wrap">
                <button type="button" className="assistant-toggle">Expand helper tasks</button>
              </span>
            </div>
            <div data-task-id="t1" className="assistant-task-item">
              Reviewer task
            </div>
          </div>
        }
        tabs={<div>tabs</div>}
        workspace={<div>workspace</div>}
      />,
    )
    const body = container.querySelector('.dashboard-body') as HTMLElement
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(body.classList.contains('rail-open')).toBe(true)


    // Переключатель ветки помощников — тоже carve-out: разворот/сворот не должен схлопывать шторку вместе с
    // собой, иначе пользователь терял бы шторку на каждый клик по ▾/▸.
    fireEvent.click(screen.getByText('Expand helper tasks'))
    expect(body.classList.contains('rail-open')).toBe(true)

    // Клик по самой дочерней строке помощника — выбор сделан → шторка закрывается, как и у обычной стадии.
    fireEvent.click(screen.getByText('Reviewer task'))
    expect(body.classList.contains('rail-open')).toBe(false)
  })
})

describe('DashboardShell rail drawer — mobile a11y', () => {
  afterEach(() => {
    // Возвращаем desktop-заглушку из setup.ts.
    mockMobile(false)
    localStorage.clear()
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

  test('the divider leaves the tab order on mobile, where the rail is a drawer', () => {
    mockMobile(true)
    renderMobileShell()
    const resizer = document.querySelector('.rail-resizer') as HTMLElement
    expect(resizer).toHaveAttribute('aria-hidden', 'true')
    expect(resizer).toHaveAttribute('tabindex', '-1')

    fireEvent.keyDown(resizer, { key: 'ArrowRight' })
    expect(localStorage.getItem('afm.railWidth')).toBeNull()
  })

  test('Tab wraps inside the open drawer instead of leaking to the workspace behind the scrim', () => {
    mockMobile(true)
    const { container } = render(
      <DashboardShell
        rail={
          <div>
            <button type="button" className="stage-row" data-stage-id="s1">Stage one</button>
            <button type="button" className="stage-row" data-stage-id="s2">Stage two</button>
          </div>
        }
        tabs={<div>tabs</div>}
        workspace={<div>workspace</div>}
      />,
    )
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    const [first, last] = Array.from(container.querySelectorAll<HTMLElement>('.stage-row')) as [HTMLElement, HTMLElement]
    expect(document.activeElement).toBe(first)

    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(first)

    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
  })

  test('before the first status (no stages yet) the open drawer takes focus itself and Tab cannot leave it', () => {
    mockMobile(true)
    const { container } = render(<DashboardShell rail={<div />} tabs={<div>tabs</div>} workspace={<div>workspace</div>} />)
    const rail = container.querySelector('#rail-slot') as HTMLElement

    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(document.activeElement).toBe(rail)

    expect(fireEvent.keyDown(rail, { key: 'Tab' })).toBe(false) // default prevented: focus stays in the drawer
    expect(fireEvent.keyDown(rail, { key: 'Tab', shiftKey: true })).toBe(false)
    expect(document.activeElement).toBe(rail)
  })

  test('on desktop the rail is never inert', () => {
    mockMobile(false)
    const { rail } = renderMobileShell()
    expect(rail.inert).toBe(false)
  })
})

describe('DashboardShell Feed focus', () => {
  afterEach(() => {
    mockMobile(false)
    localStorage.clear()
  })

  test('focus mode marks the layout without remounting the rail or the workspace, and keeps the rail width', () => {
    localStorage.setItem('afm.railWidth', '360')
    const shell = (feedFocus: boolean) => (
      <DashboardShell feedFocus={feedFocus} rail={<button type="button">rail</button>} tabs={<div>tabs</div>} workspace={<textarea aria-label="draft" />} />
    )
    const { container, rerender } = render(shell(false))
    const body = container.querySelector('.dashboard-body') as HTMLElement
    const rail = screen.getByRole('button', { name: 'rail' })
    const draft = screen.getByRole('textbox', { name: 'draft' })

    rerender(shell(true))
    expect(body).toHaveClass('dashboard-body--feed-focus')
    rerender(shell(false))
    expect(body).not.toHaveClass('dashboard-body--feed-focus')
    expect(screen.getByRole('button', { name: 'rail' })).toBe(rail)
    expect(screen.getByRole('textbox', { name: 'draft' })).toBe(draft)
    expect(body.style.getPropertyValue('--rail-width')).toBe('360px')
    expect(container.querySelector('#rail-slot')).toHaveProperty('inert', false)
  })

  test('entering focus mode closes the Stages drawer', () => {
    const { container, rerender } = render(<DashboardShell rail={<div />} tabs={<div />} workspace={<div />} />)
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    rerender(<DashboardShell feedFocus rail={<div />} tabs={<div />} workspace={<div />} />)
    expect(container.querySelector('.dashboard-body')).not.toHaveClass('rail-open')
  })

  test('reports the open Stages drawer, so F cannot change the layout under it', () => {
    mockMobile(true)
    const reported: boolean[] = []
    render(<DashboardShell onRailDrawerChange={(open) => reported.push(open)} rail={<div />} tabs={<div />} workspace={<div />} />)
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    fireEvent.click(screen.getByLabelText('Close stages'))
    expect(reported).toEqual([false, true, false])
  })

  test('an open rail on desktop is not a drawer', () => {
    const reported: boolean[] = []
    render(<DashboardShell onRailDrawerChange={(open) => reported.push(open)} rail={<div />} tabs={<div />} workspace={<div />} />)
    fireEvent.click(screen.getByLabelText('Toggle stages'))
    expect(reported).toEqual([false])
  })
})
