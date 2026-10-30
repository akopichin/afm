import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { Stage, StageQuota } from '../../types'
import type { AccountingState, CostSummary } from '../../types/cost'
import type { StageAssistantsSummary } from '../../hooks/use-stage-assistants'
import type { AssistantTask } from '../../types/assistant-task'
import { StagesList } from './StagesList'

function makeTask(overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id: 't1', parentTaskId: null, profileId: 'reviewer', title: 'Reviewer', mode: 'advisory',
    trigger: 'one_shot', status: 'running', statusReason: '', depth: 1,
    watchPasses: null, watchLastCheckpointSeq: null, blocksRootCompletion: false, waived: false, waiverReason: '',
    ...overrides,
  }
}

function summaryOf(tasks: AssistantTask[]): StageAssistantsSummary {
  return {
    tasks,
    roots: tasks.filter((t) => t.parentTaskId === null).map((t) => ({ ...t, children: [] })),
    count: tasks.length,
    hasAttention: tasks.some((t) => t.status === 'paused'),
  }
}

// Минимальный валидный CostSummary для тестов рейла (значения токенов не
// проверяются — важен только displayCost/coverage).
function makeCost(overrides: Partial<CostSummary> = {}): CostSummary {
  return {
    displayCost: '$1.23',
    coverage: 'full',
    estimatedCostUsd: 1.23,
    metered: 1,
    pricedInvocations: 1,
    unpriced: 0,
    unmetered: 0,
    models: ['claude'],
    uncachedInput: 0,
    cacheRead: 0,
    cacheWrite5m: 0,
    cacheWrite1h: 0,
    cacheWriteOther: 0,
    output: 0,
    reasoningOutput: 0,
    totalTokens: 0,
    cacheWriteTotal: 0,
    cacheHitRatio: null,
    phases: {},
    ...overrides,
  }
}

// showMoney:true у этих фикстур — тесты стоимости в рейле написаны для случая,
// когда деньги показываются. ACCOUNTING_OK_NO_MONEY отдельно проверяет скрытие.
const ACCOUNTING_OK: AccountingState = { supported: true, health: 'ok', hasData: true, showMoney: true }
const ACCOUNTING_OK_NO_MONEY: AccountingState = { supported: true, health: 'ok', hasData: true, showMoney: false }
const ACCOUNTING_UNAVAILABLE: AccountingState = { supported: true, health: 'unavailable', hasData: true, showMoney: true }
const ACCOUNTING_UNSUPPORTED: AccountingState = { supported: false }

