import { useCallback, useEffect, useRef, useState, type ReactElement } from 'react'
import { cancelNotes, continueStage, pauseStage, reviseStage, setStageNote, triggerStageButton } from '../api/run-client'
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
import { SideAgentProvider } from '../components/side-agent'
import { ReviewBanner } from '../components/review-banner'
import { useStatus } from '../hooks/use-status'
import { useSelectedStage } from '../hooks/use-selected-stage'
import { useReviewNoteCount } from '../hooks/use-review-note-count'
import { useEventFeed } from '../hooks/use-event-feed'
import { useStageEvents } from '../hooks/use-stage-events'
import { useElapsed } from '../hooks/use-elapsed'
import { useIdleMs } from '../hooks/use-idle-ms'
import { useBackoffMs } from '../hooks/use-backoff-ms'
import { anyAwaiting } from '../hooks/use-attention'
import { attentionKindForStatus, countByKind, sig, useWorkspaceView, type AttentionKind } from '../hooks/use-workspace-view'
import { useTitleFlash } from '../hooks/use-title-flash'
import { useFaviconPulse } from '../hooks/use-favicon-pulse'
import { useDesktopNotifications } from '../hooks/use-desktop-notifications'
import { usePauseOnFocus } from '../hooks/use-pause-on-focus'
import { useIsEditing } from '../hooks/use-is-editing'
import { SIGNIFICANT_EVENT_TYPES } from '../types'

// Подписи контекстной вкладки воркспейса по виду attention (Approval/Question/…).
const ATTENTION_TAB_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval',
  question: 'Question',
  failed: 'Failed',
  hook_failed: 'Hook failed',
  paused: 'Paused',
}

// Подпись ждущего действия в шапке файлового оверлея.
const ATTENTION_SHORTCUT_LABEL: Record<AttentionKind, string> = {
  approval: 'Approval waiting',
  question: 'Question waiting',
  failed: 'Stage failed',
  hook_failed: 'Hook failed',
  paused: 'Stage paused',
}

