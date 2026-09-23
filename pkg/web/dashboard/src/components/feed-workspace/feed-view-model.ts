import type { AfmEvent, StageStatus } from '../../types'

// Семантическая презентация ленты: каждое РЕАЛЬНОЕ событие из events.jsonl
// превращается в FeedItem с ролью (agent/user/system → сторона left/right),
// тоном (neutral/success/warning/danger/accent) и текстом. Никаких выдуманных
// «сообщений агента»: текст и его классы 1:1 повторяют прежний EventFeedPanel
// (toFeedLine), меняется только группировка и раскладка (мессенджер), а порядок
// и идентичность событий сохраняются (план §6).

export type FeedActor = 'agent' | 'user' | 'system'
export type FeedTone = 'neutral' | 'success' | 'warning' | 'danger' | 'accent'
export type FeedItemKind = 'tool' | 'script' | 'status' | 'success' | 'message' | 'dialog'

export type FeedItem = {
  key: string
  stageId: string
  actor: FeedActor
  side: 'left' | 'right' // user → right, agent/system → left
  tone: FeedTone
  kind: FeedItemKind
  text: string
  gap: string // статичная длительность: разница с предыдущим событием ленты
  mono: boolean // tool/script/auto-answer — компактная mono-строка
  // dialog_question/dialog_answer: координаты вопроса диалога для последующего
  // перехода к нему (клик по item — задача другой таски, здесь только данные).
  phase?: string
  id?: string
  navigable?: boolean
  // markdown — рендерить text как markdown (заголовки/таблицы/жирный/код), а не как
  // сырую строку. Выставляется ТОЛЬКО для нарратива агента (agent_action tool="text"):
  // источник события знает его семантику. User-текст (заметки/ответы) остаётся plain,
  // чтобы введённые `*`/`#`/URL не меняли вид сообщения.
  markdown?: boolean
  // reportVerificationId — id verify-прохода (verification_id), только у
  // verify_result-строк, для которых событие несло непустой report_path
  // (см. V5a EventVerifyResult). Присутствие report_path — лишь СИГНАЛ "у
  // этого исхода есть report.md"; сам путь никогда не используется как URL —
  // ссылка на полный отчёт строится из stageId (уже известен) + этот id и
  // резолвится сервером (GET /api/stages/{id}/verify/{verificationId}/report,
  // V5b.3) — клиентский filesystem-путь никогда не доверяется.
  reportVerificationId?: string
}

