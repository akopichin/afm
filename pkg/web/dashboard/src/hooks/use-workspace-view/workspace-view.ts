import type { Stage } from '../../types'

// WorkspaceView — какой единственный рабочий воркспейс показан справа от рейла стадий. 'assistant' — лента
// одной выбранной задачи помощника (Этап 3, T3.10); как и history-виды, это read-only ручной выбор, а не
// часть attention-очереди — завершение/появление помощника никогда не открывает его автоматически.
// 'team' (Agent Mesh, T7.3) — the Team view: run-wide roster (participants + conversations), gated behind
// the mesh capability. Same manual posture as 'assistant': entered only via openTeam, never
// part of the attention auto-open queue.
export type WorkspaceView = 'feed' | 'cost' | 'full-feed' | 'attention' | 'plan-history' | 'dialog-history' | 'assistant' | 'team'

// AttentionKind — вид элемента, требующего действия пользователя. 'assistants' (Этап 7, T7.9) — стадия в
// awaiting_assistants, чей required-гейт требует решения человека (см. attentionKindForStatus ниже). NB: это
// отдельный, вручную синхронизируемый список от hooks/use-attention's AttentionKind (та же, принятая ранее
// дупликация — см. AGENTS.md) — там failed/hook_failed схлопнуты в один 'failed', здесь разделены.
export type AttentionKind = 'approval' | 'question' | 'failed' | 'hook_failed' | 'paused' | 'assistants'

// episode — идентификатор attention-эпизода.
export type AttentionItem = { stageId: string; kind: AttentionKind; episode: string }

// attentionKindForStatus — стадия → вид attention, либо null, если она не требует действия. Единственная
// точка соответствия стадия→вид в этом модуле. Принимает ВСЮ стадию (не только статус): awaiting_assistants
// сам по себе ничего не решает — требуется ещё awaitingAssistantsBlocked (T7.9: «помощник ещё работает» —
// это обычный фон, не attention; «нужно решение человека» — attention).
export function attentionKindForStatus(stage: Stage): AttentionKind | null {
  switch (stage.status) {
    case 'awaiting_approval':
      return 'approval'
    case 'awaiting_user_input':
      return 'question'
    case 'failed':
      return 'failed'
    case 'hook_failed':
      return 'hook_failed'
    case 'paused':
      return 'paused'
    case 'awaiting_assistants':
      return stage.awaitingAssistantsBlocked === true ? 'assistants' : null
    default:
      return null
  }
}

// deriveAttentionItems строит упорядоченную очередь attention из массива стадий.
export function deriveAttentionItems(stages: Stage[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const s of stages) {
    const kind = attentionKindForStatus(s)
    if (kind !== null) items.push({ stageId: s.id, kind, episode: s.updatedAt })
  }
  return items
}

// sig — стабильная подпись элемента attention.
export function sig(item: AttentionItem): string {
  return `${item.stageId}:${item.kind}:${item.episode}`
}

export interface WorkspaceState {
  view: WorkspaceView
  items: AttentionItem[]
  // activeStageId — на каком элементе очереди сфокусирован воркспейс, когда
  // view === 'attention'. null — очередь пуста / attention не открыт.
  activeStageId: string | null
  // handledSignatures — подписи элементов, которые уже получили своё единственное «показ». handled = «этот
  // элемент больше сам фокус не крадёт» (rule 9).
  handledSignatures: string[]
  // returnView — куда вернуться, когда закроется/разрешится attention.
  returnView: 'feed' | 'cost' | 'full-feed'
}

export const initialWorkspaceState: WorkspaceState = {
  view: 'feed',
  items: [],
  activeStageId: null,
  handledSignatures: [],
  returnView: 'feed',
}

export type WorkspaceAction =
  // sync — новый снимок очереди из polling. ownedHandledSigs — подписи self-owned paused эпизодов
  // (пользователь сам поставил паузу через фокус композера).
  | { type: 'sync'; items: AttentionItem[]; suppressed: boolean; ownedHandledSigs?: string[] }
  // openAttention — пользователь кликнул контекстную вкладку (опц. конкретный id).
  | { type: 'openAttention'; stageId?: string }
  // openFeed — пользователь вернулся на Feed.
  | { type: 'openFeed' }
  // openCost — пользователь открыл постоянный вид учёта стоимости (increment 2).
  | { type: 'openCost' }
  // openFullFeed — пользователь открыл ленту всего флоу целиком.
  | { type: 'openFullFeed' }
  // openHistory — read-only просмотр плана/диалога завершённой стадии (rule 13).
  | { type: 'openHistory'; view: 'plan-history' | 'dialog-history' }
  // openAssistant — пользователь выбрал в рейле задачу помощника (какую именно — хранит Selection на
  // уровне App, не этот редьюсер); тот же паттерн, что и openHistory — ручной read-only вид, не связанный с
  // attention-очередью.
  | { type: 'openAssistant' }
  // openTeam — пользователь открыл вкладку Team (Agent Mesh, T7.3): run-wide ручной read-only-ish вид (свой
  // композер на беседу), как и openAssistant — не часть attention-очереди, НЕ привязан к выбранной стадии.
  | { type: 'openTeam' }

