import type { AfmEvent, StageStatus } from '../../types'
import type { MeshMessage } from '../../api/mesh-client'

// Семантическая презентация ленты: каждое РЕАЛЬНОЕ событие из events.jsonl превращается в FeedItem с
// ролью, тоном и текстом.

export type FeedActor = 'agent' | 'user' | 'system'
export type FeedTone = 'neutral' | 'success' | 'warning' | 'danger' | 'accent'
export type FeedItemKind = 'tool' | 'script' | 'status' | 'success' | 'message' | 'dialog'

export type FeedItem = {
  key: string
  stageId: string
  actor: FeedActor
  authorLabel?: string
  side: 'left' | 'right' // user → right, agent/system → left
  tone: FeedTone
  kind: FeedItemKind
  text: string
  gap: string // статичная длительность: разница с предыдущим событием ленты
  ts: number // время события (ms; NaN — нет валидного timestamp), для слияния с беседами стадии
  mono: boolean // tool/script/auto-answer — компактная mono-строка dialog_question/dialog_answer: координаты вопроса
  // диалога для последующего перехода к нему.
  phase?: string
  id?: string
  navigable?: boolean
  // markdown — рендерить text как markdown, а не как сырую строку. Выставляется ТОЛЬКО для нарратива
  // агента (agent_action tool="text"): источник события знает его семантику.
  markdown?: boolean
  // reportVerificationId — id verify-прохода, только у verify_result-строк, для которых событие несло
  // непустой report_path.
  reportVerificationId?: string
  // meshMessageId — a discussion_message row's message id: the row is dropped where the same post is shown as
  // its mirrored conversation bubble (buildFeedTimeline), so the post is not shown twice.
  meshMessageId?: string
}

// FeedGroup — последовательные items одной стороны и стадии, слитые визуально в
// один «пузырь» с общей шапкой (стадия + актор). Группировка чисто визуальная.
export type FeedGroup = {
  key: string
  side: 'left' | 'right'
  stageId: string
  actor: FeedActor
  authorLabel?: string
  items: FeedItem[]
}