// FeedGroup — последовательные items одной стороны и стадии, слитые визуально в
// один «пузырь» с общей шапкой (стадия + актор). Группировка чисто визуальная.
export type FeedGroup = {
  key: string
  side: 'left' | 'right'
  stageId: string
  actor: FeedActor
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

// codeFence computes a fence of backticks long enough that no run of
// backticks embedded in `content` (untrusted script/hook stderr) can
// prematurely close the block: CommonMark requires a closing fence to be AT
// LEAST as long as the opening one, so a stray ``` line in the middle of the
// tail must never be allowed to equal or exceed our chosen fence length.
// N = max(3, longestBacktickRun + 1).
function codeFence(content: string): string {
  const runs = content.match(/`+/g)
  const longest = runs === null ? 0 : Math.max(...runs.map((r) => r.length))
  return '`'.repeat(Math.max(3, longest + 1))
}

// neutralizeMarkers breaks `[AFM image: ...]` / `[AFM file: ...]` marker
// syntax in untrusted script/hook output (error text and stderr tail) so
// splitImageMarkers (feed-workspace) and the file-reference picker never
// treat attacker-controlled stderr as a real marker and fetch an image/file
// on the user's behalf. A zero-width space right after "[AFM" keeps the text
// readable while breaking the exact-match regexes the marker parsers use.
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
  // dedupeId — переопределяет стандартный ключ дедупликации (seq или
  // timestamp|type|stageId) там, где событие несёт свой собственный
  // стабильный идентификатор надёжнее timestamp (verify_started/
  // verify_result — verification_id+step, см. mapVerifyEvent).
  dedupeId?: string
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
      // Нарратив агента (tool="text") — чистая проза без "text:"-префикса и без
      // mono: это сообщение агента, а не вызов инструмента. Прочие инструменты
      // (Bash/Read/Write…) — компактная mono-строка "<tool>: <detail>".
      if (tool === 'text') {
        return { actor: 'agent', tone: 'neutral', kind: 'message', text: detail, mono: false, markdown: true }
      }
      return { actor: 'agent', tone: 'neutral', kind: 'tool', text: `${tool}${detail !== '' ? `: ${detail}` : ''}`, mono: true }
    }
    case 'script_output': {
      // stream различает stdout/stderr (Task 3-5, backend): stderr — тревожная
      // mono-строка (warning), как и обычный tool-вызов, но с явной пометкой
      // ":stderr", чтобы отличить от stdout в общей ленте script-стадии.
      // Отсутствующий stream (старые notices, записанные ДО этой правки) —
      // backward-compat трактуется как stdout, текст не меняется вовсе.
      const stream = str(obj.stream)
      if (stream === 'stderr') {
        return { actor: 'agent', tone: 'warning', kind: 'script', text: `[${str(obj.hook)}:stderr] ${str(obj.line)}`, mono: true }
      }
      return { actor: 'agent', tone: 'neutral', kind: 'script', text: `[${str(obj.hook)}] ${str(obj.line)}`, mono: true }
    }
    case 'script_failed': {
      // Итоговый сбой script-стадии (Task 3-5, backend): error — короткая
      // причина (напр. "exit status 1"), stderr_tail — хвост stderr для
      // диагностики. Тело рендерим как markdown с fenced code block —
      // сырой '\n' в plain-тексте схлопывается HTML-рендерингом, а
      // многострочный traceback обязан сохранить разбивку по строкам.
      //
      // error и tail — НЕДОВЕРЕННЫЙ вывод скрипта: neutralizeMarkers режет
      // [AFM image:]/[AFM file:] маркеры (иначе untrusted stderr мог бы
      // триггернуть фетч картинки/файла), а codeFence — динамический фенс
      // (N бэктиков > самого длинного забега бэктиков в tail), чтобы строка
      // ``` внутри хвоста не закрывала блок кода раньше времени.
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
      // stderr_tail (опционален, Task 3-5, backend): фенс-код-блок только
      // когда хвост реально есть (markdown:true, как у script_failed выше);
      // без хвоста — прежняя plain-строка БЕЗ markdown-рендера (сегодняшнее
      // поведение не меняется, markdown вообще не выставляется). error/tail
      // неблагонадёжны так же, как у script_failed выше — см. комментарий там.
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
      // Финальный сбой lifecycle (observer) хука — чисто информационное
      // предупреждение: FSM/стадию/ран оно не трогает (см. bus.go), поэтому
      // tone 'warning', а не 'danger', как у блокирующего hook_failed выше.
      return { actor: 'system', tone: 'warning', kind: 'status', text: `lifecycle hook ${str(obj.hook_id)} failed: ${str(obj.error)}`, mono: false }
    case 'verify_started':
    case 'verify_result':
      // AI-verify наблюдательные события (V5a): никогда не FSM-переход, стадия
      // продолжает жить своим статусом независимо от исхода verify — см.
      // mapVerifyEvent для разбора тонов (pass/needs_changes/inconclusive/
      // exec_error) и report_path → reportVerificationId.
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
      // Вытеснены dialog_question/dialog_answer (несут реальный текст вопроса/
      // ответа) — эти события больше не рендерим, чтобы не дублировать их пустой
      // заглушкой в ленте.
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
      // Заметка агенту (messenger-поле ленты / правка плана) — справа своим
      // текстом, как ответ на вопрос, но НЕ кликабельна (navigable опущен):
      // это доставленный юзером текст, а не переход к диалогу. id (seq перехода)
      // в payload нужен для дедупа live/replay, в презентацию не попадает.
      return { actor: 'user', tone: 'neutral', kind: 'message', text: str(obj.text), mono: false }
    case 'auto_answered':
      return { actor: 'system', tone: 'neutral', kind: 'dialog', text: `auto-answered ${str(obj.id)}: ${str(obj.answer)}`, mono: true }
    case 'context_warning':
      return { actor: 'system', tone: 'warning', kind: 'status', text: `context warning: ${str(data)}`, mono: false }
    default:
      return { actor: 'system', tone: 'neutral', kind: 'status', text: event.type, mono: false }
  }
}

