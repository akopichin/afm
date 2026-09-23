import { describe, it, expect } from 'vitest'
import type { AfmEvent } from '../../types'
import { toFeedItems, groupFeedItems, formatEventGap } from './feed-view-model'

const ev = (type: string, payload: unknown, stageId: string, timestamp: string, seq?: number): AfmEvent =>
  ({ type, payload, stageId, timestamp, ...(seq !== undefined ? { seq } : {}) }) as AfmEvent

describe('toFeedItems — mapping (parity with the old feed formatting)', () => {
  it('maps known event types to text, actor and tone', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'read_file', detail: 'src/x.ts' }, 's1', '2026-07-10T10:00:01Z'),
      ev('agent_completed', 'implementation', 's1', '2026-07-10T10:00:02Z'),
      ev('approved', {}, 's1', '2026-07-10T10:00:04Z'),
      ev('hook_failed', { hook: 'post', error: 'boom' }, 's1', '2026-07-10T10:00:05Z'),
      ev('custom_unknown', null, '', '2026-07-10T10:00:06Z'),
    ])
    expect(items[0]).toMatchObject({ text: '→ running', actor: 'system', side: 'left' })
    expect(items[1]).toMatchObject({ text: 'read_file: src/x.ts', actor: 'agent', side: 'left', mono: true, kind: 'tool' })
    expect(items[2]).toMatchObject({ text: 'agent implementation completed', tone: 'success', kind: 'success' })
    expect(items[3]).toMatchObject({ text: 'approved', actor: 'user', side: 'right', tone: 'success' })
    expect(items[4]).toMatchObject({ text: 'post-hook failed: boom', tone: 'danger' })
    // Неизвестный тип — падать нельзя, показываем сам тип.
    expect(items[5]).toMatchObject({ text: 'custom_unknown', actor: 'system' })
  })

  it('renders lifecycle_hook_failed as a system-side warning carrying the hook id and error', () => {
    const items = toFeedItems([
      ev('lifecycle_hook_failed', { hook_id: 'telegram', event_id: 'run-1:flow:flow_started', error: 'exit status 1' }, '', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'warning', kind: 'status' })
    expect(items[0]?.text).toContain('telegram')
    expect(items[0]?.text).toContain('exit status 1')
  })

  it('renders an agent text action as clean prose (no "text:" prefix, not mono)', () => {
    const items = toFeedItems([ev('agent_action', { tool: 'text', detail: 'I initialized the build.' }, 's1', '2026-07-10T10:00:00Z')])
    expect(items[0]).toMatchObject({ text: 'I initialized the build.', kind: 'message', mono: false, actor: 'agent' })
  })

  it('flags ONLY the agent narrative (agent_action tool=text) as markdown', () => {
    const items = toFeedItems([
      ev('agent_action', { tool: 'text', detail: '## H' }, 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'Read', detail: 'x.ts' }, 's1', '2026-07-10T10:00:01Z'),
      ev('agent_note', { text: '**literal**' }, 's1', '2026-07-10T10:00:02Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'reply' }, 's1', '2026-07-10T10:00:03Z'),
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'ask' }, 's1', '2026-07-10T10:00:04Z'),
    ])
    expect(items[0]?.markdown).toBe(true)
    // Обычный tool, заметка пользователя и оба dialog-элемента — plain (markdown не выставлен).
    expect(items[1]?.markdown).toBeUndefined()
    expect(items[2]?.markdown).toBeUndefined()
    expect(items[3]?.markdown).toBeUndefined()
    expect(items[4]?.markdown).toBeUndefined()
  })

  it('maps status transitions to tones', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'done', 's1', '2026-07-10T10:00:00Z'),
      ev('stage_status_changed', 'failed', 's1', '2026-07-10T10:00:01Z'),
      ev('stage_status_changed', 'awaiting_approval', 's1', '2026-07-10T10:00:02Z'),
    ])
    expect(items[0]?.tone).toBe('success')
    expect(items[1]?.tone).toBe('danger')
    expect(items[2]?.tone).toBe('accent')
  })

  it('computes a static gap-from-previous duration per item', () => {
    const items = toFeedItems([
      ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00.000Z'),
      ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:05.000Z'),
      ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:01:35.000Z'),
    ])
    expect(items[0]?.gap).toBe('—')
    expect(items[1]?.gap).toBe('5s')
    expect(items[2]?.gap).toBe('1m')
  })

  it('gives same-timestamp same-type events unique stable keys', () => {
    const items = toFeedItems([
      ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:00Z'),
      ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items[0]?.key).not.toBe(items[1]?.key)
  })

  it('prefers seq for the key when present', () => {
    const items = toFeedItems([ev('approved', {}, 's1', '2026-07-10T10:00:00Z', 42)])
    expect(items[0]?.key).toBe('seq:42')
  })
})