// activeItem возвращает элемент, на котором сейчас сфокусирован attention, либо
// null. Учитывает и view: в history/feed активного attention-элемента нет.
export function activeItem(state: WorkspaceState): AttentionItem | null {
  if (state.view !== 'attention' || state.activeStageId === null) return null
  return state.items.find((it) => it.stageId === state.activeStageId) ?? null
}

// countByKind — сколько нерешённых элементов каждого вида (для счётчика "· N"
// на контекстной вкладке).
export function countByKind(items: AttentionItem[], kind: AttentionKind): number {
  return items.reduce((n, it) => (it.kind === kind ? n + 1 : n), 0)
}

// globalReturnView — маппинг текущего вида на returnView при входе в attention.
function globalReturnView(view: WorkspaceView): 'feed' | 'cost' | 'full-feed' {
  return view === 'cost' || view === 'full-feed' ? view : 'feed'
}

// retainSigs оставляет из набора подписей только те, чей элемент ещё в очереди. Разрешённые/исчезнувшие
// подписи забываются, чтобы повторное появление стало новым прибытием.
function retainSigs(sigs: string[], items: AttentionItem[]): string[] {
  const present = new Set(items.map(sig))
  return sigs.filter((s) => present.has(s))
}

function reduceSync(
  state: WorkspaceState,
  items: AttentionItem[],
  suppressed: boolean,
  ownedHandledSigs: string[] = [],
): WorkspaceState {
  // 1. Забываем подписи, покинувшие очередь.
  const handled = retainSigs(state.handledSignatures, items)

  let view = state.view
  let activeStageId = state.activeStageId
  let returnView = state.returnView

  // 2. Резолюция/advance: активный элемент исчез из очереди (rule 10) → переходим к следующему
  // оставшемуся; если очередь пуста — возвращаемся на returnView.
  if (view === 'attention') {
    const stillActive = items.some((it) => it.stageId === activeStageId)
    if (!stillActive) {
      const next = items[0]
      if (next) {
        activeStageId = next.stageId
      } else {
        view = returnView
        activeStageId = null
      }
    }
  }

  // 3. Кандидаты — элементы очереди (в их порядке), чья подпись ещё не handled:
  // отложенные ранее под suppression И новоприбывшие, единым множеством.
  const candidates = items.filter((it) => !handled.includes(sig(it)))

  // 3a. Self-owned paused эпизоды — не «действие, требующее внимания» (пользователь сам поставил паузу).
  const ownedSet = new Set(ownedHandledSigs)
  for (const c of candidates) {
    if (ownedSet.has(sig(c))) handled.push(sig(c))
  }
  const openable = candidates.filter((it) => !ownedSet.has(sig(it)))
  const first = openable[0]

  // 4. При suppressed НИЧЕГО не делаем: кандидаты остаются не-handled и просто светятся beacon'ом —
  // auto-open откладывается.
  if (first && !suppressed) {
    for (const c of openable) handled.push(sig(c))
    if (view !== 'attention') {
      // Переход non-attention → attention: запоминаем returnView. History-виды
      // (plan-history/dialog-history) не восстанавливаемы — падают на 'feed'.
      returnView = globalReturnView(view)
      view = 'attention'
      activeStageId = first.stageId
    }
  }

  // 5. Единое правило «активный attention = показан = handled».
  if (view === 'attention' && activeStageId !== null) {
    const active = items.find((it) => it.stageId === activeStageId)
    if (active && !handled.includes(sig(active))) handled.push(sig(active))
  }

  return { view, items, activeStageId, handledSignatures: handled, returnView }
}

export function workspaceReducer(state: WorkspaceState, action: WorkspaceAction): WorkspaceState {
  switch (action.type) {
    case 'sync':
      return reduceSync(state, action.items, action.suppressed, action.ownedHandledSigs ?? [])
    case 'openAttention': {
      const first = state.items[0]
      if (!first) return state
      const target =
        action.stageId && state.items.some((it) => it.stageId === action.stageId)
          ? action.stageId
          : state.activeStageId && state.items.some((it) => it.stageId === state.activeStageId)
            ? state.activeStageId
            : first.stageId
      // Ручное открытие считает ФАКТИЧЕСКИ выбранный элемент handled — по полной подписи, чтобы после
      // возврата в Feed он не украл фокус повторно.
      const selected = state.items.find((it) => it.stageId === target)
      const handledSignatures =
        selected && !state.handledSignatures.includes(sig(selected))
          ? [...state.handledSignatures, sig(selected)]
          : state.handledSignatures
      // Вход из non-attention фиксирует returnView так же, как auto-entry в reduceSync (иначе beacon из
      // history вернул бы в устаревший cost). Уже будучи в attention — не трогаем.
      const returnView = state.view === 'attention' ? state.returnView : globalReturnView(state.view)
      return { ...state, view: 'attention', activeStageId: target, handledSignatures, returnView }
    }
    case 'openFeed':
      return { ...state, view: 'feed', returnView: 'feed' }
    case 'openCost':
      return { ...state, view: 'cost', returnView: 'cost' }
    case 'openFullFeed':
      return { ...state, view: 'full-feed', returnView: 'full-feed' }
    case 'openHistory':
      return { ...state, view: action.view }
    case 'openAssistant':
      return { ...state, view: 'assistant' }
    case 'openTeam':
      return { ...state, view: 'team' }
    default:
      return state
  }
}
