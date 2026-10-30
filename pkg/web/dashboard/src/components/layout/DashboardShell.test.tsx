import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'
import { StagesList } from '../stages-list/StagesList'
import type { Stage } from '../../types'
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
// закрывают, клик по строке стадии закрывает, клик по кебабу — НЕ закрывает.

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

  test('TestRail_DrawerDoesNotCloseOnKebabOrChildToggle: kebab AND the helper-task branch toggle are both carved out, a child row click still closes it', () => {
    const { container } = render(
      <DashboardShell
        rail={
          <div>
            <div data-stage-id="s1" className="stage-item">
              Stage one
              <span className="stage-kebab-wrap">
                <button type="button" className="stage-kebab">More actions</button>
              </span>
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

    // Кебаб — carve-out, как и раньше.
    fireEvent.click(screen.getByText('More actions'))
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

  test('the stage kebab menu is reachable from the keyboard inside the drawer; Escape closes only the menu', () => {
    mockMobile(true)
    const stage: Stage = { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }
    const { container } = render(
      <DashboardShell
        rail={<StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} onPause={() => {}} accounting={{ supported: false }} />}
        tabs={<div>tabs</div>}
        workspace={<div>workspace</div>}
      />,
    )
    const rail = container.querySelector('#rail-slot') as HTMLElement
    fireEvent.click(screen.getByLabelText('Toggle stages'))

    const kebab = screen.getByRole('button', { name: /more actions/i })
    fireEvent.click(kebab)
    const pause = screen.getByRole('button', { name: 'Pause' })
    expect(document.activeElement).toBe(pause)

    // Tab in the one-item menu stays in the menu; the drawer's own trap never sees it.
    expect(fireEvent.keyDown(pause, { key: 'Tab' })).toBe(false)
    expect(document.activeElement).toBe(pause)

    fireEvent.keyDown(pause, { key: 'Escape' })
    expect(screen.queryByRole('button', { name: 'Pause' })).not.toBeInTheDocument()
    expect(document.activeElement).toBe(kebab)
    expect(rail.inert).toBe(false) // the drawer is still open
  })

  test('on desktop the rail is never inert', () => {
    mockMobile(false)
    const { rail } = renderMobileShell()
    expect(rail.inert).toBe(false)
  })
})