describe('verify_started/verify_result — наблюдательные события AI-verify (V5b.1)', () => {
  it('verify_started renders as a neutral system status row naming the step and command', () => {
    const items = toFeedItems([
      ev('verify_started', { verification_id: 'v-1', step: 2, kind: 'agent', command: 'codex' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'neutral', kind: 'status' })
    expect(items[0]?.text).toContain('2')
    expect(items[0]?.text).toContain('codex')
  })

  it('verify_result verdict=pass renders as a success row and carries a report link when report_path is present', () => {
    const items = toFeedItems([
      ev(
        'verify_result',
        { verification_id: 'v-1', step: 2, kind: 'agent', command: 'codex', verdict: 'pass', reason: 'всё хорошо', report_path: '/runs/x/s1/verify/v-1/report.md' },
        's1',
        '2026-07-10T10:00:01Z',
      ),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', tone: 'success', kind: 'success' })
    expect(items[0]?.text).toContain('codex')
    expect(items[0]?.text).toContain('всё хорошо')
    // Ссылка на полный отчёт строится из id стадии + verification_id, а не из
    // абсолютного report_path (сервер отдаёт report.md только по этим id, см.
    // V5b.3) — присутствие report_path в payload лишь СИГНАЛ "отчёт есть".
    expect(items[0]?.reportVerificationId).toBe('v-1')
  })

  it('verify_result verdict=needs_changes (code rejection) renders warning, without a status change implication', () => {
    const items = toFeedItems([
      ev(
        'verify_result',
        { verification_id: 'v-1', step: 1, kind: 'shell', command: 'go test ./...', verdict: 'needs_changes', reason: 'тест упал', report_path: '/runs/x/s1/verify/v-1/report.md' },
        's1',
        '2026-07-10T10:00:02Z',
      ),
    ])
    expect(items[0]).toMatchObject({ tone: 'warning' })
    expect(items[0]?.kind).not.toBe('success')
  })

  it('verify_result verdict=inconclusive renders warning, distinct from a pass', () => {
    const items = toFeedItems([
      ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'inconclusive', reason: 'не удалось проверить' }, 's1', '2026-07-10T10:00:03Z'),
    ])
    expect(items[0]).toMatchObject({ tone: 'warning' })
    expect(items[0]?.reportVerificationId).toBeUndefined() // inconclusive никогда не пишет report.md
  })

  it('verify_result exec_error renders a tone DISTINCT from a needs_changes rejection (execution error vs code rejection)', () => {
    const rejection = toFeedItems([
      ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'needs_changes', reason: 'x' }, 's1', '2026-07-10T10:00:00Z'),
    ])[0]
    const execError = toFeedItems([
      ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', exec_error: 'exec_failure', reason: 'timeout talking to the model' }, 's1', '2026-07-10T10:00:01Z'),
    ])[0]
    expect(execError?.tone).not.toBe(rejection?.tone)
    expect(execError?.tone).toBe('danger')
    expect(execError?.reportVerificationId).toBeUndefined()
  })

  it('interrupted exec_error also renders as danger and mentions interruption', () => {
    const items = toFeedItems([
      ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'shell', command: './check.sh', exec_error: 'interrupted', reason: 'user interrupted' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items[0]).toMatchObject({ tone: 'danger' })
  })

  it('keys verify_started/verify_result by verification_id+step so a reload replay never duplicates a row', () => {
    const started = ev('verify_started', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex' }, 's1', '2026-07-10T10:00:00Z')
    const result = ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'pass', reason: 'ok' }, 's1', '2026-07-10T10:00:00Z')
    // Одинаковый timestamp (реалистично при replay из notices.jsonl с грубой
    // гранулярностью) не должен схлопнуть два РАЗНЫХ события в один ключ.
    const items = toFeedItems([started, result])
    expect(items).toHaveLength(2)
    expect(new Set(items.map((i) => i.key)).size).toBe(2)

    // Тот же список, прогнанный дважды (имитация повторного рендера той же
    // истории после reload) — ключи стабильны и не плодят occurrence-суффиксы,
    // если внутри одного toFeedItems-вызова verification_id+step не повторяется.
    const again = toFeedItems([started, result])
    expect(again.map((i) => i.key)).toEqual(items.map((i) => i.key))
  })
})

