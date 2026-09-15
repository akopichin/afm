import { useEffect, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, listNotes, pauseStage, reviseStage, setStageNote, triggerStageButton } from '../api/run-client'
import { GlobalHeader } from '../components/global-header'
import { StagesList } from '../components/stages-list'
import { AgentNoteModal } from '../components/agent-note-modal'
import { ReviewNotesModal } from '../components/review-notes-modal'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { FeedWorkspace } from '../components/feed-workspace'
import { CostPanel } from '../components/cost-panel'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardShell } from '../components/layout/DashboardShell'
import { WorkspaceTabs, WorkspaceHeader, AttentionBanner, type WorkspaceTabDescriptor } from '../components/workspace'
import { FileBrowserProvider } from '../components/file-browser'
import { ReviewBanner } from '../components/review-banner'
import { useStatus } from '../hooks/use-status'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageLog } from '../hooks/use-stage-log'
import { useElapsed } from '../hooks/use-elapsed'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
import { anyAwaiting } from '../hooks/use-attention'
import { attentionKindForStatus, countByKind, useWorkspaceView, type AttentionKind } from '../hooks/use-workspace-view'
import { useTitleFlash } from '../hooks/use-title-flash'
import { useFaviconPulse } from '../hooks/use-favicon-pulse'
import { useDesktopNotifications } from '../hooks/use-desktop-notifications'
import { ACTIVE_STAGE_STATUSES, SIGNIFICANT_EVENT_TYPES } from '../types'

// Подписи контекстной вкладки воркспейса по виду attention (Approval/Question/…).
const ATTENTION_TAB_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval',
  question: 'Question',
  failed: 'Failed',
  hook_failed: 'Hook failed',
  paused: 'Paused',
}

