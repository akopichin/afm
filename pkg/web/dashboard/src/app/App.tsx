import { useEffect, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, listNotes, pauseStage, reviseStage, setStageNote, triggerStageButton } from '../api/run-client'
import { GlobalHeader } from '../components/global-header'
import { StagesList } from '../components/stages-list'
import { AgentNoteModal } from '../components/agent-note-modal'
import { ReviewNotesModal } from '../components/review-notes-modal'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { EventFeedPanel } from '../components/event-feed'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardShell } from '../components/layout/DashboardShell'
import { WorkspaceTabs, WorkspaceHeader, type WorkspaceTabDescriptor } from '../components/workspace'
import { FileBrowserProvider } from '../components/file-browser'
import { ReviewBanner } from '../components/review-banner'
import { useStatus } from '../hooks/use-status'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageLog } from '../hooks/use-stage-log'
import { useElapsed } from '../hooks/use-elapsed'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
import { anyAwaiting, useAttention } from '../hooks/use-attention'
import { attentionKindForStatus, type AttentionKind } from '../hooks/use-workspace-view'
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

// Активная вкладка воркспейса: постоянная лента событий либо детали выбранной
// стадии (план/диалог/восстановление). Полноценная attention-навигация с
// очередью на нескольких стадиях (workspace-view-редьюсер) — в M3; здесь простое
// локальное состояние с авто-переключением на detail при появлении требующего
// действия статуса у выбранной стадии.
type WorkspaceTab = 'feed' | 'detail'

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
// Владеет состоянием выбора текущей стадии; WebSocket работает как канал обновления
// состояния — по значимым событиям ре-запрашивает /api/status.
export function App(): ReactElement {
  const { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince, capabilities, flowPauseState, flowPausedStages, refresh } = useStatus()

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
  const selectedStage = stages.find((stage) => stage.id === selectedStageId) ?? null

  // Активная вкладка воркспейса. 'feed' по умолчанию; авто-переключение на
  // 'detail' — только при появлении требующего действия статуса у выбранной
  // стадии (см. эффект ниже), НЕ на каждую смену выбора: авто-продвижение
  // wasLive не должно вырывать пользователя из ленты.
  const [activeTab, setActiveTab] = useState<WorkspaceTab>('feed')

  // Ручной выбор стадии (клик в рейле, клик по desktop-уведомлению) — показываем
  // её детали. Отличается от авто-продвижения (setSelectedStageId напрямую в
  // эффекте ниже), которое вкладку НЕ трогает.
  function handleSelectStage(stageId: string): void {
    setSelectedStageId(stageId)
    setActiveTab('detail')
  }

  // Attention-сигнал выбранной стадии: kind='dialog' (awaiting_user_input) или
  // 'plan' (awaiting_approval). needsAttention кормит title-flash для фоновой
  // вкладки, anyAttention — точку в шапке И пульс favicon (хотя бы одна
  // стадия прогона ждёт юзера, а не только выбранная).
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)
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

  // Авто-переключение на вкладку деталей при ПОЯВЛЕНИИ требующего действия
  // статуса у выбранной стадии (attention-arrival: null→approval/question/…).
  // Пришло на смену прежней прокрутке к панели действия (data-panel) — в
  // единой воркспейс-модели панель не «одна из нескольких», а сам воркспейс.
  // Ключ — id+kind: перезагорается и когда та же стадия входит в attention
  // повторно (например, после retry). Полноценная политика авто-открытия (один
  // раз, с suppression при печати/просмотре файла) появится в M3 вместе с
  // workspace-view-редьюсером; здесь — минимальное «показать действие».
  const lastAttentionKey = useRef<string | null>(null)
  useEffect(() => {
    const kind = selectedStage === null ? null : attentionKindForStatus(selectedStage.status)
    const key = kind === null || selectedStage === null ? null : `${selectedStage.id}:${kind}`
    if (key !== null && key !== lastAttentionKey.current) {
      setActiveTab('detail')
    }
    lastAttentionKey.current = key
  }, [selectedStage])

  // showPlan/showDialog capabilities are computed server-side per stage (see
  // pkg/server/stageview.go's StageView.ShowPlan/ShowDialog) — the client only
  // adds the "nothing selected → show both, neutral state" rule on top.
  const showPlan = selectedStage === null || selectedStage.showPlan
  const showDialog = selectedStage === null || selectedStage.showDialog

  const logEntries = useStageLog(selectedStageId)
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

  // Вкладки воркспейса: постоянная Feed + контекстная вкладка выбранной стадии.
  const detailKind = selectedStage === null ? null : attentionKindForStatus(selectedStage.status)
  // Счётчик «· N» — сколько стадий прогона сейчас в том же виде attention.
  const attentionCount = detailKind === null ? 0 : stages.filter((s) => attentionKindForStatus(s.status) === detailKind).length
  const tabs: WorkspaceTabDescriptor[] = [{ id: 'feed', label: 'Feed' }]
  if (selectedStage !== null) {
    tabs.push({
      id: 'detail',
      label: detailKind !== null ? ATTENTION_TAB_LABEL[detailKind] : (selectedStage.name !== '' ? selectedStage.name : selectedStage.id),
      kind: detailKind ?? undefined,
      count: detailKind !== null ? attentionCount : undefined,
      glow: detailKind !== null,
    })
  }
  // 'detail' валидна только при выбранной стадии; иначе всегда 'feed'.
  const effectiveTab: WorkspaceTab = selectedStage === null ? 'feed' : activeTab

  const detailPanels: ReactElement[] = []
  if (selectedStage !== null && showPlan) {
    detailPanels.push(<PlanPanel key="plan" stage={selectedStage} attention={detailKind === 'approval'} />)
  }
  if (selectedStage !== null && showDialog) {
    detailPanels.push(<DialogChannel key="dialog" stage={selectedStage} attention={detailKind === 'question'} />)
  }

  return (
    <FileBrowserProvider flowName={flowName} startedAt={startedAt} enabled={capabilities.fileBrowser} flowPauseState={flowPauseState}>
      <GlobalHeader
        flowName={flowName}
        description={description}
        connected={connected}
        attention={anyAttention}
        startedAt={startedAt}
        elapsedMs={elapsedMs}
        idleMs={idleMs}
        backoffMs={backoffMs}
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
                selectedStageId={selectedStageId}
                onSelect={handleSelectStage}
                onAddNote={setNoteModalStageId}
                onEditPreNote={setPreNoteModalStageId}
                onPause={handlePause}
                onButton={handleButton}
                progressDone={stages.filter((s) => s.status === 'done').length}
                progressTotal={stages.length}
              />
            }
            tabs={<WorkspaceTabs tabs={tabs} activeId={effectiveTab} onSelect={(id) => setActiveTab(id as WorkspaceTab)} />}
            workspace={
              <>
                {/* Контекст выбранной стадии (имя + статус) — общая шапка
                    воркспейса и для Feed, и для деталей (мокап показывает
                    контекст стадии над лентой). */}
                {selectedStage !== null && <WorkspaceHeader stage={selectedStage} connected={connected} />}
                {effectiveTab === 'feed' || selectedStage === null ? (
                  <EventFeedPanel events={events} logEntries={logEntries} />
                ) : detailPanels.length === 0 ? (
                  <div className="detail-empty empty-hint">Nothing to show for this stage</div>
                ) : (
                  <div className="detail-panels">{detailPanels}</div>
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
        />
      )}

      {preNoteModalStageId !== null && (
        <AgentNoteModal
          stageId={preNoteModalStageId}
          variant="prenote"
          initialNote={stages.find((s) => s.id === preNoteModalStageId)?.preNote ?? ''}
          onCancel={() => setPreNoteModalStageId(null)}
          onSubmit={handleSubmitPreNote}
        />
      )}

      {reviewModalOpen && (
        <ReviewNotesModal pausedStages={flowPausedStages} onClose={() => setReviewModalOpen(false)} />
      )}
    </FileBrowserProvider>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
