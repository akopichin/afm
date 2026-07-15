import { useEffect, useRef, useState, type ReactElement } from 'react'
import { FlowHeader } from '../components/flow-header'
import { StagesList } from '../components/stages-list'
import { PlanPanel } from '../components/plan-panel'
import { DialogChannel } from '../components/dialog-channel'
import { LogPanel } from '../components/log-panel'
import { EventFeedPanel } from '../components/event-feed'
import { Footer } from '../components/footer'
import { MaximizeProvider } from '../components/layout/Maximizable'
import { DashboardLayout } from '../components/layout/DashboardLayout'
import { useStatus } from '../hooks/use-status'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageLog } from '../hooks/use-stage-log'
import { useElapsed } from '../hooks/use-elapsed'
import { anyAwaiting, useAttention } from '../hooks/use-attention'
import { useTitleFlash } from '../hooks/use-title-flash'
import { ACTIVE_STAGE_STATUSES, SIGNIFICANT_EVENT_TYPES, STAGE_STATUS_LABELS } from '../types'
import type { Stage } from '../types'

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
// Владеет состоянием выбора текущей стадии; WebSocket работает как канал обновления
// состояния — по значимым событиям ре-запрашивает /api/status.
export function App(): ReactElement {
  const { flowName, stages, startedAt, refresh } = useStatus()

  const wsUrl = buildWebSocketUrl()
  const { events, connected } = useEventFeed(wsUrl)

  const [selectedStageId, setSelectedStageId] = useState<string | null>(null)
  const selectedStage = stages.find((stage) => stage.id === selectedStageId) ?? null

  // Attention-сигнал выбранной стадии: kind='dialog' (awaiting_user_input) или
  // 'plan' (awaiting_approval). needsAttention кормит title-flash для фоновой
  // вкладки, anyAttention — точку в шапке (хотя бы одна стадия ждёт юзера).
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)

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

  // Панели (PlanPanel/DialogChannel) требуют Stage, а не Stage | null, но в
  // resizable-лейауте они должны быть смонтированы всегда — иначе id панелей
  // скачут и сохранённый layout (afm-rows) перестаёт совпадать. Когда стадия
  // не выбрана, рендерим панели с нейтральным sentinel'ом: они уходят в пустое
  // состояние (GET /api/stages//plan → 404 → early-return), а stageHeader
  // остаётся null и показывает заглушку «выберите стадию».
  const NO_STAGE: Stage = { id: '', name: '', status: 'pending', updatedAt: '' }
  const stageForPanels = selectedStage ?? NO_STAGE

  const logEntries = useStageLog(selectedStageId)
  const elapsedMs = useElapsed(startedAt)

  const refreshedForEvent = useRef<number>(-1)

  // Автовыбор активной стадии (иначе первая failed); продвижение к следующей активной,
  // когда выбранная завершилась (done). Соответствует loadState/handleEvent в app.js.
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

    if (current.status === 'done') {
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
      <FlowHeader flowName={flowName} connected={connected} attention={anyAttention} />

      <main id="main">
        <div className="ray" aria-hidden="true" />

        <MaximizeProvider>
          <DashboardLayout
            stages={<StagesList stages={stages} selectedStageId={selectedStageId} onSelect={setSelectedStageId} />}
            stageHeader={
              selectedStage === null ? null : (
                <>
                  <h2 id="detail-title">{selectedStage.name !== '' ? selectedStage.name : selectedStage.id}</h2>
                  <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
                    {STAGE_STATUS_LABELS[selectedStage.status]}
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
            plan={<PlanPanel stage={stageForPanels} attention={attention.kind === 'plan'} />}
            dialog={<DialogChannel stage={stageForPanels} attention={attention.kind === 'dialog'} />}
            log={<LogPanel entries={logEntries} />}
            feed={<EventFeedPanel events={events} />}
          />
        </MaximizeProvider>
      </main>

      <Footer stages={stages} startedAt={startedAt} elapsedMs={elapsedMs} />
    </>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
