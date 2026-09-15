import type { Stage, StageStatus } from '../../types'

// WorkspaceView — какой единственный рабочий воркспейс показан справа от рейла
// стадий (плана §4). 'feed' — постоянный дефолт; 'cost' — постоянный вид учёта
// токенов/стоимости (increment 2); 'attention' — контекстный воркспейс текущего
// элемента, требующего действия (approval/question/failed/hook_failed/paused);
// 'plan-history'/'dialog-history' — read-only просмотр завершённого плана/диалога
// (rule 13: они НЕ считаются attention и не светятся).
export type WorkspaceView = 'feed' | 'cost' | 'attention' | 'plan-history' | 'dialog-history'

// AttentionKind — вид элемента, требующего действия пользователя. В отличие от
// use-attention (которое схлопывает failed/hook_failed в один 'failed' для
// заголовочной точки), здесь пять раздельных видов — под пять контекстных
// вкладок с собственными подписями (Approval/Question/Paused/Failed/Hook failed).
export type AttentionKind = 'approval' | 'question' | 'failed' | 'hook_failed' | 'paused'

// episode — идентификатор attention-эпизода (stage.updatedAt). Одна стадия за
// свою жизнь может входить в один и тот же вид attention несколько раз подряд
// (интерактивный диалог: q1 → ответ → q2, обе awaiting_user_input, но с разными
// updatedAt). Без episode в подписи reducer считал бы q2 тем же элементом, что и
// q1, и не авто-открывал бы его после возврата в Feed (Finding #1, раунд 3) —
// симметрично тому, как desktop-уведомления различают эпизоды по updatedAt.
export type AttentionItem = { stageId: string; kind: AttentionKind; episode: string }

// attentionKindForStatus — статус стадии → вид attention, либо null, если статус
// не требует действия. Единственная точка соответствия статус→вид.
export function attentionKindForStatus(status: StageStatus): AttentionKind | null {
  switch (status) {
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
    default:
      return null
  }
}

// deriveAttentionItems строит упорядоченную очередь attention из массива стадий.
// Порядок сохраняется как есть — сервер уже отдаёт stages в топологическом
// порядке (см. pkg/server/stageview.go's buildStageViews), поэтому rule 6
// ("order by the server-provided topological stage order") выполняется без
// повторной сортировки на клиенте.
export function deriveAttentionItems(stages: Stage[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const s of stages) {
    const kind = attentionKindForStatus(s.status)
    if (kind !== null) items.push({ stageId: s.id, kind, episode: s.updatedAt })
  }
  return items
}

// sig — стабильная подпись элемента attention (stageId+kind+episode). Одна стадия
// за свою жизнь может пройти несколько разных видов (paused → running → failed) —
// поэтому в подпись входит вид; и может ВОЙТИ В ТОТ ЖЕ вид повторно (диалог из
// нескольких вопросов) — поэтому в подпись входит episode (updatedAt): смена вида
// ИЛИ нового эпизода той же стадии = новый элемент (новое «прибытие»).
export function sig(item: AttentionItem): string {
  return `${item.stageId}:${item.kind}:${item.episode}`
}

export interface WorkspaceState {
  view: WorkspaceView
  items: AttentionItem[]
  // activeStageId — на каком элементе очереди сфокусирован воркспейс, когда
  // view === 'attention'. null — очередь пуста / attention не открыт.
  activeStageId: string | null
  // autoOpenedSignatures — подписи элементов, для которых авто-открытие уже
  // отработало (rule 9: авто-открывать РОВНО один раз на прибытие). Подпись,
  // чей элемент покинул очередь, забывается (см. syncItems) — чтобы повторное
  // появление той же стадии+вида снова считалось новым прибытием (симметрично
  // забыванию отвеченных вопросов в pollQuestions на бэкенде).
  autoOpenedSignatures: string[]
  // returnView — куда вернуться, когда закроется/разрешится attention.
  // Ограничен двумя ГЛОБАЛЬНЫМИ видами ('feed'/'cost') — history-виды
  // (plan-history/dialog-history) не подлежат восстановлению: они привязаны к
  // конкретной выбранной стадии, а не к постоянному воркспейсу. Записывается
  // ровно на переходе non-attention → attention (см. reduceSync) и обновляется
  // явной ручной навигацией (openFeed/openCost), в том числе во время attention.
  returnView: 'feed' | 'cost'
}

