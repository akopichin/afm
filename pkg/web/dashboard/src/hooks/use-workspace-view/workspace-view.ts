import type { Stage, StageStatus } from '../../types'

// WorkspaceView — какой единственный рабочий воркспейс показан справа от рейла
// стадий (плана §4). 'feed' — постоянный дефолт; 'cost' — постоянный вид учёта
// токенов/стоимости (increment 2); 'full-feed' — постоянный вид ленты всего
// флоу целиком (не привязан к выбранной стадии); 'attention' — контекстный
// воркспейс текущего элемента, требующего действия
// (approval/question/failed/hook_failed/paused); 'plan-history'/'dialog-history'
// — read-only просмотр завершённого плана/диалога (rule 13: они НЕ считаются
// attention и не светятся).
export type WorkspaceView = 'feed' | 'cost' | 'full-feed' | 'attention' | 'plan-history' | 'dialog-history'

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
  // handledSignatures — подписи элементов, которые уже получили своё единственное
  // «показ» (авто-открыты, вручную открыты, либо показаны как активный attention).
  // handled = «этот элемент больше сам фокус не крадёт» (rule 9). Пока элемент
  // прибыл под suppression и ещё НЕ handled — он «отложен»: только светится
  // beacon'ом и авто-откроется, когда suppression снимут. Подпись, чей элемент
  // покинул очередь, забывается (см. reduceSync) — чтобы повторное появление той
  // же стадии+вида снова считалось новым прибытием (симметрично забыванию
  // отвеченных вопросов в pollQuestions на бэкенде).
  handledSignatures: string[]
  // returnView — куда вернуться, когда закроется/разрешится attention.
  // Ограничен ГЛОБАЛЬНЫМИ видами ('feed'/'cost'/'full-feed') — history-виды
  // (plan-history/dialog-history) не подлежат восстановлению: они привязаны к
  // конкретной выбранной стадии, а не к постоянному воркспейсу. Записывается
  // ровно на переходе non-attention → attention (см. reduceSync) и обновляется
  // явной ручной навигацией (openFeed/openCost/openFullFeed), в том числе во
  // время attention.
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
  // sync — новый снимок очереди из polling. suppressed=true, когда пользователь
  // печатает / смотрит файл в оверлее / идёт owned-pause операция — тогда
  // прибывший элемент только светится, а auto-open ОТКЛАДЫВАЕТСЯ до снятия
  // suppression (элемент остаётся не-handled), а не сгорает навсегда (rule 9).
  // ownedHandledSigs — подписи self-owned paused эпизодов (пользователь сам
  // поставил паузу через фокус композера). Их auto-open НЕ откладывается, а
  // подавляется навсегда (помечаются handled сразу), иначе после снятия владения
  // до refresh /api/status устаревший paused всплыл бы ложной панелью.
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
// ГЛОБАЛЬНЫЕ виды ('cost'/'full-feed') сохраняются как есть — именно туда и
// нужно вернуться; любой НЕ глобальный вид (history) падает на 'feed', потому
// что history привязана к конкретной стадии, а не к постоянному воркспейсу.
function globalReturnView(view: WorkspaceView): 'feed' | 'cost' | 'full-feed' {
  return view === 'cost' || view === 'full-feed' ? view : 'feed'
}

// retainSigs оставляет из набора подписей только те, чей элемент ещё в очереди.
// Разрешённые/исчезнувшие подписи забываются, чтобы повторное появление стало
// новым прибытием. Единственная точка фильтрации sig-множества по items.
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

  // 2. Резолюция/advance: активный элемент исчез из очереди (rule 10) → переходим
  // к следующему оставшемуся; если очередь пуста — возвращаемся на returnView.
  // Ветка НЕ зависит от suppressed: intra-attention advance не замораживается
  // (иначе attention-панель показала бы пустоту). Пометку handled активного
  // элемента делает единое правило шага 5, а не сам advance.
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

  // 3a. Self-owned paused эпизоды — не «действие, требующее внимания» (пользователь
  // сам поставил паузу). Помечаем handled СРАЗУ и никогда не авто-открываем,
  // независимо от suppressed. Так после clearOp (handleSend снимает владение до
  // refresh /api/status) устаревший A/paused уже handled и не всплывёт ложной
  // панелью — восстанавливает гарантию прежнего consume-on-suppress. Beacon при
  // этом остаётся: элемент по-прежнему в items.
  const ownedSet = new Set(ownedHandledSigs)
  for (const c of candidates) {
    if (ownedSet.has(sig(c))) handled.push(sig(c))
  }
  const openable = candidates.filter((it) => !ownedSet.has(sig(it)))
  const first = openable[0]

  // 4. При suppressed НИЧЕГО не делаем: кандидаты остаются не-handled и просто
  // светятся beacon'ом — auto-open откладывается. При suppressed=false помечаем
  // все открываемые кандидаты handled и (если ещё не в attention) открываем
  // первый, записывая returnView В МОМЕНТ ОТКРЫТИЯ (не в момент arrival).
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

  // 5. Единое правило «активный attention = показан = handled». Активный элемент,
  // найденный по ФИНАЛЬНЫМ локалам (не по устаревшему state), всегда handled —
  // независимо от suppressed: он по определению показан в панели. Покрывает
  // первичное открытие, advance-к-следующему и in-place смену эпизода той же
  // стадии (activeStageId резолвится по stageId, поэтому A/t1 → A/t2 даёт новую
  // подпись t2, которая иначе осталась бы unseen и после openFeed+release
  // ошибочно переоткрылась бы). Заменяет частный consume-при-advance.
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
      // Ручное открытие считает ФАКТИЧЕСКИ выбранный (после fallback) элемент
      // handled — по полной подписи (у стадии может быть несколько эпизодов),
      // чтобы после возврата в Feed он не украл фокус повторно.
      const selected = state.items.find((it) => it.stageId === target)
      const handledSignatures =
        selected && !state.handledSignatures.includes(sig(selected))
          ? [...state.handledSignatures, sig(selected)]
          : state.handledSignatures
      // Вход из non-attention фиксирует returnView так же, как auto-entry в
      // reduceSync (иначе beacon из history вернул бы в устаревший cost). Уже
      // будучи в attention — не трогаем.
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
    default:
      return state
  }
}
