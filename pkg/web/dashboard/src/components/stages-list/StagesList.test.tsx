import { act, fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import type { Stage } from '../../types'
import { StagesList } from './StagesList'

describe('StagesList', () => {
  test('marks the selected stage active and calls onSelect on click', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const onSelect = vi.fn()

    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} />)

    const items = screen.getAllByRole('listitem')
    expect(items[0]).not.toHaveClass('active')
    expect(items[1]).toHaveClass('active')

    fireEvent.click(screen.getByText('Propose'))
    expect(onSelect).toHaveBeenCalledWith('s1')
  })

  test('marks awaiting_user_input stage with attention and a dialog badge', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'awaiting_user_input', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).toBeInTheDocument()
  })

  test('marks awaiting_approval stage with attention and an approval badge (not a dialog badge)', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.approval-badge')).toBeInTheDocument()
    expect(item.querySelector('.dialog-badge')).not.toBeInTheDocument()
  })

  // Регресс: .stage-item — грид из 3 колонок (18px 1fr auto). Бейдж и кебаб —
  // это ДВА трейлинг-элемента; если оба лежат прямо в гриде, кебаб не влезает в
  // третью (auto) колонку и переносится на новую неявную строку в 1-ю колонку —
  // «кебаб съезжает вниз-влево». Чиним, группируя бейджи+кебаб в единый слот
  // .stage-actions, чтобы у грида всегда было ровно 3 in-flow ребёнка.
  test('badge and kebab share one .stage-actions slot (grid does not overflow)', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    const actions = item.querySelector('.stage-actions')
    expect(actions).toBeInTheDocument()
    expect(actions?.parentElement).toBe(item)
    // и бейдж, и кебаб живут внутри общего слота — не отдельными детьми грида
    expect(actions?.querySelector('.approval-badge')).toBeInTheDocument()
    expect(actions?.querySelector('.stage-kebab')).toBeInTheDocument()
  })

  test('does not mark running stage with attention', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Run', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    const item = screen.getByRole('listitem')
    expect(item).not.toHaveAttribute('data-attention', 'true')
  })

  test('does not render stage-name element when name is empty', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} />)

    expect(screen.getByRole('listitem').querySelector('.stage-name')).not.toBeInTheDocument()
  })

  test('shows the kebab menu for running/awaiting_approval/planning/revising/retrying only', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(2) // a и c, не b
  })

  test('shows the kebab for planning/revising/retrying too, not just running/awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'planning', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'revising', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'c', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(3)
  })

  test('Pause menu item calls onPause; a running script stage shows no kebab at all (Pause is its only possible item, and it is gated out)', () => {
    const onPause = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={onPause} />)

    const buttons = screen.getAllByRole('button', { name: /more actions/i })
    expect(buttons).toHaveLength(1) // только stage a — у running-СКРИПТА (b) кебаба нет (пустое меню)
    fireEvent.click(buttons[0]!) // stage a: regular running stage
    expect(screen.getByText('Pause')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Pause'))
    expect(onPause).toHaveBeenCalledWith('a')
  })

  test('"Add note for agent" stays limited to running/awaiting_approval even though the kebab now also opens for planning/revising/retrying', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
  })

  test('"Add note for agent" is present on a regular running stage; a running script stage shows no kebab at all', () => {
    const onAddNote = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onAddNote={onAddNote} />)

    const buttons = screen.getAllByRole('button', { name: /more actions/i })
    expect(buttons).toHaveLength(1) // running СКРИПТОВАЯ стадия (b) кебаба не имеет — единственный её пункт (add-note) отфильтрован
    fireEvent.click(buttons[0]!) // обычная running-стадия — пункт есть
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()
  })

  test('pre-note: pending non-script stage shows the "Add note (before start)" item, calling onEditPreNote', () => {
    const onEditPreNote = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={onEditPreNote} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Add note (before start)'))
    expect(onEditPreNote).toHaveBeenCalledWith('a')
  })

  test('pre-note: item label becomes "Edit note" and a 📝 badge shows when a note is attached', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: 'учти лимиты', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={vi.fn()} />)

    expect(screen.getByTitle('Note attached for agent')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Edit note (before start)')).toBeInTheDocument()
  })

  test('no kebab for a running SCRIPT stage — every menu item is gated out, so an empty ⋮ must not show', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
  })

  test('pre-note: no kebab at all for a pending SCRIPT stage (no agent to note)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={vi.fn()} />)
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
  })

  test('CRITICAL: kebab menu portals to document.body so the scrollable #stages-panel cannot clip it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))

    const menuItem = screen.getByText('Add note for agent')
    const menu = menuItem.closest('ul')
    expect(menu).not.toBeNull()
    // #stages-panel has overflow-y: auto (layout.css) — any descendant that opens
    // below the visible viewport gets clipped. The menu must live outside it.
    expect(document.getElementById('stages-panel')?.contains(menu)).toBe(false)
    expect(document.body.contains(menu)).toBe(true)
  })

  test('clicking outside the open kebab menu closes it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
  })

  test('scrolling the stages panel closes the open kebab menu (the menu is anchored to a button inside it)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()

    fireEvent.scroll(document.getElementById('stages-panel')!)
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
  })

  // Регресс: раньше меню слушало скролл на window с capture:true и закрывалось
  // от ЛЮБОГО скролла в документе — в т.ч. от автоскролла ленты событий вниз
  // при новом событии (useStickToBottom дёргает scrollTop). Из-за этого меню
  // пропадало сразу после открытия, как только в фиде появлялось событие.
  // Скролл постороннего контейнера НЕ должен закрывать меню.
  test('scrolling an unrelated container (e.g. the event feed auto-scroll) does NOT close the open kebab menu', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Add note for agent')).toBeInTheDocument()

    // Отдельный скролл-контейнер вне #stages-panel — модель ленты событий.
    const feed = document.createElement('div')
    document.body.appendChild(feed)
    fireEvent.scroll(feed)

    expect(screen.getByText('Add note for agent')).toBeInTheDocument()
  })

  test('custom buttons: renders one menu item per button in declared order', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Zebra', 'Apple'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    const labels = screen.getAllByRole('button').map((b) => b.textContent)
    // Порядок объявления сохраняется: Zebra перед Apple.
    expect(labels).toContain('Zebra')
    expect(labels).toContain('Apple')
    expect(labels.indexOf('Zebra')).toBeLessThan(labels.indexOf('Apple'))
  })

  test('custom buttons: clicking one calls onButton with the label and closes the menu', () => {
    const onButton = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={onButton} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Run linter'))
    expect(onButton).toHaveBeenCalledWith('a', 'Run linter')
    // Меню закрылось после клика (тот же паттерн, что у остальных пунктов).
    expect(screen.queryByText('Run linter')).not.toBeInTheDocument()
  })

  test('custom buttons: gated by status — hidden on a running SCRIPT stage (no agent)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} />)

    // running СКРИПТ: add-note/buttons/pre-note/pause — все отфильтрованы, поэтому
    // кебаба нет вовсе (а значит и кнопки в нём).
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
    expect(screen.queryByText('Run linter')).not.toBeInTheDocument()
  })

  test('custom buttons: gated by status — not shown on a retrying stage (only running/awaiting_approval)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.queryByText('Run linter')).not.toBeInTheDocument()
  })

  test('custom buttons: sub-block and divider absent when buttons is empty', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const { baseElement } = render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(baseElement.querySelector('.stage-kebab-buttons')).toBeNull()
  })

  test('переход стадии в done навешивает one-shot класс just-done', async () => {
    const base: Stage[] = [
      { id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const { container, rerender } = render(<StagesList stages={base} selectedStageId={null} onSelect={() => {}} />)
    expect(container.querySelector('.stage-item.just-done')).toBeNull()

    const done: Stage[] = [{ ...base[0]!, status: 'done' }]
    rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} />)
    expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
  })

  test('just-done очищается через 700мс даже при промежуточном обновлении stages без нового перехода', () => {
    vi.useFakeTimers()
    try {
      const running: Stage[] = [{ id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }]
      const { container, rerender } = render(<StagesList stages={running} selectedStageId={null} onSelect={() => {}} />)
      const done: Stage[] = [{ ...running[0]!, status: 'done' }]
      act(() => { rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} />) })
      expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
      // промежуточное обновление stages (новый массив, без нового перехода) через 300мс
      act(() => { vi.advanceTimersByTime(300); rerender(<StagesList stages={[{ ...done[0]! }]} selectedStageId={null} onSelect={() => {}} />) })
      // к 700мс от перехода класс должен уйти
      act(() => { vi.advanceTimersByTime(500) })
      expect(container.querySelector('.stage-item.just-done')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