export const initialWorkspaceState: WorkspaceState = {
  view: 'feed',
  items: [],
  activeStageId: null,
  autoOpenedSignatures: [],
  returnView: 'feed',
}

export type WorkspaceAction =
  // sync — новый снимок очереди из polling. suppressed=true, когда пользователь
  // печатает / смотрит файл в оверлее — тогда прибывший элемент только светится,
  // но фокус не крадётся (rule 9).
  | { type: 'sync'; items: AttentionItem[]; suppressed: boolean }
  // openAttention — пользователь кликнул контекстную вкладку (опц. конкретный id).
  | { type: 'openAttention'; stageId?: string }
  // openFeed — пользователь вернулся на Feed.
  | { type: 'openFeed' }
  // openCost — пользователь открыл постоянный вид учёта стоимости (increment 2).
  | { type: 'openCost' }
  // openHistory — read-only просмотр плана/диалога завершённой стадии (rule 13).
  | { type: 'openHistory'; view: 'plan-history' | 'dialog-history' }

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

function reduceSync(state: WorkspaceState, items: AttentionItem[], suppressed: boolean): WorkspaceState {
  const prevSigs = new Set(state.items.map(sig))
  const currentSigs = new Set(items.map(sig))

  // Забываем подписи, покинувшие очередь: их повторное появление снова станет
  // "новым прибытием".
  const autoOpened = state.autoOpenedSignatures.filter((s) => currentSigs.has(s))

  let view = state.view
  let activeStageId = state.activeStageId
  let returnView = state.returnView

  // Резолюция: активный элемент исчез из очереди (rule 10) → переходим к
  // следующему оставшемуся; если очередь пуста — возвращаемся на returnView
  // (тот глобальный вид, откуда пришли в attention — feed или cost).
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

  // Авто-открытие ровно один раз на новое прибытие (rule 9). Прибытие — элемент,
  // чьей подписи не было в прошлой очереди и для которой авто-открытие ещё не
  // отработало. Помечаем прибытие обработанным ВСЕГДА (и при suppressed), чтобы
  // после снятия suppression оно не открылось задним числом — только светилось.
  const arrivals = items.filter((it) => !prevSigs.has(sig(it)) && !autoOpened.includes(sig(it)))
  const firstArrival = arrivals[0]
  if (firstArrival) {
    for (const a of arrivals) autoOpened.push(sig(a))
    if (!suppressed && view !== 'attention') {
      // Переход non-attention → attention: запоминаем returnView. History-виды
      // (plan-history/dialog-history) не восстанавливаемы — падаем на 'feed'.
      returnView = view === 'cost' ? 'cost' : 'feed'
      view = 'attention'
      activeStageId = firstArrival.stageId
    }
  }

  return { view, items, activeStageId, autoOpenedSignatures: autoOpened, returnView }
}

export function workspaceReducer(state: WorkspaceState, action: WorkspaceAction): WorkspaceState {
  switch (action.type) {
    case 'sync':
      return reduceSync(state, action.items, action.suppressed)
    case 'openAttention': {
      const first = state.items[0]
      if (!first) return state
      const target =
        action.stageId && state.items.some((it) => it.stageId === action.stageId)
          ? action.stageId
          : state.activeStageId && state.items.some((it) => it.stageId === state.activeStageId)
            ? state.activeStageId
            : first.stageId
      return { ...state, view: 'attention', activeStageId: target }
    }
    case 'openFeed':
      return { ...state, view: 'feed', returnView: 'feed' }
    case 'openCost':
      return { ...state, view: 'cost', returnView: 'cost' }
    case 'openHistory':
      return { ...state, view: action.view }
    default:
      return state
  }
}