// Корневая композиция: шапка, список стадий, панель деталей, лента событий, футер.
export function App(): ReactElement {
  const {
    flowName, stages, startedAt, endedAt, elapsedAccumulatedMs, elapsedSince,
    description, version, idleAccumulatedMs, idleSince, backoffAccumulatedMs,
    backoffOpenSince, capabilities, runId, flowPauseState, flowPausedStages, runCost,
    runOverheadCost, coverageIssues, accounting, statusAvailable, refresh,
  } = useStatus()

  // Модалка ревью-раунда — открывается по клику ReviewBanner's "Send notes".
  const [reviewModalOpen, setReviewModalOpen] = useState(false)

  // Стадия, для которой открыта модалка pre-note; null — модалка скрыта.
  const [preNoteModalStageId, setPreNoteModalStageId] = useState<string | null>(null)

  async function handleSubmitPreNote(note: string): Promise<void> {
    if (preNoteModalStageId === null) return
    try {
      await setStageNote(preNoteModalStageId, note)
      setPreNoteModalStageId(null)
    } catch (err) {
      // Как handleSubmitNote: не закрываем молча — оставляем модалку открытой с введённым текстом, чтобы
      // пользователь мог повторить (стадия могла уже стартовать, либо сеть отвалилась).
      console.error('Failed to save pre-note:', err)
    }
  }

  // «Pause on focus» — при включённом toggle фокус поля заметки ставит running- стадию на паузу, а
  // отправка сперва снимает паузу, затем доставляет заметку.
  const pauseOnFocus = usePauseOnFocus({ pauseStage, continueStage, reviseStage })

  function handlePause(stageId: string): void {
    // onPause вызывается без await из onClick; catch нужен для ошибок сети и сменившегося статуса стадии.
    pauseStage(stageId).catch((err: unknown) => {
      console.error('Failed to pause stage:', err)
    })
  }

  function handleButton(stageId: string, name: string): void {
    // Fire-immediately, без модалки: StagesList закрывает кебаб синхронно ДО вызова onButton.
    triggerStageButton(stageId, name).catch((err: unknown) => {
      console.error('Failed to trigger stage button:', err)
    })
  }

  const reviewNoteCount = useReviewNoteCount(flowPauseState === 'paused')

  // Отмена ревью не требует целевой стадии; fire-and-forget вызов обязательно обрабатывает reject.
  function handleReviewCancel(): void {
    cancelNotes().catch((err: unknown) => {
      console.error('Failed to cancel review notes:', err)
    })
  }

  // Открываем ReviewNotesModal для выбора целевой стадии перед injectNotes.
  function handleReviewSend(): void {
    setReviewModalOpen(true)
  }

  const wsUrl = buildWebSocketUrl()
  const { events, connected } = useEventFeed(wsUrl)

  // Единый workspace-view-редьюсер — источник истины для того, ЧТО показано справа от рейла: Feed,
  // контекстный attention или read-only просмотр истории плана/диалога.
  const [filesOpen, setFilesOpen] = useState(false)
  // sideAgentOpen — видимость «бокового» агента. Входит в anyModalOpen, чтобы attention других стадий не
  // авто-открывался за открытой беседой (только сигналил).
  const [sideAgentOpen, setSideAgentOpen] = useState(false)
  const otherModalsOpen = filesOpen || preNoteModalStageId !== null || reviewModalOpen
  const anyModalOpen = otherModalsOpen || sideAgentOpen
  // selfOwnedActive — у какой-то стадии есть незавершённая «Pause on focus»-операция.
  const selfOwnedActive = stages.some((s) => pauseOnFocus.shouldSuppressAttention(s.id))
  const editing = useIsEditing() || anyModalOpen || selfOwnedActive
  // ownedHandledSigs — подписи НАШИХ же paused-эпизодов: их auto-open подавляется навсегда, а не
  // откладывается.
  const ownedHandledSigs = stages
    .filter((s) => s.status === 'paused' && pauseOnFocus.shouldSuppressAttention(s.id))
    .map((s) => sig({ stageId: s.id, kind: 'paused', episode: s.updatedAt }))
  const { state: wsState, activeItem: attnItem, openFeed, openCost, openFullFeed, openAttention, openHistory } = useWorkspaceView(stages, editing, ownedHandledSigs)

  // Забываем Pause-on-focus ownership после внешнего Continue или завершения стадии, чтобы Send не вызвал
  // лишний Continue.
  useEffect(() => {
    pauseOnFocus.reconcile(stages.map((s) => ({ id: s.id, status: s.status })))
  }, [stages, pauseOnFocus.reconcile])
  const [selectedStageId, setSelectedStageId] = useSelectedStage(
    stages,
    wsState.view === 'attention' ? attnItem?.stageId ?? null : null,
  )

  // После активации Cost из поповера переносим фокус на вкладку Cost; inline-клик сохраняет свой фокус.
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
  // Защита: если accounting выключён на бэкенде, а вид застрял на 'cost' — уводим в Feed.
  useEffect(() => {
    if (!accounting.supported && wsState.view === 'cost') openFeed()
  }, [accounting.supported, wsState.view])

  // Стадия, о которой сейчас говорит воркспейс: в attention-режиме — активный элемент очереди; иначе —
  // выбранная в рейле стадия.
  const workspaceStageId = wsState.view === 'attention' && attnItem !== null ? attnItem.stageId : selectedStageId
  const workspaceStage = stages.find((stage) => stage.id === workspaceStageId) ?? null

  // stageEvents — лента вкладки Feed: только события ТЕКУЩЕЙ стадии воркспейса, а не весь событийный поток
  // флоу. Full feed (глобальный вид) продолжает читать сырые `events` напрямую.
  const stageEvents = useStageEvents(workspaceStage?.id ?? null, events)

  // Клик по стадии в рейле: выбираем её и открываем подходящий вид — attention, если стадия ждёт действия;
  // иначе Feed для завершённых/ещё не начавшихся стадий; историю живой стадии — если есть.
  function handleSelectStage(stageId: string): void {
    setSelectedStageId(stageId)
    // Ручной выбор другой стадии инвалидирует прицельный скролл к Q&A, заданный кликом по элементу ленты —
    // иначе, если у новой стадии случайно совпадёт phase/id, DialogChannel проскроллит не туда.
    setScrollToDialogTarget(null)
    const stage = stages.find((s) => s.id === stageId) ?? null
    const kind = stage === null ? null : attentionKindForStatus(stage.status)
    if (kind !== null) {
      openAttention(stageId)
      return
    }
    // Нет ожидающего действия.
    if (wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'full-feed') return
    if (stage?.status === 'done' || stage?.status === 'pending') openFeed()
    else if (stage?.showDialog === true) openHistory('dialog-history')
    else if (stage?.showPlan === true) openHistory('plan-history')
    else openFeed()
  }

  // Прицельный переход из ленты по клику на dialog_question/dialog_answer элемент: выбираем стадию,
  // открываем диалог и запоминаем, к какому именно Q&A прокрутить.
  const [scrollToDialogTarget, setScrollToDialogTarget] = useState<{ stageId: string; phase: string; id: string } | null>(null)
  function handleOpenDialogFromFeed(stageId: string, phase: string, id: string): void {
    setSelectedStageId(stageId)
    const stage = stages.find((s) => s.id === stageId) ?? null
    if (stage?.status === 'awaiting_user_input') openAttention(stageId)
    else openHistory('dialog-history')
    setScrollToDialogTarget({ stageId, phase, id })
  }

  // Вызывается DialogChannel'ом сразу после того, как scrollToDialogTarget был успешно применён — родитель
  // обязан сбросить цель, иначе она переживёт своё назначение.
  const handleDialogTargetConsumed = useCallback((): void => {
    setScrollToDialogTarget(null)
  }, [])

  // anyAttention (любая стадия прогона ждёт) — пульс favicon (шапочная точка
  // была убрана в этой ветке — пульс остался единственным глобальным сигналом).
  const anyAttention = anyAwaiting(stages)
  // Title flash завязан на ГЛОБАЛЬНОЕ непросмотренное ожидание, а не только на текущую workspace-стадию:
  // мигаем, когда в очереди есть ожидание, но воркспейс сейчас НЕ в attention-виде.
  useTitleFlash(wsState.items.length > 0 && wsState.view !== 'attention')
  useFaviconPulse(anyAttention)
  const {
    enabled: notificationsEnabled,
    permission: notificationsPermission,
    requestEnable: onRequestEnableNotifications,
    disable: onDisableNotifications,
  } = useDesktopNotifications(stages, handleSelectStage, flowName)

  // Заголовок вкладки берём из description флоу, иначе имя флоу, иначе дефолт.
  useEffect(() => {
    document.title = (description ?? '').trim() || flowName || 'afm Dashboard'
  }, [description, flowName])

  // showPlan/showDialog capabilities are computed server-side per stage — the client only adds the
  // "nothing selected → show both, neutral state" rule on top.
  const showPlan = workspaceStage === null || workspaceStage.showPlan
  const showDialog = workspaceStage === null || workspaceStage.showDialog

  // noteTarget — стадия, живому агенту которой можно послать заметку из messenger-поля ленты: running И
  // не скрипт, ЛИБО есть незавершённая Pause-on-focus операция (hasActiveOp) — композер остаётся
  // смонтированным на всём цикле pause→набор→send→resume, иначе поле и черновик исчезли бы, когда стадия
  // мигнула в paused/resuming. Прочие статусы/скрипты и flowPauseState!=='none' → null (поле не показываем).
  const noteTarget =
    workspaceStage !== null &&
    flowPauseState === 'none' &&
    !workspaceStage.isScript &&
    (workspaceStage.status === 'running' || pauseOnFocus.hasActiveOp(workspaceStage.id))
      ? workspaceStage.id
      : null

  const statusLive = statusAvailable
  const elapsedMs = useElapsed(startedAt, endedAt, statusLive, elapsedAccumulatedMs, elapsedSince)
  const idleMs = useIdleMs(idleAccumulatedMs, idleSince, statusLive)
  const backoffMs = useBackoffMs(backoffAccumulatedMs, backoffOpenSince, statusLive)

  const lastRefreshedEvent = useRef<object | null>(null)

  // WebSocket как канал обновления: значимое событие → ре-запрос состояния флоу.
  useEffect(() => {
    if (events.length === 0) return

    // Сравниваем ссылку на последнее событие: длина ленты перестаёт меняться после MAX_EVENTS.
    const latest = events[events.length - 1]
    if (latest === undefined || latest === lastRefreshedEvent.current) return

    lastRefreshedEvent.current = latest
    if (SIGNIFICANT_EVENT_TYPES.has(latest.type)) {
      refresh()
    }
  }, [events, refresh])

  // Очередь attention и позиция активного элемента — для навигации prev/next между несколькими стадиями,
  // ждущими действия. Очередь ведёт сам редьюсер (wsState.items).
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

  // Клик по первому нерешённому ожиданию закрывает Files и открывает attention.
  const attentionShortcut = attentionTabItem !== null
    ? {
        label: ATTENTION_SHORTCUT_LABEL[attentionTabItem.kind],
        onActivate: (): void => {
          setSelectedStageId(attentionTabItem.stageId)
          openAttention(attentionTabItem.stageId)
        },
      }
    : null

  // Вкладки: Feed + контекстная detail-вкладка + отдельный маяк ожидания.
  const isGlobalView = wsState.view === 'feed' || wsState.view === 'cost' || wsState.view === 'full-feed'
  // В глобальном виде detail-таб выбранной стадии дублирует glow-маяк, если оба ведут в ОДНО и то же
  // ожидание — т.е. у стадии есть своё ожидание и маяк указывает на неё же.
  const duplicatesBeacon =
    isGlobalView &&
    contextKind !== null &&
    workspaceStage !== null &&
    attentionTabItem?.stageId === workspaceStage.id
  // Cost — постоянная вкладка: глобальный отчёт по затратам, не привязанный к выбранной стадии. Никогда не
  // auto-open'ится (в отличие от контекстных attention-вкладок ниже).
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
    } else if (!isGlobalView || (wsState.view === 'feed' && workspaceStage.status !== 'pending' && !duplicatesBeacon && (workspaceStage.showPlan || workspaceStage.showDialog))) {
      // Тело показывает историю/детали выбранной стадии — вкладка это и отражает. В history-виде
      // (!isGlobalView) таб нужен всегда — он и есть активная вкладка истории.
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
  // Cost всегда последней — после detail/beacon, а не второй вкладкой сразу после Feed.
  if (accounting.supported) {
    tabs.push({ id: 'cost', label: 'Cost' })
  }
  // Full feed — постоянная ГЛОБАЛЬНАЯ вкладка, читает весь событийный поток флоу. Всегда ПОСЛЕДНЯЯ в
  // списке — правее Cost, а не второй сразу после Feed (тот же порядок, что и у Cost выше).
  tabs.push({ id: 'full-feed', label: 'Full feed' })
  const activeTabId = wsState.view === 'feed' ? 'feed' : wsState.view === 'cost' ? 'cost' : wsState.view === 'full-feed' ? 'full-feed' : 'detail'
  function onSelectTab(id: string): void {
    if (id === 'feed') { openFeed(); return }
    if (id === 'cost') { openCost(); return }
    if (id === 'full-feed') { openFullFeed(); return }
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

  // Единственная панель детали — по одной за раз, на всю доступную высоту.
  const attentionBanner = inAttention && contextKind !== null ? <AttentionBanner kind={contextKind} /> : undefined
  let detailPanel: ReactElement | null = null
  if (workspaceStage !== null) {
    if (wsState.view === 'attention') {
      detailPanel = contextKind === 'question'
        ? <DialogChannel key="dialog" stage={workspaceStage} attention banner={attentionBanner} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} />
        : <PlanPanel key="plan" stage={workspaceStage} attention={contextKind === 'approval'} banner={attentionBanner} />
    } else if (wsState.view === 'plan-history') {
      detailPanel = showPlan
        ? <PlanPanel key="plan" stage={workspaceStage} attention={false} />
        : showDialog ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} /> : null
    } else if (wsState.view === 'dialog-history') {
      detailPanel = showDialog
        ? <DialogChannel key="dialog" stage={workspaceStage} attention={false} scrollTarget={scrollToDialogTarget} onTargetConsumed={handleDialogTargetConsumed} />
        : showPlan ? <PlanPanel key="plan" stage={workspaceStage} attention={false} /> : null
    }
  }

  return (
    <FileBrowserProvider flowName={flowName} startedAt={startedAt} enabled={capabilities.fileBrowser} flowPauseState={flowPauseState} onOpenChange={setFilesOpen} attentionShortcut={attentionShortcut}>
      <SideAgentProvider runId={runId} enabled={capabilities.sideAgent} blocked={otherModalsOpen} onOpenChange={setSideAgentOpen} attentionShortcut={attentionShortcut}>
      <GlobalHeader
        flowName={flowName}
        description={description}
        connected={connected}
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
                onEditPreNote={setPreNoteModalStageId}
                onPause={handlePause}
                onButton={handleButton}
                progressDone={stages.filter((s) => s.status === 'done').length}
                progressTotal={stages.length}
                accounting={accounting}
                version={version}
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
                {workspaceStage !== null && wsState.view !== 'cost' && wsState.view !== 'full-feed' && (
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
                ) : wsState.view === 'full-feed' ? (
                  <FeedWorkspace
                    events={events}
                    stageId={null}
                    scopeToken="scope:full"
                    showStageBadges
                    onOpenDialog={handleOpenDialogFromFeed}
                    emptyHint="No events yet"
                  />
                ) : wsState.view === 'feed' || workspaceStage === null ? (
                  <FeedWorkspace
                    events={stageEvents}
                    stageId={workspaceStage?.id ?? null}
                    // scopeToken не нужен: дефолт FeedWorkspace из stageId даёт ровно `scope:stage:<id>` / `scope:none`.
                    // Явный токен нужен только Full-feed-сайту (scope:full), который переопределяет дефолт.
                    emptyHint={workspaceStage === null ? 'Select a stage to see its feed' : 'No events for this stage yet'}
                    onOpenDialog={handleOpenDialogFromFeed}
                    noteTarget={noteTarget}
                    onSendNote={pauseOnFocus.handleSend}
                    pauseOnFocusEnabled={pauseOnFocus.enabled}
                    onTogglePauseOnFocus={pauseOnFocus.setEnabled}
                    composerPaused={
                      workspaceStage !== null &&
                      workspaceStage.status === 'paused' &&
                      pauseOnFocus.isOwned(workspaceStage.id)
                    }
                    onComposerFocus={() =>
                      workspaceStage !== null && pauseOnFocus.onFocus(workspaceStage.id, workspaceStage.status)
                    }
                    onComposerBlur={(hasDraft) =>
                      workspaceStage !== null && pauseOnFocus.onBlur(workspaceStage.id, hasDraft)
                    }
                    onComposerResumeNow={() =>
                      workspaceStage !== null && pauseOnFocus.resumeNow(workspaceStage.id)
                    }
                  />
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
      </SideAgentProvider>
    </FileBrowserProvider>
  )
}

function buildWebSocketUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

  return `${protocol}//${window.location.host}/ws`
}
