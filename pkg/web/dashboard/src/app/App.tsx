import { useEffect, useRef, useState, type ReactElement } from 'react'
import { reviseStage } from '../api/run-client'
import { FlowHeader } from '../components/flow-header'
import { StagesList } from '../components/stages-list'
import { AgentNoteModal } from '../components/agent-note-modal'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { SupervisorDecision } from '../components/supervisor-decision'
import { EventFeedPanel } from '../components/event-feed'
import { Footer } from '../components/footer'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardLayout } from '../components/layout/DashboardLayout'
import { useStatus } from '../hooks/use-status'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageLog } from '../hooks/use-stage-log'
import { useElapsed } from '../hooks/use-elapsed'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
import { anyAwaiting, useAttention } from '../hooks/use-attention'
import { useTitleFlash } from '../hooks/use-title-flash'
import { useFaviconPulse } from '../hooks/use-favicon-pulse'
import { useDesktopNotifications } from '../hooks/use-desktop-notifications'
import { ACTIVE_STAGE_STATUSES, SIGNIFICANT_EVENT_TYPES, STAGE_STATUS_LABELS } from '../types'

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
// Владеет состоянием выбора текущей стадии; WebSocket работает как канал обновления
// состояния — по значимым событиям ре-запрашивает /api/status.
export function App(): ReactElement {
  const { flowName, stages, startedAt, description, idleAccumulatedMs, idleSince, backoffAccumulatedMs, backoffOpenSince, refresh } = useStatus()

  // Стадия, для которой сейчас открыта модалка «Добавить поправку агенту»
  // (agent_suggest, Task 8); null — модалка скрыта.
  const [noteModalStageId, setNoteModalStageId] = useState<string | null>(null)

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

  const wsUrl = buildWebSocketUrl()
  const { events, connected } = useEventFeed(wsUrl)

  const [selectedStageId, setSelectedStageId] = useState<string | null>(null)
  const selectedStage = stages.find((stage) => stage.id === selectedStageId) ?? null

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
  } = useDesktopNotifications(stages, setSelectedStageId)

  // Заголовок вкладки берём из description флоу (из flow.yaml), иначе имя флоу,
  // иначе дефолт. useTitleFlash мигает вокруг текущего title и восстанавливает
  // его, поэтому базовый заголовок можно менять здесь без конфликта.
  useEffect(() => {
    document.title = (description ?? '').trim() || flowName || 'afm Dashboard'
  }, [description, flowName])

  // Единожды прокрутить центральную колонку к панели, которой нужно действие,
  // в момент перехода kind null→'plan'/'dialog' (или смены самой панели).
  // PanelFrame ставит data-panel={maximizeId} на <section> — по нему и ищем.
  const lastKind = useRef<typeof attention.kind>(null)
  useEffect(() => {
    if (attention.kind !== null && lastKind.current !== attention.kind) {
      const sel = `[data-panel="${attention.kind}"]`
      document.querySelector(sel)?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
    }
    lastKind.current = attention.kind
  }, [attention.kind])

  // showPlan/showDialog capabilities are computed server-side per stage (see
  // pkg/server/stageview.go's StageView.ShowPlan/ShowDialog) — the client only
  // adds the "nothing selected → show both, neutral state" rule on top.
  const showPlan = selectedStage === null || selectedStage.showPlan
  const showDialog = selectedStage === null || selectedStage.showDialog

  const logEntries = useStageLog(selectedStageId)
  const elapsedMs = useElapsed(startedAt)
  const idleMs = useIdleMs(idleAccumulatedMs, idleSince, connected)
  const backoffMs = useBackoffMs(backoffAccumulatedMs, backoffOpenSince, connected)

  const refreshedForEvent = useRef<number>(-1)

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
    if (refreshedForEvent.current === events.length) return

    refreshedForEvent.current = events.length

    const latest = events[events.length - 1]
    if (latest !== undefined && SIGNIFICANT_EVENT_TYPES.has(latest.type)) {
      refresh()
    }
  }, [events, refresh])

  return (
    <>
      <FlowHeader
        flowName={flowName}
        connected={connected}
        attention={anyAttention}
        description={description}
        notificationsPermission={notificationsPermission}
        notificationsEnabled={notificationsEnabled}
        onRequestEnableNotifications={onRequestEnableNotifications}
        onDisableNotifications={onDisableNotifications}
      />

      <main id="main">
        <div className="ray" aria-hidden="true" />

        <MaximizeProvider>
          <DashboardLayout
            stages={
              <StagesList
                stages={stages}
                selectedStageId={selectedStageId}
                onSelect={setSelectedStageId}
                onAddNote={setNoteModalStageId}
              />
            }
            stageHeader={
              selectedStage === null ? null : (
                <>
                  <h2 id="detail-title">{selectedStage.name !== '' ? selectedStage.name : selectedStage.id}</h2>
                  <span className="status-badge-wrap">
                    <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
                      {STAGE_STATUS_LABELS[selectedStage.status]}
                    </span>
                    {selectedStage.status === 'running' && connected && (
                      <span className="thinking" aria-hidden="true">
                        <span className="td" />
                        <span className="td" />
                        <span className="td" />
                        thinking
                      </span>
                    )}
                    {selectedStageId != null && <SupervisorDecision stageId={selectedStageId} />}
                  </span>
                  <span className="ornament" aria-hidden="true">
                    <svg viewBox="0 0 100 100" fill="none" stroke="#6fd4cc" strokeWidth="1">
                      <circle cx="50" cy="50" r="46" />
                      <circle cx="50" cy="50" r="32" />
                      <path d="M50 6 L52 50 L50 94 L48 50 Z" fill="#6fd4cc" stroke="none" />
                      <path d="M6 50 L50 48 L94 50 L50 52 Z" fill="#6fd4cc" stroke="none" />
                      <circle cx="50" cy="50" r="3" fill="#e5d442" stroke="none" />
                    </svg>
                  </span>
                </>
              )
            }
            plan={showPlan ? <PlanPanel stage={selectedStage} attention={attention.kind === 'plan'} /> : null}
            dialog={showDialog ? <DialogChannel stage={selectedStage} attention={attention.kind === 'dialog'} /> : null}
            feed={<EventFeedPanel events={events} logEntries={logEntries} />}
          />
        </MaximizeProvider>
      </main>

      <Footer stages={stages} startedAt={startedAt} elapsedMs={elapsedMs} idleMs={idleMs} backoffMs={backoffMs} />

      {noteModalStageId !== null && (
        <AgentNoteModal
          stageId={noteModalStageId}
          onCancel={() => setNoteModalStageId(null)}
          onSubmit={handleSubmitNote}
        />
      )}
    </>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