describe('StagesList', () => {
  test('renders rail progress (done / total) when progress props are given', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'A', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 's2', name: 'B', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} progressDone={1} progressTotal={2} accounting={{ supported: false }} />)
    expect(document.getElementById('progress-text')).toHaveTextContent('1 of 2 done')
    // One segment per stage: the done stage is green, the running one amber.
    const segments = Array.from(document.querySelectorAll('.rail-progress-seg'))
    expect(segments.map((seg) => seg.getAttribute('data-state'))).toEqual(['done', 'active'])
    expect(screen.getByRole('img', { name: '1 of 2 stages done' })).toBeInTheDocument()
  })

  // §14.1/TestProgress_AdvisoryDoesNotInflateDenominator: the rail's "N / M stages done" progress is computed
  // by the CALLER purely from the stages array (App.tsx: stages.filter(done).length / stages.length) — it
  // never reads useStageAssistants' summaries at all. Attaching any number of advisory (or required) helper
  // tasks to a stage must not change progressDone/progressTotal's rendered output, because the rail component
  // itself has no code path from `assistants` to `progress-text`/the progress segments.
  test('TestProgress_AdvisoryDoesNotInflateDenominator', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'A', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 's2', name: 'B', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    // A pile of advisory tasks (plus one required one) attached to s2 — none of this is "stage completion"
    // work and must not leak into the stage-level progress denominator.
    const manyAdvisory = summaryOf([
      makeTask({ id: 'adv-1', mode: 'advisory' }),
      makeTask({ id: 'adv-2', mode: 'advisory' }),
      makeTask({ id: 'adv-3', mode: 'advisory' }),
      makeTask({ id: 'req-1', mode: 'required' }),
    ])
    const { rerender } = render(
      <StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} progressDone={1} progressTotal={2} accounting={{ supported: false }} />,
    )
    expect(document.getElementById('progress-text')).toHaveTextContent('1 of 2 done')
    expect(document.querySelectorAll('.rail-progress-seg')).toHaveLength(2)

    rerender(
      <StagesList
        stages={stages}
        selectedStageId={null}
        onSelect={vi.fn()}
        progressDone={1}
        progressTotal={2}
        accounting={{ supported: false }}
        assistants={{ s2: manyAdvisory }}
      />,
    )
    // Same progressDone/progressTotal props in → identical rendered progress out, regardless of how many
    // helper tasks (of either mode) are attached to a stage.
    expect(document.getElementById('progress-text')).toHaveTextContent('1 of 2 done')
    expect(document.querySelectorAll('.rail-progress-seg')).toHaveLength(2)
  })

  test('omits rail progress when props are absent (0/0 stays safe)', () => {
    render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)
    expect(document.getElementById('progress-text')).toBeNull()
  })

  test('marks the selected stage active and calls onSelect on click', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const onSelect = vi.fn()

    render(<StagesList stages={stages} selectedStageId="s2" onSelect={onSelect} accounting={{ supported: false }} />)

    const items = screen.getAllByRole('listitem')
    expect(items[0]).not.toHaveClass('active')
    expect(items[1]).toHaveClass('active')

    fireEvent.click(screen.getByText('Propose'))
    expect(onSelect).toHaveBeenCalledWith('s1')
  })

  test('stage row is keyboard-accessible: a real button, focusable, selects on Enter/Space', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const onSelect = vi.fn()
    render(<StagesList stages={stages} selectedStageId={null} onSelect={onSelect} accounting={{ supported: false }} />)

    // Строка — настоящая <button> с доступным именем из содержимого (имя+статус).
    const row = screen.getByRole('button', { name: /Propose/i })
    expect(row.tagName).toBe('BUTTON')

    // Клавиатура: фокус ставится, Enter/Space активируют кнопку нативно.
    row.focus()
    expect(document.activeElement).toBe(row)
    // jsdom не синтезирует click из keydown, поэтому проверяем сам клик (то, что
    // браузер вызывает по Enter/Space на <button>), плюс что фокус реально лёг.
    fireEvent.click(row)
    expect(onSelect).toHaveBeenCalledWith('s1')
  })

  test('marks awaiting_user_input stage with attention and a dialog badge', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Propose', status: 'awaiting_user_input', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.dialog-badge')).toBeInTheDocument()
  })

  test('marks awaiting_approval stage with attention and an approval badge (not a dialog badge)', () => {
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)

    const item = screen.getByRole('listitem')
    expect(item).toHaveAttribute('data-attention', 'true')
    expect(item.querySelector('.approval-badge')).toBeInTheDocument()
    expect(item.querySelector('.dialog-badge')).not.toBeInTheDocument()
  })

  // Регресс: .stage-item — грид из 3 колонок. Чиним, группируя бейджи+кебаб в единый слот .stage-actions,
  // чтобы у грида всегда было ровно 3 in-flow ребёнка.
  test('badge and kebab share one .stage-actions slot (grid does not overflow)', () => {
    // awaiting_approval с кастомной кнопкой: есть и approval-бейдж, и кебаб. Без кнопок у
    // awaiting_approval кебаба нет — заметка живому агенту ушла в поле ленты, а Pause к нему неприменим.
    const stages: Stage[] = [
      { id: 's1', name: 'Plan', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)

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

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)

    const item = screen.getByRole('listitem')
    expect(item).not.toHaveAttribute('data-attention', 'true')
  })

  test('falls back to the stage id as the name when name is empty', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]

    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)

    // Заголовок стадии всегда есть (макет показывает имя жирным); при пустом
    // name показываем id.
    expect(screen.getByRole('listitem').querySelector('.stage-name')).toHaveTextContent('s1')
  })

  test('a row shows the stage name as its title and the status word under it, not the id', () => {
    const stages: Stage[] = [
      { id: 'impl', name: 'Build the checkout', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)
    const row = screen.getByRole('listitem')
    expect(row.querySelector('.stage-name')).toHaveTextContent('Build the checkout')
    expect(row.querySelector('.stage-status')).toHaveTextContent('Running')
    expect(row).not.toHaveTextContent('impl')
  })

  test('kebab shows for a pausable running stage; a bare awaiting_approval (no buttons) and a done stage have none', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      // awaiting_approval без кнопок: пункт «Add note for agent» убран (заметка — в поле ленты), Pause к
      // нему неприменим, pre-note только для pending → кебаба нет вовсе.
      { id: 'c', name: '', status: 'awaiting_approval', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(1) // только a (pausable)
  })

  test('shows the kebab for planning/revising/retrying too, not just running/awaiting_approval', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'planning', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'revising', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'c', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(screen.getAllByRole('button', { name: /more actions/i })).toHaveLength(3)
  })

  test('Pause menu item calls onPause; a running script stage shows no kebab at all (Pause is its only possible item, and it is gated out)', () => {
    const onPause = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
      { id: 'b', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={onPause} accounting={{ supported: false }} />)

    const buttons = screen.getAllByRole('button', { name: /more actions/i })
    expect(buttons).toHaveLength(1) // только stage a — у running-СКРИПТА (b) кебаба нет (пустое меню)
    fireEvent.click(buttons[0]!) // stage a: regular running stage
    expect(screen.getByText('Pause')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Pause'))
    expect(onPause).toHaveBeenCalledWith('a')
  })

  test('a stage parked waiting for a colleague (awaiting_peer) can be paused from the menu', () => {
    const onPause = vi.fn()
    const stages: Stage[] = [
      { id: 'p', name: '', status: 'awaiting_peer', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={onPause} accounting={{ supported: false }} />)
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Pause'))
    expect(onPause).toHaveBeenCalledWith('p')
  })

  test('the kebab no longer offers "Add note for agent" on a running stage — that moved to the feed composer', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={vi.fn()} accounting={{ supported: false }} />)
    // Кебаб у running-стадии есть (через Pause), но пункта заметки в нём нет.
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.queryByText('Add note for agent')).not.toBeInTheDocument()
    expect(screen.getByText('Pause')).toBeInTheDocument()
  })

  test('pre-note: pending non-script stage shows the "Add note (before start)" item, calling onEditPreNote', () => {
    const onEditPreNote = vi.fn()
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={onEditPreNote} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    fireEvent.click(screen.getByText('Add note (before start)'))
    expect(onEditPreNote).toHaveBeenCalledWith('a')
  })

  test('pre-note: item label becomes "Edit note" and a 📝 badge shows when a note is attached', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: 'учти лимиты', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={vi.fn()} accounting={{ supported: false }} />)

    expect(screen.getByTitle('Note attached for agent')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Edit note (before start)')).toBeInTheDocument()
  })

  test('no kebab for a running SCRIPT stage — every menu item is gated out, so an empty ⋮ must not show', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
  })

  test('pre-note: no kebab at all for a pending SCRIPT stage (no agent to note)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onEditPreNote={vi.fn()} accounting={{ supported: false }} />)
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
  })

  test('CRITICAL: kebab menu portals to document.body so the scrollable .stages-scroll cannot clip it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))

    const menuItem = screen.getByText('Pause')
    const menu = menuItem.closest('ul')
    expect(menu).not.toBeNull()
    // .stages-scroll has overflow-y: auto — any descendant that opens
    // below the visible viewport gets clipped. The menu must live outside it.
    expect(document.getElementById('stages-panel')?.contains(menu)).toBe(false)
    expect(document.body.contains(menu)).toBe(true)
  })

  test('keyboard: opening the kebab menu moves focus into it, Tab wraps inside, Escape closes it and returns focus to the kebab', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} onPause={vi.fn()} accounting={{ supported: false }} />)
    const kebab = screen.getByRole('button', { name: /more actions/i })

    fireEvent.click(kebab)
    const items = Array.from(document.querySelectorAll<HTMLElement>('.stage-kebab-menu button'))
    const first = items[0] as HTMLElement
    const last = items[items.length - 1] as HTMLElement
    expect(items.length).toBeGreaterThan(1)
    expect(document.activeElement).toBe(first)

    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)

    fireEvent.keyDown(last, { key: 'Escape' })
    expect(screen.queryByText('Pause')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(kebab)
  })

  test('clicking outside the open kebab menu closes it', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Pause')).toBeInTheDocument()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByText('Pause')).not.toBeInTheDocument()
  })

  test('closing the menu by scroll or by clicking an inert area returns focus to the kebab, so Tab stays in the rail', async () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onPause={vi.fn()} accounting={{ supported: false }} />)
    const kebab = screen.getByRole('button', { name: /more actions/i })

    fireEvent.click(kebab)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Pause' }))
    fireEvent.scroll(document.querySelector('.stages-scroll')!)
    expect(screen.queryByText('Pause')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(kebab)

    // Браузер после mousedown на нефокусируемой области роняет фокус на body.
    fireEvent.click(kebab)
    fireEvent.mouseDown(document.body)
    ;(document.activeElement as HTMLElement).blur()
    expect(screen.queryByText('Pause')).not.toBeInTheDocument()
    await waitFor(() => expect(document.activeElement).toBe(kebab))
  })

  test('scrolling the stages scroller closes the open kebab menu (the menu is anchored to a button inside it)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Pause')).toBeInTheDocument()

    fireEvent.scroll(document.querySelector('.stages-scroll')!)
    expect(screen.queryByText('Pause')).not.toBeInTheDocument()
  })

  // Регресс: раньше меню слушало скролл на window с capture:true и закрывалось от ЛЮБОГО скролла в
  // документе — в т.ч. Скролл постороннего контейнера НЕ должен закрывать меню.
  test('scrolling an unrelated container (e.g. the event feed auto-scroll) does NOT close the open kebab menu', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.getByText('Pause')).toBeInTheDocument()

    // Отдельный скролл-контейнер вне #stages-panel — модель ленты событий.
    const feed = document.createElement('div')
    document.body.appendChild(feed)
    fireEvent.scroll(feed)

    expect(screen.getByText('Pause')).toBeInTheDocument()
  })

  test('custom buttons: renders one menu item per button in declared order', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Zebra', 'Apple'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} accounting={{ supported: false }} />)

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
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={onButton} accounting={{ supported: false }} />)

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
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} accounting={{ supported: false }} />)

    // running СКРИПТ: add-note/buttons/pre-note/pause — все отфильтрованы, поэтому
    // кебаба нет вовсе (а значит и кнопки в нём).
    expect(screen.queryByRole('button', { name: /more actions/i })).not.toBeInTheDocument()
    expect(screen.queryByText('Run linter')).not.toBeInTheDocument()
  })

  test('custom buttons: gated by status — not shown on a retrying stage (only running/awaiting_approval)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: ['Run linter'] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.queryByText('Run linter')).not.toBeInTheDocument()
  })

  test('custom buttons: sub-block and divider absent when buttons is empty', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const { baseElement } = render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} onButton={vi.fn()} accounting={{ supported: false }} />)

    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(baseElement.querySelector('.stage-kebab-buttons')).toBeNull()
  })

  test('переход стадии в done навешивает one-shot класс just-done', async () => {
    const base: Stage[] = [
      { id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    const { container, rerender } = render(<StagesList stages={base} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.stage-item.just-done')).toBeNull()

    const done: Stage[] = [{ ...base[0]!, status: 'done' }]
    rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
  })

  test('just-done очищается через 700мс даже при промежуточном обновлении stages без нового перехода', () => {
    vi.useFakeTimers()
    try {
      const running: Stage[] = [{ id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }]
      const { container, rerender } = render(<StagesList stages={running} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
      const done: Stage[] = [{ ...running[0]!, status: 'done' }]
      act(() => { rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />) })
      expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
      // промежуточное обновление stages (новый массив, без нового перехода) через 300мс
      act(() => { vi.advanceTimersByTime(300); rerender(<StagesList stages={[{ ...done[0]! }]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />) })
      // к 700мс от перехода класс должен уйти
      act(() => { vi.advanceTimersByTime(500) })
      expect(container.querySelector('.stage-item.just-done')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  // --- Стоимость в рейле (railCost precedence, cost-ui increment 2) ---

  test('rail cost: running stage with a priced stage.cost shows the display cost, not the ellipsis placeholder', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [], cost: makeCost({ displayCost: '$4.56' }) },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    const rail = screen.getByRole('listitem').querySelector('.stage-cost')
    expect(rail).toHaveTextContent('$4.56')
    expect(rail).not.toHaveTextContent('…')
  })

  test('rail cost: cost==null + running + non-script + accounting ok shows the pending spinner (no ellipsis text)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    const rail = screen.getByRole('listitem').querySelector('.stage-cost')
    // Спиннер — не текст: старого «…» больше нет, но доступное предложение «Estimated cost pending»
    // остаётся для скринридеров, а сам спиннер скрыт от них, чтобы не озвучивался как отдельный узел.
    const spinner = rail?.querySelector('.stage-cost-spinner')
    expect(spinner).not.toBeNull()
    expect(spinner).toHaveAttribute('aria-hidden', 'true')
    expect(rail).not.toHaveTextContent('…')
    expect(rail).toHaveTextContent('Estimated cost pending')
  })

  test('rail cost: cost==null + running SCRIPT stage shows nothing (a script has no priced agent turn to estimate)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: true, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: cost==null + accounting unavailable shows nothing, even with data', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_UNAVAILABLE} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: retrying stage (cost==null) shows nothing — retrying is passive backoff, not an active agent turn', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'retrying', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: show_money off hides a priced stage.cost entirely (no $ leaks into the rail)', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [], cost: makeCost({ displayCost: '$4.56' }) },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK_NO_MONEY} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
    expect(screen.getByRole('listitem')).not.toHaveTextContent('$4.56')
  })

  test('rail cost: show_money off hides the pending spinner too', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK_NO_MONEY} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: unsupported accounting + running shows nothing', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_UNSUPPORTED} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: pending stage (cost==null) shows nothing regardless of accounting health', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    expect(screen.getByRole('listitem').querySelector('.stage-cost')).toBeNull()
  })

  test('rail cost: partial coverage adds ", partial pricing coverage" to the accessible description', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [], cost: makeCost({ displayCost: '$2.00', coverage: 'partial' }) },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    const row = screen.getByRole('button', { name: /^a/ })
    const describedBy = row.getAttribute('aria-describedby')
    expect(describedBy).not.toBeNull()
    expect(document.getElementById(describedBy!)).toHaveTextContent('Estimated cost $2.00, partial pricing coverage')
  })

  test('rail cost: coverage none renders "Estimated cost unavailable" as the accessible description', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [], cost: makeCost({ displayCost: '—', coverage: 'none' }) },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    const row = screen.getByRole('button', { name: /^a/ })
    const describedBy = row.getAttribute('aria-describedby')
    expect(describedBy).not.toBeNull()
    expect(document.getElementById(describedBy!)).toHaveTextContent('Estimated cost unavailable')
  })

  test('rail cost: an empty rail (no cost figure) leaves the stage row without aria-describedby', () => {
    const stages: Stage[] = [
      { id: 'a', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] },
    ]
    render(<StagesList stages={stages} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)

    expect(screen.getByRole('button', { name: /^a/ })).not.toHaveAttribute('aria-describedby')
  })

  // --- Version footer (pinned bottom-left of the rail) ---

  test('version footer: renders "afm <version>" in a .rail-foot element when version is given', () => {
    render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} version="v1.1.28" />)
    const foot = document.querySelector('.rail-foot')
    expect(foot).not.toBeNull()
    expect(foot).toHaveTextContent('afm v1.1.28')
  })

  test('version footer: renders nothing when version is empty or the prop is omitted', () => {
    const { rerender } = render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} version="" />)
    expect(document.querySelector('.rail-foot')).toBeNull()

    rerender(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)
    expect(document.querySelector('.rail-foot')).toBeNull()
  })

  // NOTE: jsdom does no layout, so this only proves the DOM ordering the pinning relies on. Actual
  // bottom-pinning + scroll-above behaviour is verified in a live run, not here.
  test('version footer: sits in the bottom block, the last child of #stages-panel after the scroll wrapper', () => {
    render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} version="dev" />)
    const panel = document.getElementById('stages-panel')
    expect(panel).not.toBeNull()
    const last = panel!.lastElementChild
    expect(last).toHaveClass('rail-bottom')
    expect(last!.lastElementChild).toHaveClass('rail-foot')
    expect(last).toHaveTextContent('afm dev')
  })

  test('the rail carries no Docker workspace label: the version is the only footer text', () => {
    render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} version="v2.0.0" />)
    const panel = document.getElementById('stages-panel')!
    expect(panel).not.toHaveTextContent(/docker/i)
    expect(document.querySelector('.rail-foot')).toHaveTextContent(/^afm v2\.0\.0$/)
  })

  test('a done stage draws its filled node with an SVG check, not a text glyph', () => {
    const done: Stage = { id: 'a', name: 'Build', status: 'done', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }
    render(<StagesList stages={[done]} selectedStageId={null} onSelect={vi.fn()} accounting={{ supported: false }} />)
    const check = document.querySelector('[data-stage-id="a"] .status-dot[data-status="done"] .dot-check')!
    expect(check.querySelector('svg path')).not.toBeNull()
    expect(check).toHaveAttribute('aria-hidden', 'true')
    expect(check.textContent).toBe('')
  })

  describe('verify badge (V5b.2, now durable stage.verify — Task 7)', () => {
    const stage: Stage = { id: 'a', name: 'Build', status: 'running', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }

    test('a stage with no verify entry shows no badge', () => {
      const { container } = render(<StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />)
      expect(container.querySelector('.verify-badge')).toBeNull()
    })

    test('an in-progress verify shows a "running" badge naming the step and command in its title', () => {
      const running: Stage = { ...stage, verify: { step: 2, command: 'codex', phase: 'running' } }
      const { container } = render(
        <StagesList stages={[running]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      const badge = container.querySelector('.verify-badge')
      expect(badge).not.toBeNull()
      expect(badge).toHaveAttribute('data-phase', 'running')
      expect(badge?.getAttribute('title')).toContain('2')
      expect(badge?.getAttribute('title')).toContain('codex')
    })

    test('a finished verify (pass/needs_changes/error) reflects the outcome via data-phase, not a new status', () => {
      const passing: Stage = { ...stage, verify: { step: 1, command: 'codex', phase: 'pass' } }
      const { container, rerender } = render(
        <StagesList stages={[passing]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      expect(container.querySelector('.verify-badge')).toHaveAttribute('data-phase', 'pass')

      const needsChanges: Stage = { ...stage, verify: { step: 1, command: 'codex', phase: 'needs_changes' } }
      rerender(
        <StagesList stages={[needsChanges]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      expect(container.querySelector('.verify-badge')).toHaveAttribute('data-phase', 'needs_changes')

      // Стадия по-прежнему просто "running" — verify не подменяет статус.
      expect(screen.getByRole('button', { name: /Build/i })).toHaveTextContent('Running')
    })

    test('accepted_stalemate (review_patience) renders a distinct data-phase badge with its own title', () => {
      const stalemate: Stage = { ...stage, verify: { step: 2, command: 'codex', phase: 'accepted_stalemate' } }
      const { container } = render(
        <StagesList stages={[stalemate]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      const badge = container.querySelector('.verify-badge')
      expect(badge).toHaveAttribute('data-phase', 'accepted_stalemate')
      expect(badge).toHaveAttribute('title', expect.stringContaining('accepted (stalemate)'))
    })

    test('a terminal stage never renders a pulsing "running" verify badge (stale phase is hidden)', () => {
      for (const status of ['done', 'failed'] as const) {
        const staleRunning: Stage = { ...stage, status, verify: { step: 1, command: 'codex', phase: 'running' } }
        const { container, unmount } = render(
          <StagesList stages={[staleRunning]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
        )
        expect(container.querySelector('.verify-badge[data-phase="running"]')).toBeNull()
        expect(container.querySelector('.verify-badge')).toBeNull()
        unmount()
      }
    })

    test('a running verify badge is hidden while the stage is not running (awaiting_quota/paused/retrying)', () => {
      for (const status of ['awaiting_quota', 'paused', 'retrying'] as const) {
        const parked: Stage = { ...stage, status, verify: { step: 1, command: 'codex', phase: 'running' } }
        const { container, unmount } = render(
          <StagesList stages={[parked]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
        )
        expect(container.querySelector('.verify-badge'), status).toBeNull()
        unmount()
      }
    })

    test('a terminal stage still shows a finished-outcome verify badge', () => {
      const donePass: Stage = { ...stage, status: 'done', verify: { step: 1, command: 'codex', phase: 'pass' } }
      const { container } = render(
        <StagesList stages={[donePass]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      expect(container.querySelector('.verify-badge')).toHaveAttribute('data-phase', 'pass')
    })

    test('a non-terminal stage with a running verify still pulses (behavior preserved)', () => {
      const running: Stage = { ...stage, status: 'running', verify: { step: 1, command: 'codex', phase: 'running' } }
      const { container } = render(
        <StagesList stages={[running]} selectedStageId={null} onSelect={() => {}} accounting={ACCOUNTING_OK} />,
      )
      expect(container.querySelector('.verify-badge[data-phase="running"]')).not.toBeNull()
    })

    test('pause/kebab actions remain available on a stage with a verify badge', () => {
      const running: Stage = { ...stage, status: 'running', verify: { step: 1, command: 'codex', phase: 'running' } }
      const onPause = vi.fn()
      render(
        <StagesList
          stages={[running]}
          selectedStageId={null}
          onSelect={() => {}}
          onPause={onPause}
          accounting={ACCOUNTING_OK}
        />,
      )
      fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
      fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
      expect(onPause).toHaveBeenCalledWith('a')
    })
  })

  // Этап 7, T7.9: the stage row's own status label/dot for awaiting_assistants, plus the attention escalation
  // that only fires when the stage's required gate actually needs a human decision.
  describe('awaiting_assistants row (T7.9)', () => {
    const stage: Stage = { id: 'a', name: 'QA gate', status: 'awaiting_assistants', updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [] }

    test('TestStageRow_ShowsAwaitingAssistants', () => {
      const { container } = render(<StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
      expect(screen.getByRole('button', { name: /QA gate/i })).toHaveTextContent('Awaiting helpers')
      expect(container.querySelector('.status-dot[data-status="awaiting_assistants"]')).not.toBeNull()
    })

    test('TestNeedsChanges_AppearsAsActionableAttention — blocked renders a visible row badge and attention marker, unblocked does not', () => {
      const working = stage // awaitingAssistantsBlocked absent/false — a helper is just still running
      const { container, rerender } = render(
        <StagesList stages={[working]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />,
      )
      expect(container.querySelector('.assistants-blocked-badge')).toBeNull()
      expect(container.querySelector('.stage-item')).not.toHaveAttribute('data-attention', 'true')
      expect(container.querySelector('.status-dot[data-blocked="true"]')).toBeNull()

      const blocked: Stage = { ...stage, awaitingAssistantsBlocked: true }
      rerender(<StagesList stages={[blocked]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
      expect(container.querySelector('.assistants-blocked-badge')).not.toBeNull()
      expect(container.querySelector('.stage-item')).toHaveAttribute('data-attention', 'true')
      expect(container.querySelector('.status-dot[data-blocked="true"]')).not.toBeNull()
    })
  })
})

describe('StagesList — helper-task child rows (T3.9)', () => {
  const stage: Stage = {
    id: 's1', name: 'Build', status: 'running', updatedAt: '', interactive: false, autonomous: false,
    autoApprove: false, hasDialog: false, showPlan: true, showDialog: false, isScript: false, pausedFrom: '',
    preNote: '', buttons: [],
  }

  test('a stage with helper tasks auto-expands: child rows render under the parent with no ordinal number', () => {
    const summary = summaryOf([makeTask({ id: 't1', title: 'Reviewer' })])
    const { container } = render(
      <StagesList stages={[stage]} selectedStageId="s1" onSelect={() => {}} accounting={{ supported: false }} assistants={{ s1: summary }} />,
    )
    const row = container.querySelector('[data-task-id="t1"]')
    expect(row).not.toBeNull()
    expect(row).toHaveClass('assistant-task-item')
    // No stage ordinal (.dot-num) anywhere inside the task row — it has its own, simpler status dot.
    expect(row?.querySelector('.dot-num')).toBeNull()
    expect(row?.querySelector('.assistant-task-status-dot')).not.toBeNull()
    expect(screen.getByText('Reviewer')).toBeInTheDocument()
  })

  test('child rows appear AFTER their parent stage in document order', () => {
    const summary = summaryOf([makeTask({ id: 't1' })])
    const { container } = render(
      <StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ s1: summary }} />,
    )
    const items = Array.from(container.querySelectorAll('#stages-list > li, #stages-list > [data-task-id]'))
    const stageIdx = items.findIndex((el) => el.getAttribute('data-stage-id') === 's1')
    const taskIdx = items.findIndex((el) => el.getAttribute('data-task-id') === 't1')
    expect(stageIdx).toBeGreaterThanOrEqual(0)
    expect(taskIdx).toBeGreaterThan(stageIdx)
  })

  test('collapsing the branch hides the rows and shows an aggregate (count + attention flag)', () => {
    const summary = summaryOf([makeTask({ id: 't1', status: 'paused' }), makeTask({ id: 't2', status: 'running' })])
    render(
      <StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ s1: summary }} />,
    )
    // Auto-expanded on first sight — collapse it.
    fireEvent.click(screen.getByRole('button', { name: /collapse helper tasks for s1/i }))

    expect(screen.queryByText('Reviewer')).not.toBeInTheDocument()
    expect(document.querySelector('[data-task-id="t1"]')).toBeNull()
    const aggregate = document.querySelector('.assistant-branch-aggregate')
    expect(aggregate).toHaveTextContent('2')
    expect(aggregate).toHaveAttribute('data-attention', 'true')
  })

  test('no aggregate badge is shown while the branch is expanded', () => {
    const summary = summaryOf([makeTask({ id: 't1' })])
    render(
      <StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ s1: summary }} />,
    )
    expect(document.querySelector('.assistant-branch-aggregate')).toBeNull()
  })

  test('TestSelection_ChildSelectable: clicking a child row calls onSelectTask with (stageId, taskId)', () => {
    const summary = summaryOf([makeTask({ id: 't1' })])
    const onSelectTask = vi.fn()
    render(
      <StagesList
        stages={[stage]}
        selectedStageId={null}
        onSelect={() => {}}
        accounting={{ supported: false }}
        assistants={{ s1: summary }}
        onSelectTask={onSelectTask}
      />,
    )
    fireEvent.click(screen.getByText('Reviewer'))
    expect(onSelectTask).toHaveBeenCalledWith('s1', 't1')
  })

  test('selectedTaskId marks the matching child row active', () => {
    const summary = summaryOf([makeTask({ id: 't1' }), makeTask({ id: 't2', title: 'Second' })])
    render(
      <StagesList
        stages={[stage]}
        selectedStageId="s1"
        onSelect={() => {}}
        accounting={{ supported: false }}
        assistants={{ s1: summary }}
        selectedTaskId="t2"
      />,
    )
    expect(document.querySelector('[data-task-id="t1"]')).not.toHaveClass('active')
    expect(document.querySelector('[data-task-id="t2"]')).toHaveClass('active')
  })

  test('a stage with no helper tasks renders no branch toggle and no child rows (prop omitted entirely — existing callers unaffected)', () => {
    const { container } = render(
      <StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />,
    )
    expect(container.querySelector('.assistant-branch-toggle-wrap')).toBeNull()
    expect(container.querySelector('.assistant-task-item')).toBeNull()
  })

  test('keyboard: the child row is a real, focusable <button>; the expand toggle is a sibling, never nested inside .stage-row or .assistant-task-row (no nested interactive elements)', () => {
    const summary = summaryOf([makeTask({ id: 't1' }), makeTask({ id: 't2', title: 'Second', status: 'waiting_parent' })])
    const { container } = render(
      <StagesList stages={[stage]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ s1: summary }} />,
    )
    const taskRow = screen.getByRole('button', { name: /^Reviewer/i })
    expect(taskRow.tagName).toBe('BUTTON')
    expect(taskRow.className).toContain('assistant-task-row')
    taskRow.focus()
    expect(document.activeElement).toBe(taskRow)

    // No interactive element nested inside .stage-row or .assistant-task-row (a <button> inside a <button>
    // is invalid HTML and breaks keyboard activation) — the branch toggle and any per-task expand toggle are
    // always siblings, never descendants, of the row button.
    expect(container.querySelectorAll('.stage-row button')).toHaveLength(0)
    expect(container.querySelectorAll('.assistant-task-row button')).toHaveLength(0)
  })
})

describe('StagesList — usage-limit wait (awaiting_quota)', () => {
  const NOW = new Date('2026-10-23T16:50:00Z')
  const baseStage: Stage = {
    id: 'a', name: 'Build', status: 'awaiting_quota', updatedAt: '', interactive: false, autonomous: false, autoApprove: false,
    hasDialog: false, showPlan: false, showDialog: false, isScript: false, pausedFrom: '', preNote: '', buttons: [],
  }
  const quota: StageQuota = {
    provider: 'claude', window: 'five_hour', source: 'structured', until: '2026-10-23T17:02:06Z', resetAt: '2026-10-23T17:02:00Z', pauseReason: '',
  }
  const waiting: Stage = { ...baseStage, quota }

  afterEach(() => {
    vi.useRealTimers()
  })

  test('the row says "Usage limit" in its status line and accessible name, with a ticking countdown badge', () => {
    vi.useFakeTimers({ now: NOW })
    const { container } = render(<StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.stage-status')).toHaveTextContent('Usage limit')
    expect(container.querySelector('.stage-item')).toHaveAttribute('data-status', 'awaiting_quota')
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('12m 06s')
    act(() => { vi.advanceTimersByTime(1000) })
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('12m 05s')
  })

  test('awaiting_quota is machine work: no human-attention marker on the row', () => {
    const { container } = render(<StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.stage-item')).not.toHaveAttribute('data-attention')
  })

  test('the countdown freezes while /api/status is stale (statusAvailable=false)', () => {
    vi.useFakeTimers({ now: NOW })
    const { container, rerender } = render(
      <StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} statusAvailable />,
    )
    act(() => { vi.advanceTimersByTime(2000) })
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('12m 04s')
    rerender(<StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} statusAvailable={false} />)
    act(() => { vi.advanceTimersByTime(20_000) })
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('12m 04s')
  })

  test('after zero the badge says "Resuming…"', () => {
    vi.useFakeTimers({ now: NOW })
    const soon: Stage = { ...waiting, quota: { ...quota, until: '2026-10-23T16:50:01Z' } }
    const { container } = render(<StagesList stages={[soon]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    act(() => { vi.advanceTimersByTime(1000) })
    expect(container.querySelector('.quota-countdown')).toHaveTextContent('Resuming…')
  })

  test('probe source and a quota with no time: the rail never shows a misleading countdown', () => {
    vi.useFakeTimers({ now: NOW })
    const noTime: Stage = { ...waiting, quota: { ...quota, source: 'text', until: '', resetAt: '' } }
    const { container } = render(<StagesList stages={[noTime]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.quota-countdown')).toBeNull()
  })

  test('a stage paused from a quota wait (pause reason set) shows no countdown badge: nothing is scheduled', () => {
    const paused: Stage = { ...baseStage, status: 'paused', pausedFrom: 'awaiting_quota', quota: { ...quota, pauseReason: 'exceeds_max_wait' } }
    const { container } = render(<StagesList stages={[paused]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} />)
    expect(container.querySelector('.quota-badge')).toBeNull()
  })

  test('Pause is offered while waiting (and Continue is not a rail action)', () => {
    const onPause = vi.fn()
    render(<StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} onPause={onPause} accounting={{ supported: false }} />)
    fireEvent.click(screen.getByRole('button', { name: /more actions/i }))
    expect(screen.queryByText('Continue')).toBeNull()
    fireEvent.click(screen.getByText('Pause'))
    expect(onPause).toHaveBeenCalledWith('a')
  })

  test('a helper queued on a usage-limit park reads "Usage limit" (label, title and amber dot), not "Queued"', () => {
    const helper = makeTask({ id: 'h1', title: 'Reviewer', status: 'queued', quota })
    const { container } = render(
      <StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ a: summaryOf([helper]) }} />,
    )
    const row = container.querySelector('.assistant-task-row') as HTMLElement
    expect(row).toHaveAttribute('title', 'Usage limit')
    expect(row.querySelector('.assistant-task-status-sr')).toHaveTextContent('Usage limit')
    expect(container.querySelector('.assistant-task-status-dot')).toHaveAttribute('data-quota', 'true')
    // обычный queued без quota остаётся «Queued»
    const plain = makeTask({ id: 'h2', title: 'Plain', status: 'queued' })
    const second = render(
      <StagesList stages={[waiting]} selectedStageId={null} onSelect={() => {}} accounting={{ supported: false }} assistants={{ a: summaryOf([plain]) }} />,
    )
    expect(second.container.querySelector('.assistant-task-row')).toHaveAttribute('title', 'Queued')
  })
})

describe('StagesList: кнопка «Add stage» зависит от capability add_stage', () => {
  function renderRail(props: { canAddStage?: boolean; onAddStage?: () => void }) {
    render(<StagesList stages={[]} selectedStageId={null} onSelect={vi.fn()} accounting={ACCOUNTING_UNSUPPORTED} {...props} />)
  }

  test.each([
    ['canAddStage=false', { canAddStage: false }],
    ['canAddStage не передан', {}],
  ])('%s: кнопки нет', (_name, props) => {
    renderRail({ ...props, onAddStage: vi.fn() })
    expect(screen.queryByRole('button', { name: 'Add stage' })).not.toBeInTheDocument()
  })

  test('canAddStage=true: кнопка видна, клик вызывает onAddStage один раз', () => {
    const onAddStage = vi.fn()
    renderRail({ canAddStage: true, onAddStage })
    fireEvent.click(screen.getByRole('button', { name: 'Add stage' }))
    expect(onAddStage).toHaveBeenCalledTimes(1)
  })
})