// mapVerifyEvent — презентация AI-verify наблюдательных событий (V5a). Ни
// verify_started, ни verify_result НИКОГДА не меняют статус стадии — это
// ортогональный проход поверх уже "заявившей о готовности" стадии, поэтому
// тон здесь читается только как информационный сигнал, не как индикатор
// состояния FSM. Различаем три исхода verify_result:
//   - verdict: pass          → success (проверка прошла)
//   - verdict: needs_changes/inconclusive → warning (РЕЙТИНГ кода — агенту
//     есть что поправить/проверка не смогла решить; это ожидаемая, не
//     аварийная часть цикла, как и retry_scheduled выше)
//   - exec_error: *          → danger (ИСПОЛНЕНИЕ проверки не удалось —
//     таймаут/прерывание/сбой транспорта — качественно другая проблема,
//     чем "код не прошёл проверку", поэтому свой, более тревожный тон)
// reportVerificationId выставляется только когда payload нёс непустой
// report_path — это ЛИШЬ сигнал "у этого исхода есть report.md", сам путь
// никогда не используется как URL (см. FeedItem.reportVerificationId).
function mapVerifyEvent(type: 'verify_started' | 'verify_result', obj: Record<string, unknown>): Mapped {
  const step = typeof obj.step === 'number' ? obj.step : Number(str(obj.step))
  const command = str(obj.command)
  const verificationId = str(obj.verification_id)
  const dedupeId = `verify:${verificationId}:${step}:${type}`

  if (type === 'verify_started') {
    return { actor: 'system', tone: 'neutral', kind: 'status', text: `Verify step ${step} · ${command}`, mono: false, dedupeId }
  }

  const reason = str(obj.reason)
  const verdict = str(obj.verdict)
  const execError = str(obj.exec_error)
  const reportVerificationId = str(obj.report_path) !== '' ? verificationId : undefined

  if (verdict === 'pass') {
    return { actor: 'system', tone: 'success', kind: 'success', text: `Verify passed (step ${step}) · ${command}: ${reason}`, mono: false, reportVerificationId, dedupeId }
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

// Статичная длительность строки: разница между этим событием и предыдущим в
// ленте по порядку отображения (не per-стадийно). Нет предыдущего или
// невалидный timestamp — em dash. (Перенесено 1:1 из EventFeedPanel.)
export function formatEventGap(tsMs: number, prevTsMs: number): string {
  if (Number.isNaN(tsMs) || Number.isNaN(prevTsMs)) return '—'
  const diffSec = Math.max(0, Math.floor((tsMs - prevTsMs) / 1000))
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

// toFeedItems — события → FeedItem[] с уникальными стабильными ключами (тот же
// приём, что был в EventFeedPanel: seq либо `ts|type|stage`, при повторе —
// occurrence-суффикс `#N`, детерминированно при неизменном порядке).
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
      mono: m.mono,
      phase: m.phase,
      id: m.id,
      navigable: m.navigable,
      markdown: m.markdown,
      reportVerificationId: m.reportVerificationId,
    })
  })

  return items
}

// groupFeedItems сливает подряд идущие items одной стороны и стадии в один
// FeedGroup. Разрыв (новый пузырь) — при смене стороны/стадии/актора.
// Порядок items внутри группы сохраняется, идентичность (key) не теряется.
export function groupFeedItems(items: FeedItem[]): FeedGroup[] {
  const groups: FeedGroup[] = []
  for (const item of items) {
    const last = groups[groups.length - 1]
    if (last && last.side === item.side && last.stageId === item.stageId && last.actor === item.actor) {
      last.items.push(item)
    } else {
      groups.push({ key: item.key, side: item.side, stageId: item.stageId, actor: item.actor, items: [item] })
    }
  }
  return groups
}