describe('dialog_question/dialog_answer — реальный текст диалога, ask_user/user_answered вытеснены', () => {
  it('dialog_question maps to a navigable agent item carrying phase/id from the payload', () => {
    const items = toFeedItems([
      ev('dialog_question', { phase: 'planning', id: 'q1', title: 'Which approach?' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'agent',
      side: 'left',
      kind: 'dialog',
      text: 'Which approach?',
      navigable: true,
      phase: 'planning',
      id: 'q1',
    })
  })

  it('dialog_answer maps to a navigable user item on the right side', () => {
    const items = toFeedItems([
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'Option A' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'user',
      side: 'right',
      kind: 'message',
      text: 'Option A',
      navigable: true,
      phase: 'planning',
      id: 'q1',
    })
  })

  it('ask_user and user_answered produce no feed item — replaced by dialog_question/dialog_answer', () => {
    const items = toFeedItems([
      ev('ask_user', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:00Z'),
      ev('user_answered', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:01Z'),
    ])
    expect(items).toHaveLength(0)
  })

  it('falls back to a generic label when title is missing or empty, and stays navigable', () => {
    const items = toFeedItems([
      ev('dialog_question', { phase: 'planning', id: 'q1' }, 's1', '2026-07-10T10:00:00Z'),
      ev('dialog_answer', { phase: 'planning', id: 'q1', title: '' }, 's1', '2026-07-10T10:00:01Z'),
    ])
    expect(items[0]).toMatchObject({ text: 'question', navigable: true })
    expect(items[1]).toMatchObject({ text: 'reply', navigable: true })
  })

  it('grouping preserves phase/id/navigable on each item', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('dialog_question', { phase: 'planning', id: 'q1', title: 'Q1?' }, 's1', '2026-07-10T10:00:00Z'),
      ]),
    )
    expect(groups[0]?.items[0]).toMatchObject({ phase: 'planning', id: 'q1', navigable: true })
  })
})

describe('agent_note — заметка агенту справа своим текстом, но НЕ кликабельна', () => {
  it('maps to a right-side user message carrying the payload text, non-navigable', () => {
    const items = toFeedItems([
      ev('agent_note', { id: 42, text: 'Учти что API возвращает 500' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'user',
      side: 'right',
      kind: 'message',
      text: 'Учти что API возвращает 500',
      mono: false,
    })
    // navigable опущен → FeedGroupView отрендерит обычный <div>, не кнопку.
    expect(items[0]?.navigable).toBeUndefined()
  })
})

describe('groupFeedItems', () => {
  it('merges consecutive same-side, same-stage items into one group', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('stage_status_changed', 'running', 's1', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:01Z'),
        ev('dialog_answer', { phase: 'planning', id: 'q1', title: 'yes' }, 's1', '2026-07-10T10:00:02Z'),
        ev('agent_action', { tool: 'b' }, 's1', '2026-07-10T10:00:03Z'),
      ]),
    )
    // system+agent для s1 — оба left, но actor различается (system vs agent) →
    // это два разных пузыря; затем user (right); затем снова agent (left).
    expect(groups.map((g) => `${g.side}:${g.actor}`)).toEqual([
      'left:system',
      'left:agent',
      'right:user',
      'left:agent',
    ])
    expect(groups[3]?.items).toHaveLength(1)
  })

  it('breaks a group when the stage changes', () => {
    const groups = groupFeedItems(
      toFeedItems([
        ev('agent_action', { tool: 'a' }, 's1', '2026-07-10T10:00:00Z'),
        ev('agent_action', { tool: 'b' }, 's2', '2026-07-10T10:00:01Z'),
      ]),
    )
    expect(groups).toHaveLength(2)
    expect(groups[0]?.stageId).toBe('s1')
    expect(groups[1]?.stageId).toBe('s2')
  })
})

describe('formatEventGap', () => {
  it('formats seconds/minutes/hours/days and em-dash for invalid', () => {
    expect(formatEventGap(5000, 0)).toBe('5s')
    expect(formatEventGap(90_000, 0)).toBe('1m')
    expect(formatEventGap(7_200_000, 0)).toBe('2h')
    expect(formatEventGap(172_800_000, 0)).toBe('2d')
    expect(formatEventGap(NaN, 0)).toBe('—')
  })
})
