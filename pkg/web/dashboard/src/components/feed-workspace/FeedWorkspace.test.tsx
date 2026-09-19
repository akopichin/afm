import { render, screen, fireEvent, within, act } from '@testing-library/react'
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { FeedWorkspace } from './FeedWorkspace'

const ev = (type: string, payload: unknown, stageId: string, timestamp: string): AfmEvent =>
  ({ type, payload, stageId, timestamp }) as AfmEvent

describe('FeedWorkspace', () => {
  beforeEach(() => window.localStorage.clear())

  it('renders messenger groups with stage badge and side alignment', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'x.ts' }, 's1', '2026-07-10T10:00:00Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'reply to user' }, 's1', '2026-07-10T10:00:01Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} stageId={null} />)
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveClass('feed-left')
    expect(within(groups[0] as HTMLElement).getByText('s1')).toBeInTheDocument()
    expect(groups[1]).toHaveClass('feed-right')
    expect(container.textContent).toContain('read_file: x.ts')
    expect(container.textContent).toContain('reply to user')
  })

  it('shows an empty feed without crashing', () => {
    const { container } = render(<FeedWorkspace events={[]} stageId={null} />)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
    expect(container.querySelectorAll('.feed-group')).toHaveLength(0)
  })

  it('renders agent narrative (agent_action tool="text") as prose, not a mono row', () => {
    const events = [ev('agent_action', { tool: 'text', detail: 'Мысль агента' }, 's1', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} stageId={null} />)
    const item = screen.getByText('Мысль агента').closest('.feed-item')
    expect(item).not.toBeNull()
    expect(item).not.toHaveClass('mono')
    expect(container.textContent).toContain('Мысль агента')
  })

  // Реальный кейс из вывода стадии review (goga): агент пишет заголовок, жирный текст и
  // markdown-таблицу. Раньше это отрисовывалось сырым текстом («сломанный md рендер»).
  it('renders agent markdown narrative as real markdown (heading, table, bold, code)', () => {
    const detail = [
      'Ревью задачи завершено. ✅',
      '',
      '## Итог ревью (`goga-review-task`)',
      '',
      'Проверил `task.md`. **Критичных и High-находок нет.**',
      '',
      '| # | Severity | Область | Статус |',
      '|---|----------|---------|--------|',
      '| 1 | Medium | Current State — точность ссылок | ✅ Fixed |',
      '',
      '**Вердикт: passed.**',
    ].join('\n')
    const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} stageId={null} />)

    const item = container.querySelector('.feed-item-markdown')
    expect(item).not.toBeNull()
    const h2 = container.querySelector('h2')
    expect(h2?.textContent).toContain('Итог ревью')
    expect(container.querySelector('table')).not.toBeNull()
    expect(container.querySelectorAll('th').length).toBeGreaterThan(0)
    expect(container.querySelectorAll('td').length).toBeGreaterThan(0)
    const strong = Array.from(container.querySelectorAll('strong')).map((el) => el.textContent)
    expect(strong).toContain('Критичных и High-находок нет.')
    const codes = Array.from(container.querySelectorAll('code')).map((el) => el.textContent)
    expect(codes).toContain('task.md')
    // Не должно быть сырых markdown-маркеров как текста.
    expect(container.textContent).not.toContain('## Итог')
  })

  it('does NOT markdown-render a user note (agent_note stays literal text)', () => {
    const events = [ev('agent_note', { text: '**literal**' }, 's1', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} stageId={null} />)
    expect(container.querySelector('.feed-item-markdown')).toBeNull()
    expect(container.querySelector('strong')).toBeNull()
    expect(container.textContent).toContain('**literal**')
  })

  // --- Фильтрация по стадии (scope This stage | All) ---

  // События двух стадий, для проверки фильтра.
  const twoStagesEvents = (): AfmEvent[] => [
    ev('agent_action', { tool: 'read_file', detail: 'a.ts' }, 's1', '2026-07-10T10:00:00Z'),
    ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:01Z'),
  ]

  it('по умолчанию (scope=stage) показывает события только переданного stageId', () => {
    const { container } = render(<FeedWorkspace events={twoStagesEvents()} stageId="s1" />)
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('flow-level событие (stageId==="") видно при выбранной стадии', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'a.ts' }, 's1', '2026-07-10T10:00:00Z'),
      ev('stage_status_changed', 'running', '', '2026-07-10T10:00:01Z'),
      ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:02Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('→ running') // flow-level всегда видно
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('переключение на All показывает события всех стадий, обратно на This stage — снова фильтрует', () => {
    const { container } = render(<FeedWorkspace events={twoStagesEvents()} stageId="s1" />)
    // По умолчанию — только s1.
    expect(container.textContent).not.toContain('read_file: b.ts')

    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('read_file: b.ts')

    fireEvent.click(screen.getByRole('button', { name: 'This stage' }))
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).not.toContain('read_file: b.ts')
  })

  it('stageId===null → тумблер скрыт и показываются все события', () => {
    const { container } = render(<FeedWorkspace events={twoStagesEvents()} stageId={null} />)
    expect(screen.queryByRole('button', { name: 'This stage' })).not.toBeInTheDocument()
    expect(container.textContent).toContain('read_file: a.ts')
    expect(container.textContent).toContain('read_file: b.ts')
  })

  it('empty-state зависит от эффективного scope', () => {
    // scope=stage, stageId есть, но событий этой стадии нет → «for this stage».
    const other = [ev('agent_action', { tool: 'read_file', detail: 'b.ts' }, 's2', '2026-07-10T10:00:00Z')]
    const { rerender } = render(<FeedWorkspace events={other} stageId="s1" />)
    expect(screen.getByText('No events for this stage yet')).toBeInTheDocument()

    // scope=stage, но stageId=null → эффективный scope false → общий empty-state.
    rerender(<FeedWorkspace events={[]} stageId={null} />)
    expect(screen.getByText('No events yet')).toBeInTheDocument()
  })

  it('только flow-level событие → рендерится, не empty', () => {
    const events = [ev('stage_status_changed', 'running', '', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(1)
    expect(screen.queryByText('No events for this stage yet')).not.toBeInTheDocument()
    expect(container.textContent).toContain('→ running')
  })

  // --- Навигация по diалог-элементам (dialog_question/dialog_answer) ---

  it('navigable-элемент (dialog_question) рендерится кнопкой и вызывает onOpenDialog с координатами вопроса', () => {
    const events = [
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'what next?' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} stageId="s1" onOpenDialog={onOpenDialog} />)
    const button = screen.getByRole('button', { name: /what next\?/ })
    expect(button.tagName).toBe('BUTTON')
    expect(button).toHaveClass('feed-item-navigable')
    fireEvent.click(button)
    expect(onOpenDialog).toHaveBeenCalledWith('s1', 'planning', 'q1')
  })

  it('navigable-элемент (dialog_answer) рендерится кнопкой и вызывает onOpenDialog с координатами вопроса', () => {
    const events = [
      ev('dialog_answer', { phase: 'implementation', id: 'q2', title: 'reply text' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} stageId="s1" onOpenDialog={onOpenDialog} />)
    const button = screen.getByRole('button', { name: /reply text/ })
    fireEvent.click(button)
    expect(onOpenDialog).toHaveBeenCalledWith('s1', 'implementation', 'q2')
  })

  it('не-navigable элемент (agent_action) НЕ рендерится кнопкой', () => {
    const events = [ev('agent_action', { tool: 'read_file', detail: 'x.ts' }, 's1', '2026-07-10T10:00:00Z')]
    const onOpenDialog = vi.fn()
    render(<FeedWorkspace events={events} stageId="s1" onOpenDialog={onOpenDialog} />)
    expect(screen.queryByRole('button', { name: /read_file/ })).not.toBeInTheDocument()
    expect(screen.getByText('read_file: x.ts').closest('.feed-item')?.tagName).toBe('DIV')
  })

  it('без onOpenDialog navigable-элемент рендерится как обычный div, без падения', () => {
    const events = [
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'what next?' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    expect(() => render(<FeedWorkspace events={events} stageId="s1" />)).not.toThrow()
    expect(screen.queryByRole('button', { name: /what next\?/ })).not.toBeInTheDocument()
    expect(screen.getByText('what next?').closest('.feed-item')?.tagName).toBe('DIV')
  })

  // --- Картинки, произведённые агентом ([AFM image: <name>]) ---

  describe('agent images', () => {
    afterEach(() => vi.useRealTimers())

    it('рендерит <img> с ожидаемым same-origin src, окружающий текст — markdown', () => {
      const detail = ['before text', '[AFM image: chart.png]', '## after'].join('\n')
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)

      const img = container.querySelector('img.feed-image') as HTMLImageElement | null
      expect(img).not.toBeNull()
      expect(img?.getAttribute('src')).toBe('/api/stages/s1/artifacts/chart.png')
      expect(img?.getAttribute('alt')).toBe('agent image')
      expect(img?.getAttribute('loading')).toBe('lazy')
      // Окружающий текст отрендерен как markdown.
      expect(container.textContent).toContain('before text')
      expect(container.querySelector('h2')?.textContent).toContain('after')
    })

    it('src строится через encodeURIComponent (имя/стадия не попадают сырыми)', () => {
      const detail = '[AFM image: a.b-c_1.png]'
      const events = [ev('agent_action', { tool: 'text', detail }, 'stage.1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="stage.1" />)
      const img = container.querySelector('img.feed-image') as HTMLImageElement | null
      expect(img?.getAttribute('src')).toBe('/api/stages/stage.1/artifacts/a.b-c_1.png')
    })

    it('onError выполняет backoff-ретрай: src получает retry-параметр', () => {
      vi.useFakeTimers()
      const detail = '[AFM image: chart.png]'
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)

      const img = container.querySelector('img.feed-image') as HTMLImageElement
      expect(img.getAttribute('src')).toBe('/api/stages/s1/artifacts/chart.png')

      act(() => {
        fireEvent.error(img)
        vi.advanceTimersByTime(2000)
      })

      const img2 = container.querySelector('img.feed-image') as HTMLImageElement
      expect(img2.getAttribute('src')).toContain('retry=1')
    })

    it('ретрай ограничен: после исчерпания попыток src больше не меняется', () => {
      vi.useFakeTimers()
      const detail = '[AFM image: chart.png]'
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)

      const fail = () => {
        const img = container.querySelector('img.feed-image') as HTMLImageElement
        act(() => {
          fireEvent.error(img)
          vi.advanceTimersByTime(5000)
        })
      }
      // Гоняем ошибки заведомо больше, чем максимум попыток.
      for (let i = 0; i < 6; i++) fail()

      const img = container.querySelector('img.feed-image') as HTMLImageElement
      const src = img.getAttribute('src') ?? ''
      const m = /retry=(\d+)/.exec(src)
      expect(m).not.toBeNull()
      expect(Number(m?.[1])).toBeLessThanOrEqual(3)
    })

    it('сегменты (текст+картинка+текст) лежат в колоночной обёртке в исходном порядке', () => {
      const detail = ['before', '[AFM image: chart.png]', 'after'].join('\n')
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)

      const wrap = container.querySelector('.feed-item-segments')
      expect(wrap).not.toBeNull()
      // Порядок детей обёртки: md(before) → img → md(after).
      const kids = Array.from(wrap!.children)
      expect(kids).toHaveLength(3)
      expect(kids[0]?.tagName).toBe('DIV')
      expect(kids[0]?.textContent).toContain('before')
      expect(kids[1]?.tagName).toBe('IMG')
      expect(kids[2]?.textContent).toContain('after')
      // timestamp — отдельный элемент РЯДА, не внутри колонки сегментов.
      expect(wrap!.querySelector('.feed-item-gap')).toBeNull()
    })

    it('маркер внутри code-fence НЕ становится картинкой (остаётся текстом)', () => {
      const detail = ['```', '[AFM image: nope.png]', '```'].join('\n')
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      expect(container.querySelector('img.feed-image')).toBeNull()
      expect(container.textContent).toContain('[AFM image: nope.png]')
    })
  })

  describe('note composer', () => {
    it('renders the composer when noteTarget is set', () => {
      render(<FeedWorkspace events={[]} stageId="s1" noteTarget="s1" onSendNote={vi.fn()} />)
      expect(screen.getByRole('button', { name: /send note to agent/i })).toBeInTheDocument()
    })

    it('does not render the composer when noteTarget is null', () => {
      render(<FeedWorkspace events={[]} stageId="s1" noteTarget={null} onSendNote={vi.fn()} />)
      expect(screen.queryByRole('button', { name: /send note to agent/i })).not.toBeInTheDocument()
    })

    it('delivers the typed note to onSendNote for the target stage', () => {
      const onSendNote = vi.fn().mockResolvedValue(undefined)
      render(<FeedWorkspace events={[]} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'учти 500' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      expect(onSendNote).toHaveBeenCalledWith('s1', 'учти 500')
    })
  })
})
