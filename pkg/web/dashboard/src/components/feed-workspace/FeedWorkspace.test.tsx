import { render, screen, fireEvent, act, waitFor, within } from '@testing-library/react'
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import type { AfmEvent } from '../../types'
import { FeedWorkspace, FeedGroupView, feedItemSignature } from './FeedWorkspace'
import type { FeedGroup, FeedItem } from './feed-view-model'
import * as verifyReportClient from '../../api/verify-report-client'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import { sideAgentFeedGroups } from '../side-agent/side-agent-feed-model'
import type { SideAgentEvent } from '../../types/side-agent'

const ev = (type: string, payload: unknown, stageId: string, timestamp: string): AfmEvent =>
  ({ type, payload, stageId, timestamp }) as AfmEvent

describe('FeedWorkspace', () => {
  beforeEach(() => window.localStorage.clear())

  it('renders messenger groups with side alignment', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'x.ts' }, 's1', '2026-07-10T10:00:00Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'reply to user' }, 's1', '2026-07-10T10:00:01Z'),
    ]
    const { container } = render(<FeedWorkspace events={events} stageId={null} />)
    const groups = container.querySelectorAll('.feed-group')
    expect(groups).toHaveLength(2)
    expect(groups[0]).toHaveClass('feed-left')
    expect(groups[0]?.querySelector('.feed-stage-badge')).toBeNull()
    expect(groups[1]).toHaveClass('feed-right')
    expect(container.textContent).toContain('read_file x.ts')
    expect(container.textContent).toContain('reply to user')
  })

  describe('tool-call groups', () => {
    const at = (n: number) => `2026-07-10T10:00:0${n}Z`
    const tools = [
      ev('agent_action', { tool: 'Read', detail: 'src/a.ts' }, 's1', at(1)),
      ev('agent_action', { tool: 'Edit', detail: 'src/a.ts' }, 's1', at(2)),
      ev('agent_action', { tool: 'Read', detail: 'src/b.ts' }, 's1', at(3)),
    ]

    it('renders consecutive tool calls as ONE collapsed group under the agent message', () => {
      const events = [ev('agent_action', { tool: 'text', detail: 'Working on it' }, 's1', at(0)), ...tools]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      const groups = container.querySelectorAll('details.feed-tool-group')
      expect(groups).toHaveLength(1)
      expect(groups[0]).not.toHaveAttribute('open')
      expect(groups[0]?.querySelector('summary')).toHaveTextContent('3 tool calls · read, edit')
      expect(groups[0]?.querySelectorAll('.feed-tool-rows .feed-item')).toHaveLength(3)
      const rows = groups[0]?.closest('.feed-rows')
      expect(rows?.querySelector('.feed-item-markdown')).toHaveTextContent('Working on it')
    })

    it('shows each call as "<Tool> <detail>" and says "1 tool call" for a single call', () => {
      const { container } = render(<FeedWorkspace events={tools.slice(0, 1)} stageId="s1" />)
      expect(container.querySelector('summary')).toHaveTextContent(/^1 tool call · read$/)
      const row = container.querySelector('.feed-tool-rows .feed-item')
      expect(row?.querySelector('.feed-tool-name')).toHaveTextContent('Read')
      expect(row?.querySelector('.feed-item-text')).toHaveTextContent('Read src/a.ts')
    })

    it('does not invent a result chip', () => {
      const { container } = render(<FeedWorkspace events={tools} stageId="s1" />)
      expect(container.querySelector('.feed-tool-result')).toBeNull()
    })

    it('a message between tool calls splits them into two groups', () => {
      const events = [tools[0], ev('agent_action', { tool: 'text', detail: 'Next' }, 's1', at(2)), tools[2]] as AfmEvent[]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      expect(container.querySelectorAll('details.feed-tool-group')).toHaveLength(2)
    })

    it('toggles from the keyboard-reachable summary and keeps the group open when a call is appended', () => {
      const { container, rerender } = render(<FeedWorkspace events={tools.slice(0, 2)} stageId="s1" />)
      const details = container.querySelector('details') as HTMLDetailsElement
      details.open = true
      rerender(<FeedWorkspace events={tools} stageId="s1" />)
      expect(container.querySelectorAll('details.feed-tool-group')).toHaveLength(1)
      expect(container.querySelector('details')).toBe(details)
      expect(details.open).toBe(true)
      expect(details.querySelector('summary')).toHaveTextContent('3 tool calls')
    })
  })

  it('shows an empty feed without crashing', () => {
    const { container } = render(<FeedWorkspace events={[]} stageId={null} />)
    expect(container.querySelector('#feed-content')).toBeInTheDocument()
    expect(container.querySelectorAll('.feed-group')).toHaveLength(0)
  })

  it.each([
    ['stage', 's1'],
    ['activity', null],
  ])('shows host-local clock and previous-event gap in the %s feed', (_name, stageId) => {
    const events = [
      ev('stage_status_changed', 'running', 's1', '2026-10-21T05:31:09Z'),
      ev('agent_action', { tool: 'read_file', detail: 'a' }, 's1', '2026-10-21T05:31:16Z'),
      ev('agent_action', { tool: 'read_file', detail: 'b' }, 's1', '2026-10-21T05:33:16Z'),
    ]
    const { container } = render(
      <MessageTimeZoneProvider timeZone="Asia/Tokyo" utcOffsetMinutes={0}>
        <FeedWorkspace events={events} stageId={stageId} />
      </MessageTimeZoneProvider>,
    )
    const times = Array.from(container.querySelectorAll('time.message-time'))
    // The flow line, the stage agent's author row (the time of its first row), then the two tool calls.
    expect(times).toHaveLength(4)
    expect(times.map((time) => time.querySelector('.message-time-clock')?.textContent)).toEqual(['14:31', '14:31', '14:31', '14:33'])
    expect(times.map((time) => time.querySelector('.message-time-gap')?.textContent)).toEqual(['+0s', '+7s', '+7s', '+2m'])
    expect(times[0]).toHaveAttribute('title', expect.stringContaining('first event (+0s)'))
    expect(times[1]).toHaveAttribute('title', expect.stringContaining('+7s after previous'))
    expect(times.every((time) => time.getAttribute('tabindex') === '0')).toBe(true)
  })

  it('omits a time label for an invalid event timestamp', () => {
    const events = [ev('agent_action', { tool: 'read_file', detail: 'a' }, 's1', 'invalid')]
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    expect(container.querySelector('.feed-item')).toBeInTheDocument()
    expect(container.querySelector('time.message-time')).toBeNull()
  })

  it('shows side-agent history through the same host-local time renderer', () => {
    const sideEvent = (seq: number, timestamp: string, value: string): SideAgentEvent => ({
      schemaVersion: 1, seq, conversationId: 'c1', turnId: 't1', timestamp,
      type: 'assistant_text', data: { text: value },
    })
    const groups = sideAgentFeedGroups([
      sideEvent(1, '2026-10-21T05:31:09Z', 'hello'),
      sideEvent(2, '2026-10-21T05:31:16Z', 'again'),
    ])
    const { container } = render(
      <MessageTimeZoneProvider timeZone="Asia/Tokyo" utcOffsetMinutes={0}>
        {groups.map((group) => <FeedGroupView key={group.key} group={group} showStageBadges={false} />)}
      </MessageTimeZoneProvider>,
    )
    const times = Array.from(container.querySelectorAll('time.message-time'))
    expect(times.map((time) => time.querySelector('.message-time-clock')?.textContent)).toEqual(['14:31', '14:31'])
    expect(times.map((time) => time.querySelector('.message-time-gap')?.textContent)).toEqual(['+0s', '+7s'])
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

  // --- FeedWorkspace — тупой рендерер: рендерит переданные events как есть ---

  it('renders exactly the events it is given (no internal filtering)', () => {
    const events = [
      ev('agent_action', { tool: 'read_file', detail: 'early' }, 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'read_file', detail: 'noise' }, 's2', '2026-07-10T10:00:01Z'),
    ]
    render(<FeedWorkspace events={events} stageId="early" />)
    // both rows present — FeedWorkspace does not filter by stageId anymore
    expect(screen.getByText(/early/)).toBeInTheDocument()
    expect(screen.getByText(/noise/)).toBeInTheDocument()
  })

  it('shows the empty hint when given no events', () => {
    render(<FeedWorkspace events={[]} stageId="early" emptyHint="No events for this stage yet" />)
    expect(screen.getByText('No events for this stage yet')).toBeInTheDocument()
  })

  it('shows the default empty hint when no emptyHint is given', () => {
    render(<FeedWorkspace events={[]} stageId={null} />)
    expect(screen.getByText('No events yet')).toBeInTheDocument()
  })

  it('только flow-level событие → рендерится, не empty', () => {
    const events = [ev('stage_status_changed', 'running', '', '2026-07-10T10:00:00Z')]
    const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
    expect(container.querySelectorAll('.feed-group')).toHaveLength(1)
    expect(container.textContent).toContain('→ running')
  })

  describe('systemAuthorLabel + imagePolicy (Side agent parameterization)', () => {
    // Регрессия: [AFM image: …] раскрывается в stage-картинку; флоу-строки идут без подписи «Flow».
    it('defaults: flow lines carry no author label and image markers render as <img>', () => {
      const events = [
        ev('stage_status_changed', 'running', '', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'text', detail: '[AFM image: chart.png]' }, 's1', '2026-07-10T10:00:01Z'),
      ]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      const labels = Array.from(container.querySelectorAll('.feed-actor')).map((el) => el.textContent)
      expect(labels).not.toContain('Flow')
      expect(labels).not.toContain('Agent')
      expect(container.querySelector('.feed-flow')).toHaveTextContent('→ running')
      expect(container.querySelector('img.feed-image')).not.toBeNull()
    })

    it('a stage agent message has an author row: avatar, bold stage name, role and time, without an outer frame', () => {
      const events = [ev('agent_action', { tool: 'text', detail: 'Working on it' }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      const group = container.querySelector('.feed-group[data-actor="agent"]')
      expect(group?.querySelector(':scope > .mesh-avatar')).toBeInTheDocument()
      const row = group?.querySelector('.feed-author-row')
      expect(row?.querySelector('.feed-actor')).toHaveTextContent('s1')
      expect(row?.querySelector('.feed-role')).toHaveTextContent('Stage agent')
      expect(row?.querySelector('time.message-time')).toBeInTheDocument()
      expect(group?.querySelector('.feed-bubble')).toBeNull()
      expect(group?.querySelector('.feed-rows .feed-item-markdown')).toHaveTextContent('Working on it')
      expect(container.textContent).not.toContain('Agent')
    })

    it('each stage agent message has its own author row, tool calls stay in one collapsed group between them', () => {
      const events = [
        ev('agent_action', { tool: 'text', detail: 'First thought' }, 's1', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'Read', detail: 'a.ts' }, 's1', '2026-07-10T10:00:01Z'),
        ev('agent_action', { tool: 'Edit', detail: 'a.ts' }, 's1', '2026-07-10T10:00:02Z'),
        ev('agent_action', { tool: 'text', detail: 'Second thought' }, 's1', '2026-07-10T10:00:03Z'),
        ev('agent_action', { tool: 'text', detail: 'Third thought' }, 's1', '2026-07-10T10:00:04Z'),
      ]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      const groups = Array.from(container.querySelectorAll('.feed-group[data-actor="agent"]'))
      expect(groups).toHaveLength(3)
      for (const group of groups) {
        expect(group.querySelectorAll('.feed-author-row')).toHaveLength(1)
        expect(group.querySelector(':scope > .mesh-avatar')).toBeInTheDocument()
      }
      expect(groups[0]?.querySelectorAll('details')).toHaveLength(1)
      expect(groups[1]?.querySelectorAll('details')).toHaveLength(0)
      expect(groups.map((g) => g.querySelector('.feed-item-markdown')?.textContent?.trim())).toEqual(['First thought', 'Second thought', 'Third thought'])
    })

    it('imagePolicy="none" renders [AFM image: …] as literal markdown text, not an <img>', () => {
      const events = [ev('agent_action', { tool: 'text', detail: 'before [AFM image: chart.png] after' }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" imagePolicy="none" />)
      expect(container.querySelector('img.feed-image')).toBeNull()
      expect(container.querySelector('.feed-item-segments')).toBeNull()
      expect(container.textContent).toContain('[AFM image: chart.png]')
    })
  })

  describe('showStageBadges', () => {
    const events = (): AfmEvent[] => [ev('agent_action', { tool: 'read_file', detail: 'a.ts' }, 's1', '2026-07-10T10:00:00Z')]

    it('по умолчанию (Feed для одной стадии) бейдж стадии не рендерится', () => {
      const { container } = render(<FeedWorkspace events={events()} stageId="s1" />)
      expect(container.querySelector('.feed-stage-badge')).toBeNull()
    })


  })

  // --- Навигация по diалог-элементам (dialog_question/dialog_answer) ---

  it('navigable-элемент (dialog_question) рендерится кнопкой и вызывает onOpenDialog с координатами вопроса', () => {
    const events = [
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'what next?' }, 's1', '2026-07-10T10:00:00Z'),
    ]
    const onOpenDialog = vi.fn()
    const { container } = render(<FeedWorkspace events={events} stageId="s1" onOpenDialog={onOpenDialog} />)
    const button = screen.getByRole('button', { name: /what next\?/ })
    expect(button.tagName).toBe('BUTTON')
    expect(button).toHaveClass('feed-item-navigable')
    // The time of the group's first row is in the author row, outside the button, so it may take focus.
    expect(button.querySelector('time.message-time')).toBeNull()
    const time = container.querySelector('.feed-author-row time.message-time')
    expect(time).toHaveAttribute('tabindex', '0')
    expect(time?.querySelector('.message-time-gap')).toHaveTextContent('+0s')
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
    expect(screen.getByText('read_file').closest('.feed-item')?.tagName).toBe('DIV')
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
      expect(wrap!.querySelector('.message-time')).toBeNull()
    })

    it('маркер внутри code-fence НЕ становится картинкой (остаётся текстом)', () => {
      const detail = ['```', '[AFM image: nope.png]', '```'].join('\n')
      const events = [ev('agent_action', { tool: 'text', detail }, 's1', '2026-07-10T10:00:00Z')]
      const { container } = render(<FeedWorkspace events={events} stageId="s1" />)
      expect(container.querySelector('img.feed-image')).toBeNull()
      expect(container.textContent).toContain('[AFM image: nope.png]')
    })
  })

  describe('verify report link (V5b.1/V5b.3)', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    it('renders a report link for a verify_result with a report, and shows the fetched markdown on click', async () => {
      const spy = vi.spyOn(verifyReportClient, 'getVerifyReport').mockResolvedValue('# Отчёт\n\nВердикт: pass')
      const events = [
        ev(
          'verify_result',
          { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'pass', reason: 'ok', report_path: '/runs/x/s1/verify/v-1/report.md' },
          's1',
          '2026-07-10T10:00:00Z',
        ),
      ]
      render(<FeedWorkspace events={events} stageId={null} />)

      const link = screen.getByRole('button', { name: /show full report/i })
      fireEvent.click(link)

      expect(spy).toHaveBeenCalledWith('s1', 'v-1')
      await waitFor(() => expect(screen.getByText('Отчёт')).toBeInTheDocument())
    })

    it('does not render a report link when the event carries no report_path', () => {
      const events = [
        ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'inconclusive', reason: 'unclear' }, 's1', '2026-07-10T10:00:00Z'),
      ]
      render(<FeedWorkspace events={events} stageId={null} />)
      expect(screen.queryByRole('button', { name: /show full report/i })).not.toBeInTheDocument()
    })

    it('toggling the link twice collapses the report and does not re-fetch', async () => {
      const spy = vi.spyOn(verifyReportClient, 'getVerifyReport').mockResolvedValue('report body')
      const events = [
        ev(
          'verify_result',
          { verification_id: 'v-1', step: 1, kind: 'shell', command: './check.sh', verdict: 'needs_changes', reason: 'x', report_path: '/runs/x/s1/verify/v-1/report.md' },
          's1',
          '2026-07-10T10:00:00Z',
        ),
      ]
      render(<FeedWorkspace events={events} stageId={null} />)

      const link = screen.getByRole('button', { name: /show full report/i })
      fireEvent.click(link)
      await waitFor(() => expect(screen.getByText('report body')).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: /hide report/i }))
      expect(screen.queryByText('report body')).not.toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: /show full report/i }))
      await waitFor(() => expect(screen.getByText('report body')).toBeInTheDocument())
      expect(spy).toHaveBeenCalledTimes(1)
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

  // --- Ответ на мысль агента (repliable thoughts) ---

  const thoughtItem = (over: Partial<FeedItem> = {}): FeedItem => ({
    key: 'k1',
    stageId: 's1',
    actor: 'agent',
    side: 'left',
    tone: 'neutral',
    kind: 'message',
    text: 'мысль агента',
    gap: '—',
    ts: 0,
    mono: false,
    markdown: true,
    ...over,
  })

  const groupOf = (items: FeedItem[]): FeedGroup => ({
    key: items[0]!.key,
    side: 'left',
    stageId: 's1',
    actor: 'agent',
    items,
  })

  describe('FeedGroupView — repliable thoughts', () => {
    it('the author row names the stage agent when the caller knows its name', () => {
      const { container } = render(<FeedGroupView group={groupOf([thoughtItem()])} showStageBadges={false} agentName="Checkout executor" />)
      expect(container.querySelector('.feed-author-row .feed-actor')).toHaveTextContent('Checkout executor')
      expect(container.querySelector('.mesh-avatar')).toHaveTextContent('CE')
    })

    it('клик по строке-мысли вызывает onReplyToThought(key, text)', () => {
      const onReply = vi.fn()
      render(<FeedGroupView group={groupOf([thoughtItem()])} showStageBadges={false} onReplyToThought={onReply} />)
      fireEvent.click(screen.getByText('мысль агента'))
      expect(onReply).toHaveBeenCalledWith('k1', 'мысль агента')
    })

    it('клик по кнопке ↩ вызывает onReplyToThought(key, text)', () => {
      const onReply = vi.fn()
      render(<FeedGroupView group={groupOf([thoughtItem()])} showStageBadges={false} onReplyToThought={onReply} />)
      fireEvent.click(screen.getByRole('button', { name: /reply to this thought/i }))
      expect(onReply).toHaveBeenCalledWith('k1', 'мысль агента')
    })

    it('reply button keeps the thought time out of the tab order', () => {
      const first = thoughtItem({ key: 'k0', ts: Date.parse('2026-10-21T05:31:00Z'), gap: '0s' })
      const { container } = render(
        <FeedGroupView group={groupOf([first, thoughtItem({ ts: Date.parse('2026-10-21T05:31:09Z'), gap: '9s' })])}
          showStageBadges={false} onReplyToThought={vi.fn()} />,
      )
      // The first row's time is in the author row; a later thought keeps its own time inside the row.
      const row = container.querySelectorAll('.feed-thought-row')[1]
      expect(row?.querySelector('time.message-time')).toBeInTheDocument()
      expect(row?.querySelector('time.message-time')).not.toHaveAttribute('tabindex')
      const reply = within(row as HTMLElement).getByRole('button', { name: /reply to this thought/i })
      reply.focus()
      expect(document.activeElement).toBe(reply)
      expect(row?.contains(reply)).toBe(true)
    })

    it('клик по ссылке <a> внутри мысли НЕ выбирает мысль (link guard)', () => {
      const onReply = vi.fn()
      const item = thoughtItem({ text: 'см [link](https://example.com) тут' })
      render(<FeedGroupView group={groupOf([item])} showStageBadges={false} onReplyToThought={onReply} />)
      fireEvent.click(screen.getByRole('link', { name: 'link' }))
      expect(onReply).not.toHaveBeenCalled()
    })

    it('строка-инструмент (не мысль) не репляибл', () => {
      const onReply = vi.fn()
      const tool = thoughtItem({ kind: 'tool', markdown: false, mono: true, text: 'Bash: ls' })
      render(<FeedGroupView group={groupOf([tool])} showStageBadges={false} onReplyToThought={onReply} />)
      expect(screen.queryByRole('button', { name: /reply to this thought/i })).toBeNull()
      fireEvent.click(screen.getByText('Bash'))
      expect(onReply).not.toHaveBeenCalled()
    })

    it('ровно одна строка получает is-reply-target (по activeReplyKey)', () => {
      const onReply = vi.fn()
      const items = [thoughtItem({ key: 'k1', text: 'первая' }), thoughtItem({ key: 'k2', text: 'вторая' })]
      const { container } = render(
        <FeedGroupView group={groupOf(items)} showStageBadges={false} onReplyToThought={onReply} activeReplyKey="k2" />,
      )
      const targets = container.querySelectorAll('.is-reply-target')
      expect(targets).toHaveLength(1)
      expect(targets[0]?.textContent).toContain('вторая')
    })

    it('без onReplyToThought мысли не репляиблы', () => {
      const { container } = render(<FeedGroupView group={groupOf([thoughtItem()])} showStageBadges={false} />)
      expect(screen.queryByRole('button', { name: /reply to this thought/i })).toBeNull()
      expect(container.querySelector('.feed-thought-row')).toBeNull()
      // Но текст мысли по-прежнему отрисован как markdown.
      expect(screen.getByText('мысль агента')).toBeInTheDocument()
    })
  })

  describe('reply-to-thought integration (FeedWorkspace)', () => {
    const thoughtEvents = (): AfmEvent[] => [
      ev('agent_action', { tool: 'text', detail: 'Мысль для ответа' }, 's1', '2026-07-10T10:00:00Z'),
    ]

    it('клик по ↩ в per-stage Feed показывает quote-чип в композере', () => {
      render(<FeedWorkspace events={thoughtEvents()} stageId="s1" noteTarget="s1" onSendNote={vi.fn().mockResolvedValue(undefined)} />)
      fireEvent.click(screen.getByRole('button', { name: /reply to this thought/i }))
      expect(screen.getByText(/in reply to/i)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /cancel reply to thought/i })).toBeInTheDocument()
    })

    it('смена стадии сбрасывает reply-чип (нет stale-цитаты)', () => {
      const { rerender } = render(
        <FeedWorkspace events={thoughtEvents()} stageId="s1" noteTarget="s1" onSendNote={vi.fn().mockResolvedValue(undefined)} />,
      )
      fireEvent.click(screen.getByRole('button', { name: /reply to this thought/i }))
      expect(screen.getByRole('button', { name: /cancel reply to thought/i })).toBeInTheDocument()

      rerender(<FeedWorkspace events={thoughtEvents()} stageId="s2" noteTarget="s2" onSendNote={vi.fn().mockResolvedValue(undefined)} />)
      expect(screen.queryByRole('button', { name: /cancel reply to thought/i })).toBeNull()
    })

    it('успешная отправка ответа доставляет quote+comment и сбрасывает чип', async () => {
      const onSendNote = vi.fn().mockResolvedValue(undefined)
      render(<FeedWorkspace events={thoughtEvents()} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      fireEvent.click(screen.getByRole('button', { name: /reply to this thought/i }))
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'ответ' } })
      fireEvent.click(screen.getByRole('button', { name: /send note to agent/i }))
      await waitFor(() => expect(screen.queryByRole('button', { name: /cancel reply to thought/i })).toBeNull())
      expect(onSendNote).toHaveBeenCalledWith('s1', expect.stringContaining('> Мысль для ответа'))
    })

    it('Лента без композера рендерит мысли не репляиблыми', () => {
      render(<FeedWorkspace events={thoughtEvents()} stageId={null} />)
      expect(screen.queryByRole('button', { name: /reply to this thought/i })).toBeNull()
    })
  })

  describe('entrance animation of the newest message', () => {
    it('animates only the newest row when a new live message arrives', () => {
      const { rerender } = render(<FeedWorkspace events={[
        ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00Z'),
      ]} stageId="s1" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0) // baseline
      rerender(<FeedWorkspace events={[
        ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00Z'),
        ev('agent_action', { tool: 'text', detail: 'b' }, 's1', '2026-01-01T00:00:01Z'),
      ]} stageId="s1" />)
      const entering = document.querySelectorAll('.feed-item--enter')
      expect(entering.length).toBe(1)                                   // exactly the newest row
      expect(entering[0]?.textContent).toContain('b')
    })

    it('does not animate a reconnect re-sync that leaves the newest message unchanged', () => {
      const a = ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00Z')
      const b = ev('agent_action', { tool: 'text', detail: 'b' }, 's1', '2026-01-01T00:00:01Z')
      const { rerender } = render(<FeedWorkspace events={[a, b]} stageId="s1" />) // baseline, last = b reconnect re-sync: history prepends an older row and re-keys 'a' with an
      // interpolated timestamp, but the newest message (content 'b') is unchanged:
      rerender(<FeedWorkspace events={[
        ev('agent_action', { tool: 'text', detail: 'older' }, 's1', '2025-12-31T23:59:59Z'),
        ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00.5Z'),
        b,
      ]} stageId="s1" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)
    })

    it('does not replay the entrance after animationend + regroup remount', () => {
      const histRow = ev('agent_action', { tool: 'text', detail: 'H' }, 's1', '2026-01-01T00:00:00Z')
      const liveRow = ev('agent_action', { tool: 'text', detail: 'L' }, 's1', '2026-01-01T00:00:02Z')
      const { rerender } = render(<FeedWorkspace events={[histRow]} stageId="s1" />) // baseline
      rerender(<FeedWorkspace events={[histRow, liveRow]} stageId="s1" />)           // L animates
      const row = document.querySelector('.feed-item--enter') as HTMLElement
      expect(row).not.toBeNull()
      fireEvent.animationEnd(row)                                                    // entrance done (its own animationend)
      // A backfill prepends (regroups), but the newest message (L) is unchanged:
      rerender(<FeedWorkspace events={[
        ev('agent_action', { tool: 'text', detail: 'M' }, 's1', '2026-01-01T00:00:01Z'),
        histRow,
        liveRow,
      ]} stageId="s1" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)          // no replay
    })

    it('does not animate on a stage switch', () => {
      const { rerender } = render(<FeedWorkspace events={[ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00Z')]} stageId="s1" />)
      rerender(<FeedWorkspace events={[ev('agent_action', { tool: 'text', detail: 'x' }, 's2', '2026-01-01T00:00:03Z')]} stageId="s2" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)
    })

    it('does not animate when switching an activity view to a stage', () => {
      const { rerender } = render(<FeedWorkspace
        events={[ev('agent_action', { tool: 'text', detail: 'activity-last' }, 's1', '2026-01-01T00:00:00Z')]}
        stageId={null}  />)
      rerender(<FeedWorkspace
        events={[ev('agent_action', { tool: 'text', detail: 'stage-full-last' }, 'full', '2026-01-01T00:00:05Z')]}
        stageId="full"  />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)
    })

    // The decision MUST key on CONTENT, not the feed key. A key-based signal would animate here; a content
    // one must not.
    it('decides by content not the feed key: a re-keyed but content-unchanged newest row does not animate', () => {
      const mk = (detail: string) => ev('agent_action', { tool: 'text', detail }, 's1', '2026-01-01T00:00:00Z')
      const { rerender } = render(<FeedWorkspace events={[mk('x'), mk('same')]} stageId="s1" />) // baseline; newest 'same' = key #1
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)
      rerender(<FeedWorkspace events={[mk('y'), mk('x'), mk('same')]} stageId="s1" />)           // prepend shifts newest to key #2
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)                       // content unchanged -> no animate
    })

    // The entrance clears ONLY on the row's own animationend, never on a bubbled child animation.
    it('clears the entrance on the row’s own animationend, not a bubbled child animation', () => {
      const first = ev('agent_action', { tool: 'text', detail: 'a' }, 's1', '2026-01-01T00:00:00Z')
      const events = [first, ev('agent_action', { tool: 'text', detail: 'b' }, 's1', '2026-01-01T00:00:01Z')]
      const { rerender } = render(<FeedWorkspace events={[first]} stageId="s1" />) // baseline
      rerender(<FeedWorkspace events={events} stageId="s1" />)                       // 'b' animates
      const row = document.querySelector('.feed-item--enter') as HTMLElement
      expect(row).not.toBeNull()
      const child = row.querySelector('.feed-item-text') as HTMLElement
      expect(child).not.toBeNull()
      fireEvent.animationEnd(child)                                                     // bubbles up — must be ignored
      rerender(<FeedWorkspace events={events} stageId="s1" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(1)             // still armed
      fireEvent.animationEnd(document.querySelector('.feed-item--enter') as HTMLElement) // row's own end
      rerender(<FeedWorkspace events={events} stageId="s1" />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)             // cleared
    })

    // Per-branch wiring: the entrance class + its self-clearing onAnimationEnd must be present on ALL
    // three row branches, not just the default div.
    it('animates and self-clears a navigable (dialog) newest row (button branch)', () => {
      const q1 = ev('dialog_question', { phase: 'planning', id: 'q1', title: 'first?' }, 's1', '2026-01-01T00:00:00Z')
      const q2 = ev('dialog_question', { phase: 'planning', id: 'q2', title: 'second?' }, 's1', '2026-01-01T00:00:01Z')
      const { rerender } = render(<FeedWorkspace events={[q1]} stageId="s1" onOpenDialog={vi.fn()} />) // baseline
      rerender(<FeedWorkspace events={[q1, q2]} stageId="s1" onOpenDialog={vi.fn()} />)                  // q2 animates
      const row = document.querySelector('.feed-item--enter')
      expect(row?.tagName).toBe('BUTTON')
      expect(row).toHaveClass('feed-item-navigable')
      fireEvent.animationEnd(row as HTMLElement)                                                        // its own end
      rerender(<FeedWorkspace events={[q1, q2]} stageId="s1" onOpenDialog={vi.fn()} />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)                              // cleared
    })

    // The reply-able thought branch renders through its own <div className="… feed-item-thought">; it too
    // must carry the entrance class and self-clear.
    it('animates and self-clears a reply-able thought newest row (thought branch)', () => {
      const onSendNote = vi.fn().mockResolvedValue(undefined)
      const t1 = ev('agent_action', { tool: 'text', detail: 'первая мысль' }, 's1', '2026-01-01T00:00:00Z')
      const t2 = ev('agent_action', { tool: 'text', detail: 'вторая мысль' }, 's1', '2026-01-01T00:00:01Z')
      const { rerender } = render(<FeedWorkspace events={[t1]} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      rerender(<FeedWorkspace events={[t1, t2]} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      const row = document.querySelector('.feed-item--enter')
      expect(row).toHaveClass('feed-item-thought')
      fireEvent.animationEnd(row as HTMLElement)
      rerender(<FeedWorkspace events={[t1, t2]} stageId="s1" noteTarget="s1" onSendNote={onSendNote} />)
      expect(document.querySelectorAll('.feed-item--enter').length).toBe(0)
    })

    // Signature must be collision-free: two DIFFERENT tuples that would share a '|'-join must produce
    // DIFFERENT signatures, else a genuinely-new newest row fails to animate.
    it('feedItemSignature does not collide for different tuples that share a |-join', () => {
      const a = feedItemSignature({ stageId: 'x', kind: 'tool', actor: 'agent', text: 'message|user|z' })
      const b = feedItemSignature({ stageId: 'x|tool|agent', kind: 'message', actor: 'user', text: 'z' })
      expect(a).not.toBe(b)
    })
  })
})


it('starts tool calls collapsed and leaves the reading position alone when expanded', () => {
  const events = [ev('agent_action', { tool: 'read_file', detail: 'long source text' }, 's1', '2026-01-01T00:00:00Z')]
  const { container, rerender } = render(<FeedWorkspace events={events} stageId="s1" />)
  const details = container.querySelector('details') as HTMLDetailsElement
  expect(details.open).toBe(false)
  const scroll = container.querySelector('.feed-scroll') as HTMLElement
  Object.defineProperties(scroll, { scrollHeight: { value: 1000, configurable: true }, clientHeight: { value: 200, configurable: true } })
  scroll.scrollTop = 800
  fireEvent.scroll(scroll)
  fireEvent.click(details.querySelector('summary') as HTMLElement)
  expect(details.open).toBe(true)
  rerender(<FeedWorkspace events={[...events, ev('agent_action', { tool: 'text', detail: 'new message' }, 's1', '2026-01-01T00:00:01Z')]} stageId="s1" />)
  expect(scroll.scrollTop).toBe(800)
})

it('never gives a closed tool result a delayed entrance animation', () => {
  vi.useFakeTimers()
  try {
    const initial = ev('agent_action', { tool: 'text', detail: 'baseline' }, 's1', '2026-01-01T00:00:00Z')
    const tool = ev('agent_action', { tool: 'read_file', detail: 'later.txt' }, 's1', '2026-01-01T00:00:01Z')
    const { rerender } = render(<FeedWorkspace events={[initial]} stageId="s1" />)
    rerender(<FeedWorkspace events={[initial, tool]} stageId="s1" />)
    const details = document.querySelector('details')!
    expect(details).not.toHaveAttribute('open')
    expect(details.querySelector('.feed-item--enter')).toBeNull()
    act(() => vi.advanceTimersByTime(2400))
    fireEvent.click(details.querySelector('summary')!)
    expect(details.querySelector('.feed-item--enter')).toBeNull()
  } finally { vi.useRealTimers() }
})
