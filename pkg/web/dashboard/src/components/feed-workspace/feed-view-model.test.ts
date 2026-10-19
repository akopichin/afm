import { describe, it, expect } from 'vitest'
import type { AfmEvent } from '../../types'
import { toFeedItems, groupFeedItems, formatEventGap, buildFeedTimeline } from './feed-view-model'
import type { MeshMessage } from '../../api/mesh-client'

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
    // Ссылка на полный отчёт строится из id стадии + verification_id, а не из абсолютного report_path —
    // присутствие report_path в payload лишь СИГНАЛ "отчёт есть".
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

  it('verify_result verdict=accepted_stalemate (review_patience) renders as a success row naming the reason', () => {
    const items = toFeedItems([
      ev('verify_result', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', verdict: 'accepted_stalemate', reason: 'accepted after 2 unchanged round(s) (reviewer/author stalemate)' }, 's1', '2026-07-10T10:00:05Z'),
    ])
    expect(items[0]).toMatchObject({ tone: 'success', actor: 'system' })
    expect(items[0]?.text).toContain('Verify accepted')
    expect(items[0]?.text).toContain('stalemate')
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

    // Тот же список, прогнанный дважды — ключи стабильны и не плодят occurrence-суффиксы, если внутри
    // одного toFeedItems-вызова verification_id+step не повторяется.
    const again = toFeedItems([started, result])
    expect(again.map((i) => i.key)).toEqual(items.map((i) => i.key))
  })
})