function statusTone(status: string): FeedTone {
  switch (status as StageStatus) {
    case 'done':
    case 'ready':
      return 'success'
    case 'failed':
    case 'hook_failed':
      return 'danger'
    case 'retrying':
    case 'paused':
      return 'warning'
    case 'awaiting_approval':
    case 'awaiting_user_input':
      return 'accent'
    default:
      return 'neutral'
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function str(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null || value === undefined) return ''
  return String(value)
}

function extractStatusString(data: unknown): string {
  if (typeof data === 'string') return data
  if (isRecord(data) && typeof data.status === 'string') return data.status
  return ''
}

// codeFence uses max(3, longestBacktickRun + 1) so untrusted stderr cannot close its Markdown block.
function codeFence(content: string): string {
  const runs = content.match(/`+/g)
  const longest = runs === null ? 0 : Math.max(...runs.map((r) => r.length))
  return '`'.repeat(Math.max(3, longest + 1))
}

// neutralizeMarkers inserts a zero-width space after '[AFM' so untrusted stderr cannot trigger image/file
// fetches.
function neutralizeMarkers(s: string): string {
  return s.replaceAll('[AFM image:', '[AFM​ image:').replaceAll('[AFM file:', '[AFM​ file:')
}

type Mapped = {
  actor: FeedActor
  tone: FeedTone
  kind: FeedItemKind
  text: string
  mono: boolean
  phase?: string
  id?: string
  navigable?: boolean
  markdown?: boolean
  reportVerificationId?: string
  // dedupeId — переопределяет стандартный ключ дедупликации там, где событие несёт свой собственный
  // стабильный идентификатор надёжнее timestamp.
  dedupeId?: string
  meshMessageId?: string
}

// mapEvent — единственная точка соответствия «тип события → презентация».
// null — событие сознательно не рендерится в ленте (см. ask_user/user_answered ниже).
function mapEvent(event: AfmEvent): Mapped | null {
  const data = event.payload
  const obj = isRecord(data) ? data : {}

  switch (event.type) {
    case 'stage_status_changed': {
      const s = extractStatusString(data)
      return { actor: 'system', tone: statusTone(s), kind: 'status', text: `→ ${s}`, mono: false }
    }
    case 'agent_completed':
      return { actor: 'agent', tone: 'success', kind: 'success', text: `agent ${str(data)} completed`, mono: false }
    case 'agent_action': {
      const tool = str(obj.tool)
      const detail = str(obj.detail)
      // Нарратив агента — чистая проза без "text:"-префикса и без mono: это сообщение агента, а не вызов
      // инструмента.
      if (tool === 'text') {
        return { actor: 'agent', tone: 'neutral', kind: 'message', text: detail, mono: false, markdown: true }
      }
      return { actor: 'agent', tone: 'neutral', kind: 'tool', text: `${tool}${detail !== '' ? `: ${detail}` : ''}`, mono: true }
    }
    case 'script_output': {
      // stream различает stdout/stderr: stderr — тревожная mono-строка, как и обычный tool-вызов, но с
      // явной пометкой ":stderr", чтобы отличить от stdout в общей ленте script-стадии.
      const stream = str(obj.stream)
      if (stream === 'stderr') {
        return { actor: 'agent', tone: 'warning', kind: 'script', text: `[${str(obj.hook)}:stderr] ${str(obj.line)}`, mono: true }
      }
      return { actor: 'agent', tone: 'neutral', kind: 'script', text: `[${str(obj.hook)}] ${str(obj.line)}`, mono: true }
    }
    case 'script_failed': {
      // Итоговый сбой script-стадии: error — короткая причина, stderr_tail — хвост stderr для диагностики.
      const error = neutralizeMarkers(str(obj.error))
      const tail = neutralizeMarkers(str(obj.stderr_tail))
      const fence = codeFence(tail)
      return {
        actor: 'system',
        tone: 'danger',
        kind: 'status',
        markdown: true,
        mono: false,
        text: tail !== '' ? `**script failed:** ${error}\n\n${fence}\n${tail}\n${fence}` : `**script failed:** ${error}`,
      }
    }
    case 'hook_failed': {
      // stderr_tail: фенс-код-блок только когда хвост реально есть; без хвоста — прежняя plain-строка БЕЗ
      // markdown-рендера.
      const error = neutralizeMarkers(str(obj.error))
      const tail = neutralizeMarkers(str(obj.stderr_tail))
      const base = `${str(obj.hook)}-hook failed: ${error}`
      if (tail === '') {
        return { actor: 'system', tone: 'danger', kind: 'status', text: base, mono: false }
      }
      const fence = codeFence(tail)
      return {
        actor: 'system',
        tone: 'danger',
        kind: 'status',
        markdown: true,
        mono: false,
        text: `${base}\n\n${fence}\n${tail}\n${fence}`,
      }
    }
    case 'hook_resolved':
      return { actor: 'system', tone: 'success', kind: 'status', text: `${str(obj.hook)}-hook ${str(obj.resolution)}`, mono: false }
    case 'lifecycle_hook_failed':
      // Финальный сбой lifecycle хука — чисто информационное предупреждение: FSM/стадию/ран оно не
      // трогает, поэтому tone 'warning', а не 'danger', как у блокирующего hook_failed выше.
      return { actor: 'system', tone: 'warning', kind: 'status', text: `lifecycle hook ${str(obj.hook_id)} failed: ${str(obj.error)}`, mono: false }
    case 'team_warning':
      // The flow's team: declaration could not be applied as written for one member (a durable run-level notice,
      // pkg/orchestrator/team.go) — the run goes on, so a warning, not an error.
      return { actor: 'system', tone: 'warning', kind: 'status', text: `team member ${str(obj.member_id)}: ${str(obj.message)}`, mono: false }
    case 'verify_started':
    case 'verify_result':
    case 'verify_retry':
    case 'verify_tree_modified':
      // Verify-события наблюдательные: рендерятся строкой в ленте (mapVerifyEvent), но не меняют FSM-статус стадии.
      return mapVerifyEvent(event.type, obj)
    case 'approved':
      return { actor: 'user', tone: 'success', kind: 'message', text: 'approved', mono: false }
    case 'revised':
      return { actor: 'user', tone: 'warning', kind: 'message', text: `revisions: ${str(data)}`, mono: false }
    case 'retry_scheduled':
      return { actor: 'system', tone: 'warning', kind: 'status', text: `retry: ${str(data)}`, mono: false }
    case 'retry_exhausted':
      return { actor: 'system', tone: 'danger', kind: 'status', text: 'retries exhausted', mono: false }
    case 'manual_retry':
      return { actor: 'system', tone: 'warning', kind: 'status', text: 'manual retry', mono: false }
    case 'ask_user':
    case 'user_answered':
      // Вытеснены dialog_question/dialog_answer (несут реальный текст вопроса/ ответа) — эти события
      // больше не рендерим, чтобы не дублировать их пустой заглушкой в ленте.
      return null
    case 'dialog_question': {
      const title = str(obj.title)
      return {
        actor: 'agent',
        tone: 'accent',
        kind: 'dialog',
        text: title !== '' ? title : 'question',
        mono: false,
        navigable: true,
        phase: str(obj.phase),
        id: str(obj.id),
      }
    }
    case 'dialog_answer': {
      const title = str(obj.title)
      return {
        actor: 'user',
        tone: 'accent',
        kind: 'message',
        text: title !== '' ? title : 'reply',
        mono: false,
        navigable: true,
        phase: str(obj.phase),
        id: str(obj.id),
      }
    }
    case 'agent_note':
      // Заметка агенту — справа своим текстом, как ответ на вопрос, но НЕ кликабельна: это доставленный
      // юзером текст, а не переход к диалогу.
      return { actor: 'user', tone: 'neutral', kind: 'message', text: str(obj.text), mono: false }
    case 'auto_answered': {
      // Реальный авто-ответ на вопрос диалога несёт dialog:true. Текст несёт сам ответ, чтобы строку можно
      // было отличить от вопроса.
      if (obj.dialog === true) {
        const answer = str(obj.answer)
        const title = str(obj.question_title)
        const body = answer !== '' ? answer : title !== '' ? title : 'auto-answered'
        return {
          actor: 'system',
          tone: 'accent',
          kind: 'dialog',
          text: `⚙ ${body}`,
          mono: false,
          navigable: true,
          phase: str(obj.phase),
          id: str(obj.id),
        }
      }
      // Legacy/repair авто-ответ (без dialog-флага, напр. "⚙️ …"-прогресс
      // ремонта битого question.json) — прежняя не навигируемая mono-строка.
      return { actor: 'system', tone: 'neutral', kind: 'dialog', text: `auto-answered ${str(obj.id)}: ${str(obj.answer)}`, mono: true }
    }
    case 'helper_result_repaired': {
      // A helper answer needed mechanical repair (markdown fence/surrounding prose) before it could be
      // strictly decoded — observational only, the task's own verdict is unaffected.
      const taskId = str(obj.task_id)
      return {
        actor: 'system',
        tone: 'warning',
        kind: 'status',
        text: `Helper task ${taskId}'s answer needed mechanical repair`,
        mono: false,
        dedupeId: `helper:${taskId}:repaired`,
      }
    }
    case 'helper_tree_modified': {
      // The working tree changed while a helper's turn ran — observational only. Anybody may have changed it
      // (the stage agent works in the same tree), so the line says when, not who.
      const agent = str(obj.agent) !== '' ? str(obj.agent) : str(obj.task_id)
      return { actor: 'system', tone: 'warning', kind: 'status', text: `The working tree changed during ${agent}'s turn`, mono: false }
    }
    case 'context_warning':
      return { actor: 'system', tone: 'warning', kind: 'status', text: `context warning: ${str(data)}`, mono: false }
    case 'mesh_participant':
      // Agent Mesh (T7.2): a participant (stage/helper/user/side-agent) became a member of the mesh roster —
      // observational only, fires every time a thread opens naming them, not just on first add.
      return { actor: 'system', tone: 'neutral', kind: 'status', text: `${str(obj.participant_id)} joined the mesh`, mono: false }
    case 'mesh_thread': {
      // 'closed' comes with status:"closed" and no title; opening always carries a title (possibly empty).
      if (str(obj.status) === 'closed') {
        return { actor: 'system', tone: 'neutral', kind: 'status', text: `thread ${str(obj.thread_id)} closed`, mono: false }
      }
      const title = str(obj.title)
      const text = title !== '' ? `thread opened: ${title}` : `thread ${str(obj.thread_id)} opened`
      return { actor: 'system', tone: 'neutral', kind: 'status', text, mono: false }
    }
    case 'mesh_message': {
      // sender_id is the fixed human participant id ("user") for a human-authored message — everything else
      // (a stage, a helper, a side agent) renders on the agent side, same convention as agent_action/agent_note.
      const senderId = str(obj.sender_id)
      const actor: FeedActor = senderId === 'user' ? 'user' : 'agent'
      return {
        actor,
        tone: 'neutral',
        kind: 'message',
        text: `${senderId} sent a ${str(obj.kind)} in thread ${str(obj.thread_id)}`,
        mono: false,
        dedupeId: `mesh:message:${str(obj.message_id)}`,
      }
    }
    case 'mesh_receipt': {
      // Delivery and terminal undeliverable notices share this event type. A receipt can re-fire for the
      // same (message, recipient) pair on retry, hence the composite dedupeId instead of timestamp.
      const status = str(obj.status)
      const text =
        status === 'acknowledged'
          ? `${str(obj.participant_id)} acknowledged message ${str(obj.message_id)}`
          : status === 'delivered'
            ? `message ${str(obj.message_id)} delivered to ${str(obj.participant_id)}`
            : `message ${str(obj.message_id)} ${status} for ${str(obj.participant_id)}`
      return {
        actor: 'system',
        tone: status === 'undeliverable' ? 'warning' : 'neutral',
        kind: 'status',
        text,
        mono: false,
        dedupeId: `mesh:receipt:${str(obj.message_id)}:${str(obj.participant_id)}:${status}`,
      }
    }
    case 'mesh_wait': {
      // Registration carries waiter_id (no status yet); resolution/cancellation carries status (agentcomm
      // WaitReady "ready" or WaitCancelled "cancelled") and no waiter_id.
      if (obj.waiter_id !== undefined) {
        return { actor: 'system', tone: 'neutral', kind: 'status', text: `${str(obj.waiter_id)} is waiting`, mono: false }
      }
      const status = str(obj.status)
      const tone: FeedTone = status === 'cancelled' ? 'warning' : 'neutral'
      const outcome = status === 'ready' ? 'resolved' : status
      return { actor: 'system', tone, kind: 'status', text: `wait ${str(obj.wait_id)} ${outcome}`, mono: false }
    }
    case 'discussion_message': {
      // A post in the stage's conversation with its agent. The stage feed shows it as a conversation bubble (its
      // mesh mirror, or the post itself from the stage's log) and drops this row then (buildFeedTimeline); the
      // row stays where no bubble is shown (the Full feed, or before the stage's log was read).
      const author = str(obj.author_kind) === 'human' ? 'You' : str(obj.author_id)
      const messageId = str(obj.message_id)
      return {
        actor: 'system',
        tone: 'neutral',
        kind: 'status',
        text: `${author} posted in the stage conversation`,
        mono: false,
        dedupeId: `discussion:${messageId}`,
        meshMessageId: messageId,
      }
    }
    case 'mesh_board':
      // Publish() payload: key, author_id, status — a free-form board status string (e.g. "ready").
      return {
        actor: 'system',
        tone: 'neutral',
        kind: 'status',
        text: `${str(obj.author_id)} published ${str(obj.key)}: ${str(obj.status)}`,
        mono: false,
      }
    default:
      return { actor: 'system', tone: 'neutral', kind: 'status', text: event.type, mono: false }
  }
}

// mapVerifyEvent — презентация AI-verify наблюдательных событий.
function mapVerifyEvent(type: 'verify_started' | 'verify_result' | 'verify_retry' | 'verify_tree_modified', obj: Record<string, unknown>): Mapped {
  const step = typeof obj.step === 'number' ? obj.step : Number(str(obj.step))
  const command = str(obj.command)
  const verificationId = str(obj.verification_id)
  const dedupeId = `verify:${verificationId}:${step}:${type}`

  if (type === 'verify_started') {
    return { actor: 'system', tone: 'neutral', kind: 'status', text: `Verify step ${step} · ${command}`, mono: false, dedupeId }
  }

  if (type === 'verify_retry') {
    // Транзиентный инфра-сбой verify-процесса, повторяемый после backoff: attempt нумерует повторную
    // попытку запуска; dedupeId несёт attempt, чтобы разные попытки были отдельными строками, а повтор
    // одной — не дублировался при reload-replay.
    const attempt = typeof obj.attempt === 'number' ? obj.attempt : Number(str(obj.attempt))
    const reason = str(obj.reason)
    return {
      actor: 'system',
      tone: 'warning',
      kind: 'status',
      text: `Verify retry ${attempt} (step ${step}) · ${command}: ${reason}`,
      mono: false,
      dedupeId: `verify:${verificationId}:${step}:retry:${attempt}`,
    }
  }

  if (type === 'verify_tree_modified') {
    // Ридонли-верифаер изменил рабочее дерево — это сбой изоляции самого верификатора, не вердикт по коду
    // автора; в отличие от retry здесь нет поля вроде attempt, различающего повторы, поэтому suffix —
    // фиксированный короткий `tree-modified` вместо `type`, просто для читаемости ключа.
    return {
      actor: 'system',
      tone: 'warning',
      kind: 'status',
      text: `Verify step ${step} · ${command} modified the working tree (verifier must be read-only)`,
      mono: false,
      dedupeId: `verify:${verificationId}:${step}:tree-modified`,
    }
  }

  const reason = str(obj.reason)
  const verdict = str(obj.verdict)
  const execError = str(obj.exec_error)
  const reportVerificationId = str(obj.report_path) !== '' ? verificationId : undefined

  if (verdict === 'pass') {
    return { actor: 'system', tone: 'success', kind: 'success', text: `Verify passed (step ${step}) · ${command}: ${reason}`, mono: false, reportVerificationId, dedupeId }
  }
  if (verdict === 'accepted_stalemate') {
    // Синтезированный afm вердикт: стадия принята, потому что автор перестал менять дерево в ответ на
    // повторные отклонения.
    return { actor: 'system', tone: 'success', kind: 'success', text: `Verify accepted (step ${step}) · ${command}: ${reason}`, mono: false, dedupeId }
  }
  if (verdict === 'needs_changes') {
    return { actor: 'system', tone: 'warning', kind: 'status', text: `Verify needs changes (step ${step}) · ${command}: ${reason}`, mono: false, reportVerificationId, dedupeId }
  }
  if (verdict === 'inconclusive') {
    return { actor: 'system', tone: 'warning', kind: 'status', text: `Verify inconclusive (step ${step}) · ${command}: ${reason}`, mono: false, dedupeId }
  }
  if (execError === 'interrupted') {
    return { actor: 'system', tone: 'danger', kind: 'status', text: `Verify interrupted (step ${step}) · ${command}: ${reason}`, mono: false, dedupeId }
  }
  // "exec_failure" или неизвестное значение — тоже ошибка ИСПОЛНЕНИЯ, не
  // рейтинг кода: тот же тон, тот же текстовый регистр.
  return { actor: 'system', tone: 'danger', kind: 'status', text: `Verify execution error (step ${step}) · ${command}: ${reason}`, mono: false, dedupeId }
}

// Статичная длительность строки: разница между этим событием и предыдущим в ленте по порядку отображения.
// Нет предыдущего или невалидный timestamp — em dash.
export function formatEventGap(tsMs: number, prevTsMs: number): string {
  if (Number.isNaN(tsMs) || Number.isNaN(prevTsMs)) return '—'
  const diffSec = Math.max(0, Math.floor((tsMs - prevTsMs) / 1000))
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

// toFeedItems — события → FeedItem[] с уникальными стабильными ключами.
export function toFeedItems(events: AfmEvent[]): FeedItem[] {
  const counts = new Map<string, number>()
  const items: FeedItem[] = []

  events.forEach((event, index) => {
    const m = mapEvent(event)
    if (m === null) return // ask_user/user_answered — не рендерим (см. mapEvent)

    const base = event.seq !== undefined ? `seq:${event.seq}` : (m.dedupeId ?? `${event.timestamp}|${event.type}|${event.stageId}`)
    const n = counts.get(base) ?? 0
    counts.set(base, n + 1)
    const key = n === 0 ? base : `${base}#${n}`

    const ts = Date.parse(event.timestamp)
    const prevTs = index > 0 ? Date.parse(events[index - 1]?.timestamp ?? '') : NaN
    items.push({
      key,
      stageId: event.stageId,
      actor: m.actor,
      side: m.actor === 'user' ? 'right' : 'left',
      tone: m.tone,
      kind: m.kind,
      text: m.text,
      gap: formatEventGap(ts, prevTs),
      ts,
      mono: m.mono,
      phase: m.phase,
      id: m.id,
      navigable: m.navigable,
      markdown: m.markdown,
      reportVerificationId: m.reportVerificationId,
      meshMessageId: m.meshMessageId,
    })
  })

  return items
}

// groupFeedItems сливает подряд идущие items одной стороны и стадии в один FeedGroup. Разрыв (новый
// пузырь) — при смене стороны/стадии/актора.
export function groupFeedItems(items: FeedItem[]): FeedGroup[] {
  const groups: FeedGroup[] = []
  for (const item of items) {
    const last = groups[groups.length - 1]
    if (last && last.side === item.side && last.stageId === item.stageId && last.actor === item.actor && last.authorLabel === item.authorLabel) {
      last.items.push(item)
    } else {
      groups.push({ key: item.key, side: item.side, stageId: item.stageId, actor: item.actor, authorLabel: item.authorLabel, items: [item] })
    }
  }
  return groups
}

// FeedFilter — фильтр ленты стадии: всё, только беседы, только работа автора (события стадии).
export type FeedFilter = 'all' | 'conversations' | 'author'

// FeedTimelineEntry — строка ленты стадии: пузырь событий автора или подряд идущие сообщения бесед.
export type FeedTimelineEntry =
  | { kind: 'group'; key: string; group: FeedGroup }
  | { kind: 'conversation'; key: string; messages: MeshMessage[] }

// isForYou — сообщение адресовано человеку: оно видно при любом фильтре.
export function isForYou(m: MeshMessage): boolean {
  return m.recipient_ids.includes('user')
}

// buildFeedTimeline сливает события стадии и сообщения её бесед по времени (при равенстве событие раньше) и
// применяет фильтр. Подряд идущие события группируются как обычно, подряд идущие сообщения — в один блок.
// Строка discussion_message, чей пост показан пузырём беседы, опускается: один пост — одна строка.
export function buildFeedTimeline(items: FeedItem[], messages: MeshMessage[], filter: FeedFilter): FeedTimelineEntry[] {
  const shownMessages = filter === 'author' ? messages.filter(isForYou) : messages
  const mirrored = new Set(shownMessages.map((m) => m.id))
  const shownItems = filter === 'conversations' ? [] : items.filter((it) => it.meshMessageId === undefined || !mirrored.has(it.meshMessageId))
  const entries: FeedTimelineEntry[] = []
  let run: FeedItem[] = []
  const flushItems = () => {
    for (const group of groupFeedItems(run)) entries.push({ kind: 'group', key: group.key, group })
    run = []
  }
  const pushMessage = (m: MeshMessage) => {
    flushItems()
    const last = entries[entries.length - 1]
    if (last?.kind === 'conversation') last.messages.push(m)
    else entries.push({ kind: 'conversation', key: `conversation:${m.id}`, messages: [m] })
  }
  let i = 0
  for (const m of shownMessages) {
    const ts = Date.parse(m.created_at)
    while (i < shownItems.length && !(ts < (shownItems[i] as FeedItem).ts)) run.push(shownItems[i++] as FeedItem)
    pushMessage(m)
  }
  run.push(...shownItems.slice(i))
  flushItems()
  return entries
}

// feedFilterCounts — how many rows each filter shows: counted from the timeline that filter builds, so a chip
// never promises more (or fewer) rows than the feed then shows.
export function feedFilterCounts(items: FeedItem[], messages: MeshMessage[]): Record<FeedFilter, number> {
  const count = (filter: FeedFilter) =>
    buildFeedTimeline(items, messages, filter).reduce((n, e) => n + (e.kind === 'group' ? e.group.items.length : e.messages.length), 0)
  return { all: count('all'), conversations: count('conversations'), author: count('author') }
}