// Подпись шортката к ждущему действию в шапке файлового оверлея (Finding #4).
const ATTENTION_SHORTCUT_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval waiting',
  question: 'Question waiting',
  failed: 'Stage failed',
  hook_failed: 'Hook failed',
  paused: 'Stage paused',
}

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
// Владеет состоянием выбора текущей стадии; WebSocket работает как канал обновления
// состояния — по значимым событиям ре-запрашивает /api/status.
export function App(): ReactElement {
  const { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince, capabilities, flowPauseState, flowPausedStages, runCost, runOverheadCost, coverageIssues, accounting, refresh } = useStatus()

  // Модалка ревью-раунда (Task 23) — открывается по клику ReviewBanner's "Send
  // notes". Держит только флаг открытия: сам список заметок/выбор стадии
  // модалка загружает и владеет собой (listNotes() на маунте).
  const [reviewModalOpen, setReviewModalOpen] = useState(false)

  // Стадия, для которой сейчас открыта модалка «Добавить поправку агенту»
  // (agent_suggest, Task 8); null — модалка скрыта.
  const [noteModalStageId, setNoteModalStageId] = useState<string | null>(null)

  // Стадия, для которой открыта модалка pre-note (заметка ДО старта, только
  // pending-стадии); null — модалка скрыта. Отдельно от noteModalStageId:
  // другой вариант модалки (префилл + пустой=удалить), другой обработчик.
  const [preNoteModalStageId, setPreNoteModalStageId] = useState<string | null>(null)

  async function handleSubmitPreNote(note: string): Promise<void> {
    if (preNoteModalStageId === null) return
    try {
      await setStageNote(preNoteModalStageId, note)
      setPreNoteModalStageId(null)
    } catch (err) {
      // Как handleSubmitNote: не закрываем молча — оставляем модалку открытой
      // с введённым текстом, чтобы пользователь мог повторить (стадия могла
      // уже стартовать, либо сеть отвалилась).
      console.error('Failed to save pre-note:', err)
    }
  }

  async function handleSubmitNote(note: string): Promise<void> {
    if (noteModalStageId === null) return

    try {
      await reviseStage(noteModalStageId, note)
      setNoteModalStageId(null)
    } catch (err) {
      // Стадия могла уйти из ожидаемого статуса за время, пока юзер печатал
      // (или сеть отвалилась) — не закрываем модалку молча, будто заметка
      // ушла: оставляем noteModalStageId как есть, юзер видит модалку с
      // введённым текстом всё ещё открытой и может повторить попытку.
      // AgentNoteModal.onSubmit — не-async проп (вызывается без await из
      // onClick), поэтому здесь обязателен свой catch, а не пробрасывание
      // наверх как unhandled rejection.
      console.error('Failed to submit agent note:', err)
    }
  }

  function handlePause(stageId: string): void {
    // StagesList закрывает кебаб синхронно ДО вызова onPause, так что здесь
    // нет модалки/состояния, которое надо откатывать при ошибке (в отличие
    // от handleSubmitNote выше) — но onPause всё равно не-async проп,
    // вызывается без await из onClick, поэтому свой catch обязателен, иначе
    // отказ pauseStage (сеть, либо стадия успела уйти из pausable-статуса
    // между открытием меню и кликом) уйдёт в unhandled rejection молча.
    pauseStage(stageId).catch((err: unknown) => {
      console.error('Failed to pause stage:', err)
    })
  }

  function handleButton(stageId: string, name: string): void {
    // Fire-immediately, без модалки: StagesList закрывает кебаб синхронно ДО
    // вызова onButton. Как и handlePause, onButton — не-async проп, поэтому
    // свой catch обязателен, иначе отказ triggerStageButton (сеть, либо стадия
    // успела уйти из running/awaiting_approval между открытием меню и кликом)
    // уйдёт в unhandled rejection молча.
    triggerStageButton(stageId, name).catch((err: unknown) => {
      console.error('Failed to trigger stage button:', err)
    })
  }

  // Число собранных ревью-заметок для ReviewBanner (Task 22) — реальный счётчик
  // через listNotes(), а не заглушка: только пока флоу реально на review-паузе
  // (flowPauseState === 'paused'), с тем же интервалом опроса, что и useStatus,
  // чтобы не заводить отдельный WS/событийный канал ради одного числа. Вне
  // паузы опрос не идёт и счётчик сбрасывается — баннер всё равно не рендерит
  // текст с числом заметок ни в 'none' (не рендерится вовсе), ни в 'resuming'
  // (фиксированный текст "Resuming…" без счётчика).
  const [reviewNoteCount, setReviewNoteCount] = useState(0)
  useEffect(() => {
    if (flowPauseState !== 'paused') {
      setReviewNoteCount(0)
      return
    }

    let cancelled = false
    const load = () => {
      listNotes()
        .then(({ notes }) => {
          if (!cancelled) setReviewNoteCount(notes.length)
        })
        .catch(() => {
          /* сеть отвалилась — оставляем предыдущее значение, следующий тик повторит */
        })
    }

    load()
    const timer = setInterval(load, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [flowPauseState])

  // onCancel баннера отменяет раунд ревью без доставки заметок (Task 19's
  // cancelNotes) — не требует выбора целевой стадии, поэтому можно вызвать
  // напрямую отсюда, тем же fire-and-forget-с-catch приёмом, что и handlePause
  // выше (onCancel — не-async проп кнопки).
  function handleReviewCancel(): void {
    cancelNotes().catch((err: unknown) => {
      console.error('Failed to cancel review notes:', err)
    })
  }

  // onSend, в отличие от onCancel, не может просто вызвать injectNotes здесь:
  // тому нужен id целевой стадии, которую пока выбирает пользователь — этим
  // занимается ReviewNotesModal (список заметок + выбор стадии + сам
  // injectNotes), эта функция лишь её открывает.
  function handleReviewSend(): void {
    setReviewModalOpen(true)
  }

  const wsUrl = buildWebSocketUrl()
  const { events, connected } = useEventFeed(wsUrl)

  const [selectedStageId, setSelectedStageId] = useState<string | null>(null)

  // Единый workspace-view-редьюсер — источник истины для того, ЧТО показано
  // справа от рейла: Feed, контекстный attention (approval/question/failed/
  // hook_failed/paused) или read-only просмотр истории плана/диалога. Он же
  // держит очередь attention (топологический порядок сервера), авто-открывает
  // новое ожидание ровно один раз (rule 9, с suppression при наборе текста),
  // продвигается к следующему при разрешении и возвращается в Feed, когда
  // очередь пуста (rule 10). Раньше это подменялось локальными
  // selectedStageId+activeTab, из-за чего ожидание на НЕ выбранной стадии не
  // всплывало — action могло «потеряться».
  // suppression авто-открытия: пользователь печатает ИЛИ открыт ЛЮБОЙ оверлей
  // поверх воркспейса — файловый браузер (Finding #4 р2), заметка агенту/pre-note
  // или модалка ревью (Finding #5 р3). Новое ожидание не должно авто-открываться
  // за непрозрачной модалкой; оно только светится (маяк-вкладка/шорткат в шапке
  // модалки). Сами модальные состояния объявлены выше по файлу.
  const [filesOpen, setFilesOpen] = useState(false)
  const anyModalOpen = filesOpen || noteModalStageId !== null || preNoteModalStageId !== null || reviewModalOpen
  const editing = useIsEditing() || anyModalOpen
  const { state: wsState, activeItem: attnItem, openFeed, openCost, openAttention, openHistory } = useWorkspaceView(stages, editing)

  // FIX 3 (round-5 #6 спеки): активация Cost из поповера «⋯» шапки должна
  // довести фокус до САМОЙ вкладки Cost воркспейса, а не оставлять его на
  // «⋯» — RunMetrics сам возвращает фокус туда лишь как промежуточный
  // стабильный узел (кнопка, в отличие от активированного тайла, никуда не
  // денется из DOM), дальнейший перенос — забота App.tsx, единственного, кто
  // знает о существовании WorkspaceTabs. Инлайновая активация тайла (fromPopover
  // =false) поведение не меняет — там фокус и так остаётся на месте клика.
  const [costFocusPending, setCostFocusPending] = useState(false)
  function handleOpenCost(fromPopover: boolean): void {
    openCost()
    if (fromPopover) setCostFocusPending(true)
  }
  useEffect(() => {
    if (!costFocusPending || wsState.view !== 'cost') return
    document.querySelector<HTMLElement>('.workspace-tabs [data-tab-id="cost"]')?.focus()
    setCostFocusPending(false)
  }, [costFocusPending, wsState.view])

  // Стадия, о которой сейчас говорит воркспейс: в attention-режиме — активный
  // элемент очереди (он может отличаться от того, что вручную выбрано в рейле —
  // именно «отделить active attention stage от обычной выбранной стадии»);
  // иначе — выбранная в рейле стадия.
  const workspaceStageId = wsState.view === 'attention' && attnItem !== null ? attnItem.stageId : selectedStageId
  const workspaceStage = stages.find((stage) => stage.id === workspaceStageId) ?? null

  // Держим selectedStageId в согласии с авто-фокусом attention: когда редьюсер
  // сам открыл/продвинул ожидание, рейл-выбор следует за ним, чтобы после
  // разрешения (возврат в Feed) контекст остался на той стадии, что смотрели.
  useEffect(() => {
    if (wsState.view === 'attention' && attnItem !== null) {
      setSelectedStageId(attnItem.stageId)
    }
  }, [wsState.view, attnItem?.stageId])

  // Клик по стадии в рейле (или по desktop-уведомлению): выбираем её и открываем
  // подходящий вид — attention, если стадия ждёт действия; иначе read-only
  // историю плана/диалога, если есть; иначе Feed. openAttention/openHistory/
  // openFeed — действия того же редьюсера, поэтому ручной выбор и авто-очередь
  // не расходятся (единая state machine, без параллельной вкладочной).
  function handleSelectStage(stageId: string): void {
    setSelectedStageId(stageId)
    const stage = stages.find((s) => s.id === stageId) ?? null
    const kind = stage === null ? null : attentionKindForStatus(stage.status)
    if (kind !== null) openAttention(stageId)
    else if (stage?.showDialog === true) openHistory('dialog-history')
    else if (stage?.showPlan === true) openHistory('plan-history')
    else openFeed()
  }

  // anyAttention (любая стадия прогона ждёт) — точка в шапке И пульс favicon.
  const anyAttention = anyAwaiting(stages)
  // Title flash завязан на ГЛОБАЛЬНОЕ непросмотренное ожидание, а не только на
  // текущую workspace-стадию (Finding #1 второго раунда): мигаем, когда в
  // очереди есть ожидание, но воркспейс сейчас НЕ в attention-виде (пользователь
  // в Feed/истории/печатает — ожидание не должно потеряться). Когда он реально
  // смотрит attention — не мигаем (он уже занят действием).
  useTitleFlash(wsState.items.length > 0 && wsState.view !== 'attention')
  useFaviconPulse(anyAttention)
  const {
    enabled: notificationsEnabled,
    permission: notificationsPermission,
    requestEnable: onRequestEnableNotifications,
    disable: onDisableNotifications,
  } = useDesktopNotifications(stages, handleSelectStage)

  // Заголовок вкладки берём из description флоу (из flow.yaml), иначе имя флоу,
  // иначе дефолт. useTitleFlash мигает вокруг текущего title и восстанавливает
  // его, поэтому базовый заголовок можно менять здесь без конфликта.
  useEffect(() => {
    document.title = (description ?? '').trim() || flowName || 'afm Dashboard'
  }, [description, flowName])

  // showPlan/showDialog capabilities are computed server-side per stage (see
  // pkg/server/stageview.go's StageView.ShowPlan/ShowDialog) — the client only
  // adds the "nothing selected → show both, neutral state" rule on top.
  const showPlan = workspaceStage === null || workspaceStage.showPlan
  const showDialog = workspaceStage === null || workspaceStage.showDialog

  const logEntries = useStageLog(workspaceStageId)
  const elapsedMs = useElapsed(startedAt)
  const idleMs = useIdleMs(idleAccumulatedMs, idleSince, connected)
  const backoffMs = useBackoffMs(backoffAccumulatedMs, backoffOpenSince, connected)

  const lastRefreshedEvent = useRef<object | null>(null)

  // Отслеживаем, была ли ТЕКУЩАЯ выбранная стадия хоть раз замечена «в работе»
  // (не done) под этим же выбором — отличает «пользователь выбрал уже
  // завершённую стадию, чтобы посмотреть план/лог» (не трогаем выбор) от
  // «стадия, за которой мы следим, завершилась» (нужно продвинуться дальше).
  // Живёт per-selection: сбрасывается при каждой смене selectedStageId, а не
  // при каждом опросе — иначе не отличить эти два случая.
  const watchingId = useRef<string | null>(null)
  const wasLive = useRef(false)

  // Автовыбор активной стадии (иначе первая failed); продвижение к следующей активной,
  // пока стадия, за которой мы следим, done. Ручной выбор уже завершённой стадии не
  // перекидывает пользователя — иначе во время работы флоу нельзя открыть логи/план/
  // диалог завершённого стейджа (он мгновенно «убегает»).
  //
  // Раньше продвижение проверялось ОДИН РАЗ — ровно в тот тик, когда выбранная
  // стадия переходила !done→done. На скриптовых стейджах (Stage.IsScript(),
  // running может длиться доли секунды) несколько стадий подряд успевают
  // полностью пройти running→done МЕЖДУ двумя опросами /api/status — к моменту,
  // когда фронтенд наконец видит «стадия1 стала done», стадия2 уже тоже done, и
  // среди ACTIVE_STAGE_STATUSES искать нечего. Прежний код на этом сдавался
  // навсегда (тот самый единственный тик уже прошёл) — выбор залипал на
  // стадии1, хотя реально уже работает стадия3/4. Теперь поиск следующей
  // активной стадии повторяется на КАЖДОМ опросе, пока выбранная стадия done и
  // wasLive — самокорректируется в течение одного цикла опроса вместо
  // необратимого залипания.
  useEffect(() => {
    if (stages.length === 0) return

    const current = stages.find((stage) => stage.id === selectedStageId) ?? null

    if (current === null) {
      const active = stages.find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status))
      const failed = stages.find((stage) => stage.status === 'failed')
      const next = active ?? failed ?? null

      if (next !== null) {
        setSelectedStageId(next.id)
      }

      return
    }

    if (watchingId.current !== selectedStageId) {
      watchingId.current = selectedStageId
      wasLive.current = current.status !== 'done'
    } else if (current.status !== 'done') {
      wasLive.current = true
    }

    if (wasLive.current && current.status === 'done') {
      const fromIndex = stages.findIndex((stage) => stage.id === selectedStageId)
      const nextActive = stages.slice(fromIndex + 1).find((stage) => ACTIVE_STAGE_STATUSES.has(stage.status)) ?? null

      if (nextActive !== null) {
        setSelectedStageId(nextActive.id)
      }
    }
  }, [stages, selectedStageId])

  // WebSocket как канал обновления: значимое событие → ре-запрос состояния флоу.
  useEffect(() => {
    if (events.length === 0) return

    // Идентичность по ССЫЛКЕ на последнее событие, а не по events.length: лента
    // капается в MAX_EVENTS=200 (use-event-feed), после чего length навсегда
    // остаётся 200, и старое сравнение `refreshedForEvent === events.length`
    // (200 === 200) возвращалось раньше НАВСЕГДА — WS-канал обновления статуса
    // тихо умирал на длинных ранах, оставляя только тротлящийся в фоне 3s-poll.
    // use-event-feed добавляет новый объект события в конец на каждое
    // поступление, поэтому ссылка на последний элемент меняется ровно тогда,
    // когда пришло новое событие. См. App.test.tsx "WS refresh survives past
    // the 200-event feed cap".
    const latest = events[events.length - 1]
    if (latest === undefined || latest === lastRefreshedEvent.current) return

    lastRefreshedEvent.current = latest
    if (SIGNIFICANT_EVENT_TYPES.has(latest.type)) {
      refresh()
    }
  }, [events, refresh])

  // Очередь attention (топологический порядок сервера) и позиция активного
  // элемента — для навигации prev/next между несколькими стадиями, ждущими
  // действия (rule 6). Очередь ведёт сам редьюсер (wsState.items).
  const attnItems = wsState.items
  const attnIndex = attnItem === null ? -1 : attnItems.findIndex((it) => it.stageId === attnItem.stageId)
  function goAttention(delta: number): void {
    if (attnItems.length === 0) return
    const base = attnIndex >= 0 ? attnIndex : 0
    const next = attnItems[(base + delta + attnItems.length) % attnItems.length]
    if (next) {
      setSelectedStageId(next.stageId)
      openAttention(next.stageId)
    }
  }

  const inAttention = wsState.view === 'attention'
  const contextKind = workspaceStage === null ? null : attentionKindForStatus(workspaceStage.status)

  // Первый нерешённый элемент очереди (для маяка/шортката). В attention-виде это
  // активный элемент; иначе — голова очереди.
  const attentionTabItem = attnItem ?? attnItems[0] ?? null

  // Шорткат к ждущему действию для шапки файлового оверлея (Finding #4): первый
  // нерешённый элемент очереди; клик закрывает Files и открывает его attention.
  const attentionShortcut = attentionTabItem !== null
    ? {
        label: ATTENTION_SHORTCUT_LABEL[attentionTabItem.kind],
        onActivate: (): void => {
          setSelectedStageId(attentionTabItem.stageId)
          openAttention(attentionTabItem.stageId)
        },
      }
    : null

  // Вкладки: Feed + контекстная detail-вкладка (тело воркспейса) + отдельный
  // маяк ожидания. Finding #7 (раунд 3): раньше единственная detail-вкладка
  // ПЕРЕИМЕНОВЫВАЛАСЬ в Approval/Question и становилась active, хотя тело всё ещё
  // показывало историю выбранной стадии — history «маскировалась» под attention,
  // а клик по «active» вкладке внезапно переключал тело на другую стадию. Теперь:
  //   • detail-вкладка отражает РЕАЛЬНОЕ тело (attention самой стадии ИЛИ история/
  //     имя выбранной стадии) и совпадает с содержимым;
  //   • ожидание, которое НЕ показано в теле, выносится в ОТДЕЛЬНУЮ glow-вкладку
  //     «beacon» — она никогда не active, клик по ней явно открывает attention.
  // Cost (increment 2) — ГЛОБАЛЬНЫЙ вид, как и Feed: отчёт по затратам не
  // привязан к выбранной стадии, поэтому detail-таб выбранной стадии в нём так
  // же неуместен, как и в Feed без содержимого. isGlobalView объединяет оба —
  // и duplicatesBeacon, и ветка ниже гейтятся ИМ, а не одним только 'feed'
  // (баг: Cost раньше вместе с маяком показывал ещё и detail-таб той же
  // стадии — два таба на одно и то же ожидание).
  const isGlobalView = wsState.view === 'feed' || wsState.view === 'cost'
  // В глобальном виде (Feed/Cost) detail-таб выбранной стадии дублирует
  // glow-маяк, если оба ведут в ОДНО и то же ожидание — т.е. у стадии есть своё
  // ожидание (contextKind) и маяк указывает на неё же. Тогда detail-таб
  // скрываем (см. else-ветку ниже), маяк остаётся. Если ждут несколько стадий и
  // маяк показывает ДРУГУЮ — не дубликат, detail-таб оставляем как прямой
  // доступ к ожиданию выбранной стадии.
  const duplicatesBeacon =
    isGlobalView &&
    contextKind !== null &&
    workspaceStage !== null &&
    attentionTabItem?.stageId === workspaceStage.id
  // Cost — постоянная вкладка (increment 2): глобальный отчёт по затратам, не
  // привязанный к выбранной стадии. Никогда не auto-open'ится (в отличие от
  // контекстных attention-вкладок ниже). Должна оставаться ПОСЛЕДНЕЙ в списке —
  // добавляется в конце, после detail/beacon вкладок (см. push ниже).
  const tabs: WorkspaceTabDescriptor[] = [{ id: 'feed', label: 'Feed' }]
  if (workspaceStage !== null) {
    if (inAttention && contextKind !== null) {
      // Тело показывает attention самой стадии → detail = attention (active, glow).
      tabs.push({
        id: 'detail',
        label: ATTENTION_TAB_LABEL[contextKind],
        kind: contextKind,
        count: countByKind(attnItems, contextKind),
        glow: true,
      })
    } else if (!isGlobalView || (wsState.view === 'feed' && !duplicatesBeacon && (workspaceStage.showPlan || workspaceStage.showDialog))) {
      // Тело показывает историю/детали выбранной стадии — вкладка это и отражает.
      // Не рисуем detail-таб в трёх случаях:
      //   • wsState.view === 'cost' — глобальный отчёт, не привязан ни к какой
      //     стадии, тело не показывает историю выбранной стадии вовсе (FIX 1);
      //   • duplicatesBeacon — он вёл бы в то же ожидание, что и glow-маяк
      //     (в Feed у стадии с собственным ожиданием), т.е. дубль маяка;
      //   • в Feed у стадии нет ни плана, ни диалога (напр. завершённая
      //     автономная стадия) — таб вёл бы в ту же ленту (Feed), пустышка, клик
      //     по которой ничего не менял. В history-виде (!isGlobalView) таб
      //     нужен всегда — он и есть активная вкладка истории.
      tabs.push({
        id: 'detail',
        label: workspaceStage.name !== '' ? workspaceStage.name : workspaceStage.id,
      })
    }
  }
  // Отдельный маяк ожидания — когда очередь непуста и мы НЕ в attention-виде
  // (в attention-виде ожидание уже показано detail-вкладкой). Никогда не active.
  const showBeacon = !inAttention && attentionTabItem !== null
  if (showBeacon && attentionTabItem !== null) {
    tabs.push({
      id: 'beacon',
      label: ATTENTION_TAB_LABEL[attentionTabItem.kind],
      kind: attentionTabItem.kind,
      count: countByKind(attnItems, attentionTabItem.kind),
      glow: true,
    })
  }
  // Cost всегда последней — после detail/beacon, а не второй вкладкой сразу
  // после Feed (иначе контекстные вкладки оказывались «за» ней).
  tabs.push({ id: 'cost', label: 'Cost' })
  const activeTabId = wsState.view === 'feed' ? 'feed' : wsState.view === 'cost' ? 'cost' : 'detail'
  function onSelectTab(id: string): void {
    if (id === 'feed') { openFeed(); return }
    if (id === 'cost') { openCost(); return }
    if (id === 'beacon') {
      // Явный переход к ждущему действию.
      if (attentionTabItem !== null) {
        setSelectedStageId(attentionTabItem.stageId)
        openAttention(attentionTabItem.stageId)
      }
      return
    }
    // detail — контекст выбранной стадии.
    if (workspaceStage === null) return
    if (contextKind !== null) openAttention(workspaceStage.id)
    else if (workspaceStage.showDialog) openHistory('dialog-history')
    else if (workspaceStage.showPlan) openHistory('plan-history')
    else openFeed()
  }

  // Единственная панель детали — по одной за раз (Question ИЛИ Approval ИЛИ
  // история), на всю доступную высоту. Раньше и PlanPanel, и DialogChannel
  // добавлялись вместе (оба flex:1) → вопрос получал полэкрана, а над/под ним
  // висела историческая панель плана. Теперь показываем ровно то, что относится
  // к текущему виду: attention-вопрос → диалог; attention-approval/failed/paused
  // → план (в нём же кнопки retry/Continue); история → соответствующая панель.
  // attention-шапка передаётся ВНУТРЬ панели (скролл-область), а не рендерится
  // над ней — так она уезжает вместе с контентом и не съедает высоту у больших
  // планов/вопросов (фиксированные кнопки/ответ остаются на месте).
  const attentionBanner = inAttention && contextKind !== null ? <AttentionBanner kind={contextKind} /> : undefined
  let detailPanel: ReactElement | null = null
  if (workspaceStage !== null) {
    if (wsState.view === 'attention') {
      detailPanel = contextKind === 'question'
        ? <DialogChannel key="dialog" stage={workspaceStage} attention banner={attentionBanner} />
        : <PlanPanel key="plan" stage={workspaceStage} attention={contextKind === 'approval'} banner={attentionBanner} />
    } else if (wsState.view === 'plan-history') {
      detailPanel = showPlan
        ? <PlanPanel key="plan" stage={workspaceStage} attention={false} />
        : showDialog ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} /> : null
    } else if (wsState.view === 'dialog-history') {
      detailPanel = showDialog
        ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} />
        : showPlan ? <PlanPanel key="plan" stage={workspaceStage} attention={false} /> : null
    }
  }

  return (
    <FileBrowserProvider flowName={flowName} startedAt={startedAt} enabled={capabilities.fileBrowser} flowPauseState={flowPauseState} onOpenChange={setFilesOpen} attentionShortcut={attentionShortcut}>
      <GlobalHeader
        flowName={flowName}
        description={description}
        connected={connected}
        attention={anyAttention}
        startedAt={startedAt}
        elapsedMs={elapsedMs}
        idleMs={idleMs}
        backoffMs={backoffMs}
        runCost={runCost}
        coverageIssues={coverageIssues}
        accounting={accounting}
        onOpenCost={handleOpenCost}
        notificationsPermission={notificationsPermission}
        notificationsEnabled={notificationsEnabled}
        onRequestEnableNotifications={onRequestEnableNotifications}
        onDisableNotifications={onDisableNotifications}
        capabilities={capabilities}
      />

      <ReviewBanner
        flowPauseState={flowPauseState}
        noteCount={reviewNoteCount}
        onSend={handleReviewSend}
        onCancel={handleReviewCancel}
      />

      <main id="main">
        <MaximizeProvider>
          <DashboardShell
            rail={
              <StagesList
                stages={stages}
                selectedStageId={workspaceStageId}
                onSelect={handleSelectStage}
                onAddNote={setNoteModalStageId}
                onEditPreNote={setPreNoteModalStageId}
                onPause={handlePause}
                onButton={handleButton}
                progressDone={stages.filter((s) => s.status === 'done').length}
                progressTotal={stages.length}
                accounting={accounting}
              />
            }
            tabs={<WorkspaceTabs tabs={tabs} activeId={activeTabId} onSelect={onSelectTab} />}
            workspace={
              <>
                {/* Контекст стадии воркспейса (имя + статус) — общая шапка и для
                    Feed, и для деталей (мокап показывает контекст стадии над
                    лентой). Cost — глобальный отчёт, не привязанный к стадии:
                    оставлять над ним имя/статус последней выбранной стадии
                    подразумевало бы несуществующий пер-стейдж фильтр. */}
                {workspaceStage !== null && wsState.view !== 'cost' && (
                  <WorkspaceHeader
                    stage={workspaceStage}
                    connected={connected}
                    attentionNav={
                      inAttention && attnIndex >= 0 && attnItems.length > 1
                        ? { pos: attnIndex + 1, total: attnItems.length, onPrev: () => goAttention(-1), onNext: () => goAttention(1) }
                        : undefined
                    }
                  />
                )}
                {wsState.view === 'cost' ? (
                  <CostPanel
                    stages={stages}
                    runCost={runCost}
                    runOverheadCost={runOverheadCost}
                    coverageIssues={coverageIssues}
                    accounting={accounting}
                  />
                ) : wsState.view === 'feed' || workspaceStage === null ? (
                  <FeedWorkspace events={events} logEntries={logEntries} stageId={workspaceStage?.id ?? null} />
                ) : detailPanel === null ? (
                  <div className="detail-empty empty-hint">Nothing to show for this stage</div>
                ) : (
                  <div className="detail-panels">
                    {/* Шапка-баннер контекстной вкладки («Plan needs your
                        approval» / «Agent needs your input») теперь рендерится
                        ВНУТРИ скролл-области панели (см. проп banner у
                        PlanPanel/DialogChannel), а не здесь над ней — чтобы она
                        уезжала со скроллом и не занимала высоту постоянно. */}
                    {/* Переключатель истории (Finding #2 второго раунда): у стадии,
                        задававшей вопрос во время planning/implementation, есть и
                        план, и диалог — иначе план стал бы недоступен навсегда
                        (клик всегда открывал dialog-history). Показываем сегмент
                        Plan | Dialog только в history-виде, когда доступны оба. */}
                    {!inAttention && showPlan && showDialog && (
                      <div className="history-switch" role="group" aria-label="History view">
                        <button
                          type="button"
                          className={`history-switch-btn${wsState.view === 'plan-history' ? ' active' : ''}`}
                          aria-pressed={wsState.view === 'plan-history'}
                          onClick={() => openHistory('plan-history')}
                        >
                          Plan
                        </button>
                        <button
                          type="button"
                          className={`history-switch-btn${wsState.view === 'dialog-history' ? ' active' : ''}`}
                          aria-pressed={wsState.view === 'dialog-history'}
                          onClick={() => openHistory('dialog-history')}
                        >
                          Dialog
                        </button>
                      </div>
                    )}
                    {detailPanel}
                  </div>
                )}
              </>
            }
          />
        </MaximizeProvider>
      </main>

      {noteModalStageId !== null && (
        <AgentNoteModal
          stageId={noteModalStageId}
          onCancel={() => setNoteModalStageId(null)}
          onSubmit={handleSubmitNote}
          attentionShortcut={attentionShortcut}
        />
      )}

      {preNoteModalStageId !== null && (
        <AgentNoteModal
          stageId={preNoteModalStageId}
          variant="prenote"
          initialNote={stages.find((s) => s.id === preNoteModalStageId)?.preNote ?? ''}
          onCancel={() => setPreNoteModalStageId(null)}
          onSubmit={handleSubmitPreNote}
          attentionShortcut={attentionShortcut}
        />
      )}

      {reviewModalOpen && (
        <ReviewNotesModal pausedStages={flowPausedStages} onClose={() => setReviewModalOpen(false)} attentionShortcut={attentionShortcut} />
      )}
    </FileBrowserProvider>
  )
}

// isEditableFocused — сейчас в фокусе редактируемый элемент (textarea/input/
// contenteditable)? Используется для suppression авто-открытия attention, чтобы
// не выдёргивать пользователя из набора текста (rule 9).
function isEditableFocused(): boolean {
  const el = document.activeElement
  if (el === null) return false
  const tag = el.tagName
  return tag === 'TEXTAREA' || tag === 'INPUT' || (el as HTMLElement).isContentEditable === true
}

// useIsEditing — реактивный сигнал «сейчас в фокусе редактируемый элемент».
// В отличие от разовой проверки isEditableFocused(), пригоден как зависимость
// (обновляется по focusin/focusout), поэтому его можно передать в workspace-
// редьюсер как suppressed: пока пользователь печатает, прибывшее ожидание лишь
// светится на вкладке, но фокус из ввода не крадётся (rule 9).
function useIsEditing(): boolean {
  const [editing, setEditing] = useState(false)
  useEffect(() => {
    const update = (): void => setEditing(isEditableFocused())
    document.addEventListener('focusin', update)
    document.addEventListener('focusout', update)
    return () => {
      document.removeEventListener('focusin', update)
      document.removeEventListener('focusout', update)
    }
  }, [])
  return editing
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