describe('verify_retry — наблюдательное предупреждение о повторном verify-проходе', () => {
  it('renders as a system warning status row naming the step, command, attempt and reason', () => {
    const items = toFeedItems([
      ev(
        'verify_retry',
        { verification_id: 'v-1', step: 2, kind: 'agent', command: 'codex', attempt: 1, reason: 'needs changes' },
        's1',
        '2026-07-10T10:00:00Z',
      ),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'warning', kind: 'status' })
    expect(items[0]?.text).toContain('2') // step
    expect(items[0]?.text).toContain('codex') // command
    expect(items[0]?.text).toContain('1') // attempt
    expect(items[0]?.text).toContain('needs changes') // reason
  })

  it('keys a verify_retry row by verification_id+step+attempt so a reload replay never duplicates it', () => {
    const retry = ev('verify_retry', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', attempt: 1, reason: 'x' }, 's1', '2026-07-10T10:00:00Z')
    // Один и тот же attempt, прогнанный дважды — один и тот же стабильный ключ, не плодит дубликатов.
    const first = toFeedItems([retry])
    const again = toFeedItems([retry])
    expect(first[0]?.key).toBe(`verify:v-1:1:retry:1`)
    expect(again.map((i) => i.key)).toEqual(first.map((i) => i.key))
  })

  it('gives a different attempt a different key (distinct retry rows, not a collapse)', () => {
    const items = toFeedItems([
      ev('verify_retry', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', attempt: 1, reason: 'first' }, 's1', '2026-07-10T10:00:00Z'),
      ev('verify_retry', { verification_id: 'v-1', step: 1, kind: 'agent', command: 'codex', attempt: 2, reason: 'second' }, 's1', '2026-07-10T10:00:01Z'),
    ])
    expect(items).toHaveLength(2)
    expect(items[0]?.key).toBe('verify:v-1:1:retry:1')
    expect(items[1]?.key).toBe('verify:v-1:1:retry:2')
    expect(items[0]?.key).not.toBe(items[1]?.key)
  })
})

describe('verify_tree_modified — предупреждение о нарушении read-only изоляции верификатора', () => {
  it('verify_tree_modified renders as a warning row naming the step and command', () => {
    const items = toFeedItems([
      ev('verify_tree_modified', { verification_id: 'v-1', step: 2, command: 'claude' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'warning', kind: 'status' })
    expect(items[0]?.text).toContain('2') // step
    expect(items[0]?.text).toContain('claude') // command
  })

  it('keys a verify_tree_modified row by verification_id+step so a reload replay never duplicates it', () => {
    const modified = ev('verify_tree_modified', { verification_id: 'v-1', step: 1, command: 'claude' }, 's1', '2026-07-10T10:00:00Z')
    const first = toFeedItems([modified])
    const again = toFeedItems([modified])
    expect(first[0]?.key).toBe('verify:v-1:1:tree-modified')
    expect(again.map((i) => i.key)).toEqual(first.map((i) => i.key))
  })
})

describe('helper_result_repaired — a helper answer needed mechanical repair', () => {
  it('maps helper_result_repaired to a warning status line', () => {
    const items = toFeedItems([
      ev('helper_result_repaired', { task_id: 't-1' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'warning', kind: 'status' })
    expect(items[0]?.text).toContain('t-1')
  })
})

describe('helper_tree_modified — the working tree changed during a helper turn', () => {
  it('renders a neutral warning naming the agent, without blaming it', () => {
    const items = toFeedItems([
      ev('helper_tree_modified', { task_id: 't-1', agent: 'Reviewer' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'warning', kind: 'status' })
    expect(items[0]?.text).toBe("The working tree changed during Reviewer's turn")
  })

  it('falls back to the task id for a notice without an agent name', () => {
    const items = toFeedItems([ev('helper_tree_modified', { task_id: 't-1' }, 's1', '2026-07-10T10:00:00Z')])
    expect(items[0]?.text).toBe("The working tree changed during t-1's turn")
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

describe('auto_answered — dialog:true делает авто-ответ навигируемым, legacy/repair остаётся системной mono-строкой', () => {
  it('навигируемый авто-ответ (dialog:true) — системный элемент со стороны agent-диалога, несёт phase/id и текст ответа', () => {
    const items = toFeedItems([
      ev(
        'auto_answered',
        {
          phase: 'implementation',
          id: 'q7',
          answer: 'Option B (recommended)',
          from_options: true,
          question_title: 'Which storage backend?',
          dialog: true,
        },
        's1',
        '2026-07-10T10:00:00Z',
      ),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'system',
      side: 'left',
      kind: 'dialog',
      navigable: true,
      phase: 'implementation',
      id: 'q7',
    })
    // Текст несёт сам ответ, чтобы строку можно было отличить от вопроса.
    expect(items[0]?.text).toContain('Option B (recommended)')
    // Это НЕ ответ человека (dialog_answer, actor:'user', справа) — визуально
    // отличается: системный автоматический ответ слева, не в mono-стиле.
    expect(items[0]?.side).toBe('left')
    expect(items[0]?.mono).toBe(false)
  })

  it('legacy авто-ответ (без dialog) остаётся НЕ навигируемой системной mono-строкой', () => {
    const items = toFeedItems([
      ev('auto_answered', { phase: 'planning', id: 'q1', answer: 'yes' }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      actor: 'system',
      kind: 'dialog',
      text: 'auto-answered q1: yes',
      mono: true,
    })
    expect(items[0]?.navigable).toBeFalsy()
    expect(items[0]?.phase).toBeUndefined()
    expect(items[0]?.id).toBeUndefined()
  })

  it('repair-прогресс авто-ответ (dialog:false, answer "⚙️ …") остаётся НЕ навигируемой системной строкой', () => {
    const items = toFeedItems([
      ev('auto_answered', { phase: 'planning', id: 'q1', answer: '⚙️ repairing question…', from_options: false }, 's1', '2026-07-10T10:00:00Z'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]?.navigable).toBeFalsy()
    expect(items[0]?.mono).toBe(true)
    expect(items[0]?.actor).toBe('system')
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

describe('script_output stream / script_failed / hook_failed stderr tail (Task 7)', () => {
  it('renders a stderr script_output line distinctly', () => {
    const item = toFeedItems([ev('script_output', { hook: 'script', line: 'Traceback...', stream: 'stderr' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.tone).toBe('warning')
    expect(item?.text).toContain('stderr')
    expect(item?.text).toContain('Traceback...')
  })

  it('renders a stdout script_output line as before (neutral)', () => {
    const item = toFeedItems([ev('script_output', { hook: 'script', line: 'building', stream: 'stdout' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.tone).toBe('neutral')
    expect(item?.text).toBe('[script] building')
  })

  it('renders a script_output line with no stream field as stdout (backward compat with old notices)', () => {
    const item = toFeedItems([ev('script_output', { hook: 'script', line: 'building' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.tone).toBe('neutral')
    expect(item?.text).toBe('[script] building')
  })

  it('renders script_failed as a danger row with the error and a real multiline traceback preserved', () => {
    const tail = 'Traceback (most recent call last):\n  File "x.py", line 1\nValueError: boom'
    const item = toFeedItems([ev('script_failed', { error: 'exit status 1', stderr_tail: tail }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.tone).toBe('danger')
    expect(item?.text).toContain('exit status 1')
    expect(item?.text).toContain('Traceback (most recent call last):')
    expect(item?.text).toContain('File "x.py", line 1')
    expect(item?.text).toContain('ValueError: boom')
    expect(item?.markdown).toBe(true)
  })

  it('renders script_failed without a tail as a plain danger row', () => {
    const item = toFeedItems([ev('script_failed', { error: 'exit status 1' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.tone).toBe('danger')
    expect(item?.text).toContain('exit status 1')
    expect(item?.text).not.toContain('```')
  })

  it('appends stderr_tail to hook_failed when present, as a fenced code block', () => {
    const item = toFeedItems([ev('hook_failed', { hook: 'after', error: 'exit status 1', stderr_tail: 'boom' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.text).toContain('exit status 1')
    expect(item?.text).toContain('boom')
    expect(item?.markdown).toBe(true)
  })

  it('keeps hook_failed as today\'s plain row when stderr_tail is absent', () => {
    const item = toFeedItems([ev('hook_failed', { hook: 'after', error: 'exit status 1' }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item?.text).toBe('after-hook failed: exit status 1')
    expect(item?.markdown).toBeUndefined()
  })

  // Fix 4: untrusted stderr tail must not break out of its code fence or
  // trigger an image/file fetch via splitImageMarkers.
  it('script_failed: a ``` line in the tail widens the fence and an embedded [AFM image:] marker is neutralized, not extracted', () => {
    const tail = 'before\n```\n[AFM image: x.png]\nafter'
    const item = toFeedItems([ev('script_failed', { error: 'exit status 1', stderr_tail: tail }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item).toBeDefined()
    const text = item?.text ?? ''

    // The fence must be longer than the longest backtick run in the tail (3),
    // so an embedded ``` line can never close the block early.
    expect(text).toMatch(/````+/)
    // Marker text is still literally present (readable), but not as an exact
    // "[AFM image:" match — i.e. neutralized, not stripped.
    expect(text).toContain('image: x.png]')
    expect(text.includes('[AFM image:')).toBe(false)
    // The rest of the tail content survives untouched.
    expect(text).toContain('before')
    expect(text).toContain('after')
  })

  it('hook_failed: a ``` line in the tail widens the fence and an embedded [AFM file:] marker is neutralized, not extracted', () => {
    const tail = '```\n[AFM file: "secret.txt"]'
    const item = toFeedItems([ev('hook_failed', { hook: 'after', error: 'boom', stderr_tail: tail }, 's1', '2026-09-23T10:00:00Z')])[0]
    expect(item).toBeDefined()
    const text = item?.text ?? ''

    expect(text).toMatch(/````+/)
    expect(text).toContain('file: "secret.txt"]')
    expect(text.includes('[AFM file:')).toBe(false)
  })

  it('script_failed: a normal multiline tail with no backticks still renders fully with a plain triple-backtick fence (regression)', () => {
    const tail = 'Traceback (most recent call last):\n  File "x.py", line 1\nValueError: boom'
    const item = toFeedItems([ev('script_failed', { error: 'exit status 1', stderr_tail: tail }, 's1', '2026-09-23T10:00:00Z')])[0]
    const text = item?.text ?? ''
    expect(text).toContain('exit status 1')
    expect(text).toContain('Traceback (most recent call last):')
    expect(text).toContain('File "x.py", line 1')
    expect(text).toContain('ValueError: boom')
    expect(text).toContain('```\n')
    expect(text).not.toMatch(/````/)
  })

  it('script_failed: a marker in the error string (no tail) is also neutralized', () => {
    const item = toFeedItems([ev('script_failed', { error: 'boom [AFM image: x.png]' }, 's1', '2026-09-23T10:00:00Z')])[0]
    const text = item?.text ?? ''
    expect(text).toContain('image: x.png]')
    expect(text.includes('[AFM image:')).toBe(false)
  })
})

describe('mesh_* — Agent Mesh notice events (T7.2): real presentation rows, not the raw-type fallback', () => {
  it('mesh_participant renders a system row naming who joined', () => {
    const items = toFeedItems([ev('mesh_participant', { participant_id: 'qa' }, '', '2026-10-18T10:00:00Z')])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', side: 'left', tone: 'neutral', kind: 'status' })
    expect(items[0]?.text).toContain('qa')
    expect(items[0]?.text).not.toBe('mesh_participant') // не падает в дефолтный fallback с сырым типом
  })

  it('mesh_thread (opened) names the thread title', () => {
    const items = toFeedItems([ev('mesh_thread', { thread_id: 't1', title: 'Report format' }, '', '2026-10-18T10:00:00Z')])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ actor: 'system', kind: 'status' })
    expect(items[0]?.text).toContain('Report format')
  })

  it('mesh_thread (closed) names the thread id and says it closed', () => {
    const items = toFeedItems([ev('mesh_thread', { thread_id: 't1', status: 'closed' }, '', '2026-10-18T10:00:00Z')])
    expect(items).toHaveLength(1)
    expect(items[0]?.text).toContain('t1')
    expect(items[0]?.text.toLowerCase()).toContain('closed')
  })

  it('mesh_message names sender, kind and thread, and sides on the sender (user vs agent)', () => {
    const items = toFeedItems([
      ev('mesh_message', { message_id: 'm1', thread_id: 't1', sender_id: 'qa', kind: 'message' }, '', '2026-10-18T10:00:00Z'),
      ev('mesh_message', { message_id: 'm2', thread_id: 't1', sender_id: 'user', kind: 'reply' }, '', '2026-10-18T10:00:01Z'),
    ])
    expect(items).toHaveLength(2)
    expect(items[0]).toMatchObject({ actor: 'agent', side: 'left' })
    expect(items[0]?.text).toContain('qa')
    expect(items[0]?.text).toContain('t1')
    expect(items[1]).toMatchObject({ actor: 'user', side: 'right' })
    expect(items[1]?.text).toContain('reply')
  })

  it('keys a mesh_message row by message_id so a reload replay never duplicates it', () => {
    const msg = ev('mesh_message', { message_id: 'm1', thread_id: 't1', sender_id: 'qa', kind: 'message' }, '', '2026-10-18T10:00:00Z')
    const first = toFeedItems([msg])
    const again = toFeedItems([msg])
    expect(first[0]?.key).toBe('mesh:message:m1')
    expect(again.map((i) => i.key)).toEqual(first.map((i) => i.key))
  })

  it('mesh_receipt distinguishes acknowledged from delivered', () => {
    const items = toFeedItems([
      ev('mesh_receipt', { message_id: 'm1', participant_id: 'qa', status: 'acknowledged' }, '', '2026-10-18T10:00:00Z'),
      ev('mesh_receipt', { message_id: 'm1', participant_id: 'devops', status: 'delivered' }, '', '2026-10-18T10:00:01Z'),
    ])
    expect(items).toHaveLength(2)
    expect(items[0]?.text).toContain('qa')
    expect(items[0]?.text.toLowerCase()).toContain('acknowledged')
    expect(items[1]?.text).toContain('devops')
    expect(items[1]?.text.toLowerCase()).toContain('delivered')
  })

  it('keys a mesh_receipt row by message_id+participant_id+status so a redelivered receipt never duplicates it', () => {
    const receipt = ev('mesh_receipt', { message_id: 'm1', participant_id: 'qa', status: 'delivered' }, '', '2026-10-18T10:00:00Z')
    const first = toFeedItems([receipt])
    const again = toFeedItems([receipt])
    expect(first[0]?.key).toBe('mesh:receipt:m1:qa:delivered')
    expect(again.map((i) => i.key)).toEqual(first.map((i) => i.key))
  })

  it('mesh_wait names the waiter when registered, and the outcome when resolved', () => {
    const items = toFeedItems([
      ev('mesh_wait', { wait_id: 'w1', waiter_id: 'devops' }, '', '2026-10-18T10:00:00Z'),
      ev('mesh_wait', { wait_id: 'w1', status: 'ready' }, '', '2026-10-18T10:00:01Z'),
      ev('mesh_wait', { wait_id: 'w2', status: 'cancelled' }, '', '2026-10-18T10:00:02Z'),
    ])
    expect(items).toHaveLength(3)
    expect(items[0]?.text).toContain('devops')
    expect(items[1]?.text.toLowerCase()).toContain('resolved')
    expect(items[2]?.text.toLowerCase()).toContain('cancelled')
    expect(items[2]?.tone).toBe('warning')
  })

  it('mesh_board names the author, key and status', () => {
    const items = toFeedItems([ev('mesh_board', { key: 'qa.tests', author_id: 'qa', status: 'ready' }, '', '2026-10-18T10:00:00Z')])
    expect(items).toHaveLength(1)
    expect(items[0]?.text).toContain('qa')
    expect(items[0]?.text).toContain('qa.tests')
    expect(items[0]?.text).toContain('ready')
  })
})

// Agent Mesh Phase 8, T8.2 ("the Feed scenario"): the backend companion
// (pkg/orchestrator/agentcomm_feed_test.go's TestFeed_UserDiscussionThenQAClarification_NoRevisionNoHelperNoGate)
// drives an existing run-scoped QA asking the "impl" stage a clarification mid-exchange — this fixture feeds
// the exact mesh_* notice sequence that chain durably publishes (participants join, a thread opens between
// QA and the stage, QA's question, the stage's reply, the reply's delivery receipt) through the SAME
// toFeedItems/groupFeedItems this file's own T7.2 unit tests already cover one event at a time, proving the
// WHOLE story reads correctly together: both sides of the exchange are distinguishable, nothing collapses
// into the untyped fallback, and a reload replay (the identical events arriving a second time, exactly like
// re-deriving the feed from events.jsonl on refresh) never duplicates a row.
describe('T8.2 — the Feed scenario: QA clarifying with a stage renders as one coherent exchange', () => {
  const qaClarifiesWithStage = (): AfmEvent[] => [
    ev('mesh_participant', { participant_id: 'run:qa' }, '', '2026-10-18T10:00:00Z'),
    ev('mesh_participant', { participant_id: 'stage:impl' }, '', '2026-10-18T10:00:00Z'),
    ev('mesh_thread', { thread_id: 't-clarify', title: '' }, '', '2026-10-18T10:00:01Z'),
    ev('mesh_message', { message_id: 'm-ask', thread_id: 't-clarify', sender_id: 'run:qa', kind: 'message' }, '', '2026-10-18T10:00:02Z'),
    ev('mesh_message', { message_id: 'm-answer', thread_id: 't-clarify', sender_id: 'stage:impl', kind: 'message' }, '', '2026-10-18T10:00:03Z'),
    ev('mesh_receipt', { message_id: 'm-answer', participant_id: 'run:qa', status: 'delivered' }, '', '2026-10-18T10:00:04Z'),
  ]

  it('renders every step with real text, never the raw-type fallback', () => {
    const items = toFeedItems(qaClarifiesWithStage())
    expect(items).toHaveLength(6)
    for (const item of items) {
      expect(item.text).not.toBe('mesh_participant')
      expect(item.text).not.toBe('mesh_thread')
      expect(item.text).not.toBe('mesh_message')
      expect(item.text).not.toBe('mesh_receipt')
    }
  })

  it("QA's question and the stage's reply are both real message rows naming the thread and their own sender", () => {
    const items = toFeedItems(qaClarifiesWithStage())
    const ask = items[3]
    const answer = items[4]
    expect(ask?.kind).toBe('message')
    expect(ask?.text).toContain('run:qa')
    expect(ask?.text).toContain('t-clarify')
    expect(answer?.kind).toBe('message')
    expect(answer?.text).toContain('stage:impl')
    expect(answer?.text).toContain('t-clarify')
    // Neither sender is the fixed human "user" id, so the mapping's own user/agent split (mesh_message's own
    // doc comment) correctly sides BOTH on 'agent' — distinct messages, same side, exactly like two colleagues
    // talking to each other with no human in the exchange.
    expect(ask).toMatchObject({ actor: 'agent', side: 'left' })
    expect(answer).toMatchObject({ actor: 'agent', side: 'left' })
  })

  it('the delivery receipt names the recipient and the delivered status', () => {
    const items = toFeedItems(qaClarifiesWithStage())
    const receipt = items[5]
    expect(receipt?.text).toContain('run:qa')
    expect(receipt?.text.toLowerCase()).toContain('delivered')
  })

  it('a reload replay (the same events arriving again) keys every row identically — no duplicate rows', () => {
    const events = qaClarifiesWithStage()
    const first = toFeedItems(events)
    const replayed = toFeedItems(events)
    expect(replayed.map((i) => i.key)).toEqual(first.map((i) => i.key))
  })

  it('groups into one exchange: the roster/thread chatter and the two-message conversation are each their own bubble', () => {
    const groups = groupFeedItems(toFeedItems(qaClarifiesWithStage()))
    // mesh_participant x2 + mesh_thread are all actor:'system' → one bubble; the two mesh_message rows are
    // actor:'agent' → a second bubble (groupFeedItems groups by side+stageId+actor only, not by sender
    // identity — so QA's question and the stage's reply land in the SAME visual bubble, exactly like any other
    // pair of consecutive agent-side feed rows); the trailing receipt is actor:'system' again → a third bubble.
    expect(groups.map((g) => `${g.side}:${g.actor}`)).toEqual(['left:system', 'left:agent', 'left:system'])
    expect(groups[1]?.items).toHaveLength(2)
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

describe('buildFeedTimeline — stage events and conversation messages in one feed', () => {
  const at = (s: number) => `2026-10-19T10:00:${String(s).padStart(2, '0')}Z`
  const msg = (id: string, s: number, recipients = ['stage:s1']): MeshMessage => ({
    id, thread_id: 'mt-1', sender_id: 'run:qa', recipient_ids: recipients, kind: 'message', body: id, seq: s, created_at: at(s),
  })
  const items = toFeedItems([
    ev('stage_status_changed', 'running', 's1', at(1)),
    ev('agent_action', { tool: 'text', detail: 'working' }, 's1', at(3)),
    ev('agent_action', { tool: 'text', detail: 'done' }, 's1', at(5)),
  ])
  const shape = (entries: ReturnType<typeof buildFeedTimeline>) =>
    entries.map((e) => (e.kind === 'group' ? e.group.items.map((i) => i.text).join('+') : e.messages.map((m) => m.id).join('+')))

  it('interleaves by time: an event first on a tie, consecutive messages in one block', () => {
    const entries = buildFeedTimeline(items, [msg('a', 3), msg('b', 4), msg('c', 6)], 'all')
    expect(shape(entries)).toEqual(['→ running', 'working', 'a+b', 'done', 'c'])
  })

  it('Conversations hides the author\'s work; Author\'s work keeps only messages addressed to you', () => {
    const messages = [msg('a', 2), msg('b', 4, ['user'])]
    expect(shape(buildFeedTimeline(items, messages, 'conversations'))).toEqual(['a+b'])
    expect(shape(buildFeedTimeline(items, messages, 'author'))).toEqual(['→ running', 'working', 'b', 'done'])
  })
})
